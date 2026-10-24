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
	"strings"
	"time"

	"github.com/CHESSComputing/fabric-agent/pkg/config"
	"github.com/CHESSComputing/fabric-agent/pkg/logx"
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
		req.Header.Set("Authorization", "Bearer "+c.cfg.AuthToken) // never logged
	}
	return req, nil
}

// do executes req and returns the body of a 2xx response. Any other status is
// an error that includes the start of the body, so callers (and the LLM repair
// loop) can see what the server complained about. Every call is logged.
func (c *Client) do(req *http.Request) ([]byte, error) {
	start := time.Now()
	logx.Infof("node API  %s %s", req.Method, req.URL.String())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		logx.Infof("node API  no response after %s: %v", time.Since(start).Round(time.Millisecond), err)
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		logx.Infof("node API  reading body failed after %s: %v", time.Since(start).Round(time.Millisecond), err)
		return nil, err
	}
	logx.Infof("node API  -> HTTP %d, %d bytes, %s", resp.StatusCode, len(data), time.Since(start).Round(time.Millisecond))
	logx.Block(logx.Debug, "node API response body", string(data))

	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 600 {
			msg = msg[:600] + "..."
		}
		return nil, &StatusError{Code: resp.StatusCode, Path: req.URL.Path, Body: msg}
	}
	return data, nil
}

// StatusError is returned when the node answers with a non-2xx status.
// Network failures and timeouts are NOT StatusErrors.
type StatusError struct {
	Code int
	Path string
	Body string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("HTTP %d from %s: %s", e.Code, e.Path, e.Body)
}

// get performs a GET against base+path (path must already be URL-escaped).
func (c *Client) get(ctx context.Context, base, path string, q url.Values, accept string) ([]byte, error) {
	u := base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := c.newRequest(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	return c.do(req)
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

// DownloadGraph fetches all triples of one dataset graph and stores the node's
// raw JSON answer in destDir.
func (c *Client) DownloadGraph(ctx context.Context, graphURI, destDir string) (string, error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	res, err := c.DatasetTriples(ctx, graphURI)
	if err != nil {
		return "", fmt.Errorf("downloading graph %s: %w", graphURI, err)
	}
	filePath := filepath.Join(destDir, SafeName(graphURI)+".json")
	if err := os.WriteFile(filePath, res.Raw, 0o644); err != nil {
		return "", err
	}
	logx.Infof("saved %d triple(s) of %s to %s", len(res.Rows), graphURI, filePath)
	return filePath, nil
}

// RegisterDataset posts an LDN "Create" activity to the Notification Service
// inbox (which requires Content-Type application/ld+json or application/json).
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
	logx.Block(logx.Debug, "LDN notification payload", string(buf))

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

// DatasetFromGraph derives a Dataset description from a graph IRI.
func DatasetFromGraph(gURI string) Dataset {
	info := ParseGraphIRI(gURI)
	sampleName := info.Sample
	if sampleName == "" {
		sampleName = "unknown-sample"
	}
	beamline := info.Beamline
	if beamline == "" {
		beamline = "general"
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
