// Package fakenode is an in-process imitation of the FabricNode services used
// by the tests. It reproduces the behaviour that matters to the agent: /sparql
// is a triple-pattern API (not SPARQL), "search" only looks at literals, and
// sample names appear only inside graph IRIs.
package fakenode

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

const (
	GraphThaumatin = "http://node/graph/id3a/btr=btr101/cycle=2024-3/sample_name=thaumatin-a"
	GraphSilicon   = "http://node/graph/id1/btr=btr102/cycle=2024-3/sample_name=silicon-std"
	GraphMeta      = "http://node/graph/beamlines"
	RDFType        = "http://www.w3.org/1999/02/22-rdf-syntax-ns#type"
)

type triple struct {
	s, p, o, g string
	literal    bool
}

var data = []triple{
	{"http://node/obs/1", RDFType, "http://www.w3.org/ns/sosa/Observation", GraphThaumatin, false},
	{"http://node/obs/1", "http://chess/detector", "Pilatus 6M", GraphThaumatin, true},
	{"http://node/obs/2", RDFType, "http://www.w3.org/ns/sosa/Observation", GraphSilicon, false},
	{"http://node/obs/2", "http://chess/detector", "Eiger2", GraphSilicon, true},
	{"http://node/id3a", "http://www.w3.org/2000/01/rdf-schema#label", "Beamline ID3A", GraphMeta, true},
}

var graphs = []string{GraphThaumatin, GraphSilicon, GraphMeta}

// Node is a running fake node; use Node.URL for data_url and catalog_url.
type Node struct {
	*httptest.Server

	// FailDatasetRoute makes the dataset-scoped route answer HTTP 500.
	FailDatasetRoute bool
	// CatalogIDs, if set, replaces the beamline ids in the catalog (the real
	// node's catalog and data can disagree, e.g. catalog "1a3" vs graphs "id1").
	CatalogIDs []string
	// RejectSearch makes /sparql?search=<this> answer HTTP 400.
	RejectSearch string

	mu   sync.Mutex
	reqs []string
}

// New starts a fake node that is closed when the test ends.
func New(t testing.TB) *Node {
	t.Helper()
	n := &Node{}
	n.Server = httptest.NewServer(http.HandlerFunc(n.handle))
	t.Cleanup(n.Server.Close)
	return n
}

// Requests returns the request URIs seen so far (path and query).
func (n *Node) Requests() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.reqs...)
}

func (n *Node) handle(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	n.reqs = append(n.reqs, r.URL.RequestURI())
	n.mu.Unlock()

	path := r.URL.Path
	switch {
	case path == "/graphs":
		writeJSON(w, map[string]interface{}{"graphs": graphs})

	case path == "/catalog/beamlines":
		entries := []map[string]string{}
		ids := n.CatalogIDs
		if ids == nil {
			ids = []string{"id1", "id3a"}
		}
		for _, id := range ids {
			entries = append(entries, map[string]string{
				"@id": "http://node/catalog/beamlines/" + id, "dct:title": "Beamline " + strings.ToUpper(id), "chess:blType": "test",
			})
		}
		writeJSON(w, map[string]interface{}{"dcat:dataset": entries})

	case strings.HasPrefix(path, "/beamlines/") && strings.HasSuffix(path, "/graphs"):
		bl := strings.Split(path, "/")[2]
		var out []string
		for _, g := range graphs {
			if strings.Contains(g, "/graph/"+bl+"/") {
				out = append(out, g)
			}
		}
		writeJSON(w, map[string]interface{}{"beamline": bl, "graphs": out})

	case strings.HasPrefix(path, "/beamlines/") && strings.Contains(path, "/datasets/") && strings.HasSuffix(path, "/sparql"):
		if n.FailDatasetRoute {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		bl := strings.Split(path, "/")[2]
		raw := r.URL.EscapedPath()
		if !strings.Contains(raw, "%2Fbeamline=") {
			http.Error(w, "dataset id must be URL-encoded", http.StatusBadRequest)
			return
		}
		start := strings.Index(raw, "/datasets/") + len("/datasets/")
		did, err := url.PathUnescape(raw[start : len(raw)-len("/sparql")])
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		g := "http://node/graph/" + bl + "/" + strings.TrimPrefix(did, "/beamline="+bl+"/")
		writeRows(w, filter(url.Values{"g": {g}}))

	case strings.HasPrefix(path, "/beamlines/") && strings.HasSuffix(path, "/sparql"):
		bl := strings.Split(path, "/")[2]
		var rows []triple
		for _, t := range filter(r.URL.Query()) {
			if strings.Contains(t.g, "/graph/"+bl+"/") {
				rows = append(rows, t)
			}
		}
		writeRows(w, rows)

	case path == "/sparql":
		if v := r.URL.Query().Get("search"); v != "" && v == n.RejectSearch {
			http.Error(w, "search term rejected", http.StatusBadRequest)
			return
		}
		writeRows(w, filter(r.URL.Query()))

	default:
		http.NotFound(w, r)
	}
}

// filter applies the data-service's parameters: exact s/p/o/g, literal
// substring search, and describe.
func filter(q url.Values) []triple {
	var out []triple
	for _, t := range data {
		if v := q.Get("g"); v != "" && t.g != v {
			continue
		}
		if v := q.Get("s"); v != "" && t.s != v {
			continue
		}
		if v := q.Get("p"); v != "" && t.p != v {
			continue
		}
		if v := q.Get("o"); v != "" && t.o != v {
			continue
		}
		if v := q.Get("search"); v != "" && !(t.literal && strings.Contains(strings.ToLower(t.o), strings.ToLower(v))) {
			continue
		}
		if v := q.Get("describe"); v != "" && t.s != v && t.o != v {
			continue
		}
		out = append(out, t)
	}
	return out
}

func writeRows(w http.ResponseWriter, rows []triple) {
	bindings := []map[string]map[string]string{}
	for _, t := range rows {
		kind := "uri"
		if t.literal {
			kind = "literal"
		}
		bindings = append(bindings, map[string]map[string]string{
			"s": {"type": "uri", "value": t.s},
			"p": {"type": "uri", "value": t.p},
			"o": {"type": kind, "value": t.o},
			"g": {"type": "uri", "value": t.g},
		})
	}
	writeJSON(w, map[string]interface{}{
		"head":    map[string]interface{}{"vars": []string{"s", "p", "o", "g"}},
		"results": map[string]interface{}{"bindings": bindings},
	})
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
