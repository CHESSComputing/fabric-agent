package fabric

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/CHESSComputing/fabric-agent/pkg/logx"
	"github.com/CHESSComputing/fabric-agent/pkg/sparql"
)

// The FabricNode data-service does NOT run SPARQL. Its /sparql endpoint is a
// triple-pattern API:
//
//	GET /sparql?s=&p=&o=&g=      exact-match filters on subject/predicate/object/graph
//	GET /sparql?search=<kw>      case-insensitive substring search in literal objects
//	GET /sparql?describe=<iri>   triples where the IRI is subject or object
//	GET /graphs                  names of all graphs
//	GET /beamlines/{bl}/graphs   graphs of one beamline
//	GET /beamlines/{bl}/sparql?s=&p=&o=
//	GET /beamlines/{bl}/datasets/{did}/sparql?s=&p=&o=
//
// Answers are SPARQL-JSON-shaped with the columns s, p, o, g. The global
// endpoint truncates answers to ServerRowCap triples.
const ServerRowCap = 100

var beamlineRe = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

// CleanBeamline lower-cases and validates a beamline id (e.g. "ID3A" -> "id3a").
func CleanBeamline(s string) (string, error) {
	bl := strings.ToLower(strings.TrimSpace(s))
	if !beamlineRe.MatchString(bl) {
		return "", fmt.Errorf("invalid beamline id %q (letters and digits only, e.g. id1, id3a, fast)", s)
	}
	return bl, nil
}

// TripleFilter selects triples by exact match; empty fields are wildcards.
// A set Graph takes precedence over Beamline.
type TripleFilter struct {
	Beamline string
	S, P, O  string
	Graph    string
}

// Graphs lists dataset graph IRIs, optionally of a single beamline.
func (c *Client) Graphs(ctx context.Context, beamline string) ([]string, error) {
	p := "/graphs"
	if beamline != "" {
		bl, err := CleanBeamline(beamline)
		if err != nil {
			return nil, err
		}
		p = "/beamlines/" + bl + "/graphs"
	}
	data, err := c.get(ctx, c.cfg.DataURL, p, nil, "application/json")
	if err != nil {
		return nil, fmt.Errorf("listing graphs: %w", err)
	}
	var gr GraphsResponse
	if err := json.Unmarshal(data, &gr); err != nil {
		return nil, fmt.Errorf("decoding graphs response: %w", err)
	}
	sort.Strings(gr.Graphs)
	return gr.Graphs, nil
}

// MatchTriples runs an exact-match triple pattern query.
func (c *Client) MatchTriples(ctx context.Context, f TripleFilter) (*sparql.Result, error) {
	q := url.Values{}
	set := func(k, v string) {
		if v != "" {
			q.Set(k, v)
		}
	}
	set("s", f.S)
	set("p", f.P)
	set("o", f.O)

	p := "/sparql"
	global := true
	if f.Graph != "" {
		set("g", f.Graph)
	} else if f.Beamline != "" {
		bl, err := CleanBeamline(f.Beamline)
		if err != nil {
			return nil, err
		}
		p = "/beamlines/" + bl + "/sparql"
		global = false
	}
	return c.triples(ctx, p, q, global)
}

// Search finds triples whose literal object contains keyword (server side,
// case-insensitive). It does not look at IRIs, so a word that only occurs in a
// graph name is not found; combine it with Graphs for that.
func (c *Client) Search(ctx context.Context, keyword string) (*sparql.Result, error) {
	return c.triples(ctx, "/sparql", url.Values{"search": {keyword}}, true)
}

// Describe returns the triples in which iri is subject or object.
func (c *Client) Describe(ctx context.Context, iri string) (*sparql.Result, error) {
	return c.triples(ctx, "/sparql", url.Values{"describe": {iri}}, true)
}

// DatasetTriples returns every triple of one dataset graph. It prefers the
// dataset-scoped route (not subject to the 100-triple cap) and falls back to
// the global g= filter.
func (c *Client) DatasetTriples(ctx context.Context, graphIRI string) (*sparql.Result, error) {
	if bl, did, ok := DatasetRefFromGraph(graphIRI); ok {
		p := "/beamlines/" + bl + "/datasets/" + url.PathEscape(did) + "/sparql"
		res, err := c.triples(ctx, p, nil, false)
		if err == nil {
			return res, nil
		}
		logx.Infof("dataset-scoped query failed (%v); falling back to the global graph filter", err)
	}
	return c.MatchTriples(ctx, TripleFilter{Graph: graphIRI})
}

func (c *Client) triples(ctx context.Context, p string, q url.Values, capped bool) (*sparql.Result, error) {
	data, err := c.get(ctx, c.cfg.DataURL, p, q, "application/sparql-results+json, application/json;q=0.9")
	if err != nil {
		return nil, err
	}
	res := sparql.ParseResults(data)
	if !res.Structured {
		return nil, fmt.Errorf("unexpected answer from %s (not SPARQL JSON): %.200s", p, string(data))
	}
	if capped && len(res.Rows) >= ServerRowCap {
		res.Notes = append(res.Notes,
			fmt.Sprintf("the node caps this endpoint at %d triples, so the answer may be incomplete", ServerRowCap))
	}
	return res, nil
}

// BeamlineInfo is one entry of the node's beamline catalog.
type BeamlineInfo struct {
	ID    string
	Title string
	Type  string
}

// Beamlines reads the beamline catalog from the Catalog Service.
func (c *Client) Beamlines(ctx context.Context) ([]BeamlineInfo, error) {
	if c.cfg.CatalogURL == "" {
		return nil, fmt.Errorf("fabric_node.catalog_url is not configured")
	}
	data, err := c.get(ctx, c.cfg.CatalogURL, "/catalog/beamlines", nil, "application/ld+json, application/json;q=0.9")
	if err != nil {
		return nil, fmt.Errorf("listing beamlines: %w", err)
	}
	var raw struct {
		Datasets []struct {
			ID    string `json:"@id"`
			Title string `json:"dct:title"`
			Type  string `json:"chess:blType"`
		} `json:"dcat:dataset"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decoding beamline catalog: %w", err)
	}
	out := make([]BeamlineInfo, 0, len(raw.Datasets))
	for _, d := range raw.Datasets {
		out = append(out, BeamlineInfo{
			ID:    strings.ToLower(path.Base(strings.TrimRight(d.ID, "/"))),
			Title: d.Title,
			Type:  d.Type,
		})
	}
	return out, nil
}

// GraphInfo is what can be read off a dataset graph IRI of the form
// <base>/graph/<beamline>/<key>=<val>/.../sample_name=<name>.
type GraphInfo struct {
	Beamline string // empty if iri is not a dataset graph (e.g. <base>/graph/sensors)
	Rest     string // the key=value path below the beamline
	Sample   string
}

// ParseGraphIRI extracts beamline and sample name from a dataset graph IRI.
func ParseGraphIRI(iri string) GraphInfo {
	const marker = "/graph/"
	i := strings.LastIndex(iri, marker)
	if i < 0 {
		return GraphInfo{}
	}
	parts := strings.Split(iri[i+len(marker):], "/")
	if len(parts) < 2 || !beamlineRe.MatchString(parts[0]) || !strings.Contains(parts[1], "=") {
		return GraphInfo{}
	}
	info := GraphInfo{Beamline: parts[0], Rest: strings.Join(parts[1:], "/")}
	for _, seg := range parts[1:] {
		if k, v, ok := strings.Cut(seg, "="); ok && k == "sample_name" {
			info.Sample = v
		}
	}
	return info
}

// DatasetRefFromGraph reverses the node's DID -> graph IRI mapping:
// <base>/graph/id3a/btr=x/cycle=y/sample_name=z  ->  /beamline=id3a/btr=x/cycle=y/sample_name=z
func DatasetRefFromGraph(iri string) (beamline, did string, ok bool) {
	info := ParseGraphIRI(iri)
	if info.Beamline == "" {
		return "", "", false
	}
	return info.Beamline, "/beamline=" + info.Beamline + "/" + info.Rest, true
}
