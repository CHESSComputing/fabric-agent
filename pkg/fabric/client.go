package fabric

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/CHESSComputing/fabric-agent/pkg/config"
	"github.com/CHESSComputing/fabric-agent/pkg/sparql"
)

type Dataset struct {
	DID        string                 `json:"did"`
	Name       string                 `json:"name"`
	Beamline   string                 `json:"beamline"`
	GraphURI   string                 `json:"graph_uri"`
	DataFormat string                 `json:"data_format"`
	Metadata   map[string]interface{} `json:"metadata"`
	CreatedAt  string                 `json:"created_at"`
}

type GraphsResponse struct {
	Graphs []string `json:"graphs"`
}

type Client struct {
	cfg        *config.FabricNodeConfig
	httpClient *http.Client
}

func NewClient(cfg *config.FabricNodeConfig) *Client {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{cfg: cfg, httpClient: &http.Client{Timeout: timeout}}
}

const maxBody = 32 << 20

func (c *Client) newRequest(ctx context.Context, method, u string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if c.cfg.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.AuthToken)
	}
	return req, nil
}

// do executes req and returns the body of a 2xx response. Any other status is
// an error that includes the start of the body, so callers (and the LLM repair
// loop) can see what the server complained about.
func (c *Client) do(req *http.Request) ([]byte, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 600 {
			msg = msg[:600] + "..."
		}
		return nil, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, req.URL.Path, msg)
	}
	return data, nil
}

// ListGraphs returns the named graphs registered in the Data Service.
func (c *Client) ListGraphs(ctx context.Context) ([]string, error) {
	req, err := c.newRequest(ctx, http.MethodGet, c.cfg.DataURL+"/graphs", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	data, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("listing graphs: %w", err)
	}
	var gr GraphsResponse
	if err := json.Unmarshal(data, &gr); err != nil {
		return nil, fmt.Errorf("decoding graphs response: %w", err)
	}
	return gr.Graphs, nil
}

// WellKnown fetches a self-description document from the Catalog Service,
// e.g. "void", "shacl" or "sparql-examples" (/.well-known/<name>).
func (c *Client) WellKnown(ctx context.Context, name string) (string, error) {
	if c.cfg.CatalogURL == "" {
		return "", fmt.Errorf("fabric_node.catalog_url is not configured")
	}
	req, err := c.newRequest(ctx, http.MethodGet, c.cfg.CatalogURL+"/.well-known/"+name, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/turtle, application/ld+json;q=0.9, application/json;q=0.8, */*;q=0.1")
	data, err := c.do(req)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Query runs a SPARQL query against the Data Service and parses the answer.
func (c *Client) Query(ctx context.Context, q string) (*sparql.Result, error) {
	const accept = "application/sparql-results+json, application/json;q=0.9, text/turtle;q=0.5, */*;q=0.1"
	endpoint := c.cfg.DataURL + "/sparql"

	var req *http.Request
	var err error
	if len(q) > 4000 { // keep long queries out of the URL
		form := url.Values{"query": {q}}
		req, err = c.newRequest(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	} else {
		req, err = c.newRequest(ctx, http.MethodGet, endpoint+"?query="+url.QueryEscape(q), nil)
	}
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)

	data, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("SPARQL query failed: %w", err)
	}
	return sparql.ParseResults(data), nil
}

var unsafeFileChars = regexp.MustCompile(`[^A-Za-z0-9._=-]+`)

// SafeName turns an IRI or DID into something usable as a file name.
func SafeName(s string) string {
	s = strings.Trim(unsafeFileChars.ReplaceAllString(s, "_"), "_")
	if len(s) > 120 {
		s = s[len(s)-120:]
	}
	if s == "" {
		s = "unnamed"
	}
	return s
}

// DownloadGraph fetches the triples of one named graph (using the configured
// graph_dump_query template) and stores the raw response in destDir.
func (c *Client) DownloadGraph(ctx context.Context, graphURI, destDir string) (string, error) {
	if !sparql.IsSafeIRI(graphURI) {
		return "", fmt.Errorf("refusing to query graph with unsafe IRI %q", graphURI)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}

	tmpl := c.cfg.GraphDumpQuery
	if tmpl == "" {
		tmpl = config.DefaultGraphDumpQuery
	}
	limit := c.cfg.GraphDumpLimit
	if limit <= 0 {
		limit = 50
	}
	q := strings.NewReplacer("{{graph}}", graphURI, "{{limit}}", strconv.Itoa(limit)).Replace(tmpl)

	res, err := c.Query(ctx, q)
	if err != nil {
		return "", fmt.Errorf("downloading graph %s: %w", graphURI, err)
	}

	filePath := filepath.Join(destDir, SafeName(graphURI)+".json")
	if err := os.WriteFile(filePath, res.Raw, 0o644); err != nil {
		return "", err
	}
	return filePath, nil
}

// RegisterDataset posts an LDN "Create" activity to the Notification Service
// inbox. Unlike earlier versions it reports failures instead of pretending the
// dataset was registered.
func (c *Client) RegisterDataset(ctx context.Context, dset Dataset) error {
	inboxURL := c.cfg.NotificationURL + "/inbox"

	payload := map[string]interface{}{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id":       fmt.Sprintf("urn:uuid:%d", time.Now().UnixNano()),
		"type":     "Create",
		"actor":    "https://chess.cornell.edu/agent/fabric-ai-agent",
		"object": map[string]interface{}{
			"id":          dset.GraphURI,
			"type":        "https://chess.cornell.edu/ns#DerivedDataset",
			"name":        dset.Name,
			"beamline":    dset.Beamline,
			"parentGraph": dset.Metadata["parent_graph"],
		},
		"published": time.Now().Format(time.RFC3339),
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := c.newRequest(ctx, http.MethodPost, inboxURL, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/ld+json")
	if _, err := c.do(req); err != nil {
		return fmt.Errorf("LDN notification failed: %w", err)
	}
	return nil
}

// DatasetFromGraph derives a Dataset description from a graph IRI. It
// understands path segments of the form sample_name=<x> and beamline=<y>.
func DatasetFromGraph(gURI string) Dataset {
	sampleName := "unknown-sample"
	beamline := "general"

	for _, p := range strings.Split(gURI, "/") {
		switch {
		case strings.HasPrefix(p, "sample_name="):
			sampleName = strings.TrimPrefix(p, "sample_name=")
		case strings.HasPrefix(p, "beamline="):
			beamline = strings.TrimPrefix(p, "beamline=")
		case p == "fast" || p == "id1" || p == "id3a" || p == "3a":
			beamline = p
		}
	}

	return Dataset{
		DID:        fmt.Sprintf("did:chess:%s:%s", beamline, sampleName),
		Name:       sampleName,
		Beamline:   beamline,
		GraphURI:   gURI,
		DataFormat: "RDF/Turtle Graph",
		Metadata:   map[string]interface{}{"graph_uri": gURI},
		CreatedAt:  time.Now().Format(time.RFC3339),
	}
}
