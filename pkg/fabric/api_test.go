package fabric

import (
	"context"
	"strings"
	"testing"

	"github.com/CHESSComputing/fabric-agent/internal/fakenode"
	"github.com/CHESSComputing/fabric-agent/pkg/config"
	"github.com/CHESSComputing/fabric-agent/pkg/logx"
)

func newClient(t *testing.T) (*Client, *fakenode.Node) {
	t.Helper()
	logx.SetLevel(logx.Off)
	n := fakenode.New(t)
	return NewClient(&config.FabricNodeConfig{CatalogURL: n.URL, DataURL: n.URL, TimeoutSeconds: 5}), n
}

func TestGraphs(t *testing.T) {
	c, _ := newClient(t)
	ctx := context.Background()

	all, err := c.Graphs(ctx, "")
	if err != nil || len(all) != 3 {
		t.Fatalf("Graphs() = %v, %v", all, err)
	}
	one, err := c.Graphs(ctx, "ID3A") // upper case is normalized
	if err != nil || len(one) != 1 || one[0] != fakenode.GraphThaumatin {
		t.Fatalf("Graphs(ID3A) = %v, %v", one, err)
	}
	if _, err := c.Graphs(ctx, "id3a/../x"); err == nil {
		t.Error("expected error for an invalid beamline id")
	}
}

func TestSearchOnlySeesLiterals(t *testing.T) {
	c, _ := newClient(t)
	ctx := context.Background()

	res, err := c.Search(ctx, "pilatus")
	if err != nil || len(res.Rows) != 1 || res.Rows[0]["g"] != fakenode.GraphThaumatin {
		t.Fatalf("Search(pilatus) = %+v, %v", res, err)
	}
	// The sample name only exists inside the graph IRI, which the node's search cannot see.
	res, err = c.Search(ctx, "thaumatin")
	if err != nil || len(res.Rows) != 0 {
		t.Fatalf("Search(thaumatin) should find nothing, got %+v, %v", res, err)
	}
}

func TestMatchTriples(t *testing.T) {
	c, _ := newClient(t)
	res, err := c.MatchTriples(context.Background(), TripleFilter{S: "http://node/obs/1"})
	if err != nil || len(res.Rows) != 2 {
		t.Fatalf("MatchTriples = %+v, %v", res, err)
	}
	res, err = c.MatchTriples(context.Background(), TripleFilter{Beamline: "id1"})
	if err != nil || len(res.Rows) != 2 {
		t.Fatalf("beamline-scoped MatchTriples = %+v, %v", res, err)
	}
}

func TestDatasetTriplesUsesEncodedDatasetRoute(t *testing.T) {
	c, n := newClient(t)
	res, err := c.DatasetTriples(context.Background(), fakenode.GraphThaumatin)
	if err != nil || len(res.Rows) != 2 {
		t.Fatalf("DatasetTriples = %+v, %v", res, err)
	}
	reqs := strings.Join(n.Requests(), "\n")
	if !strings.Contains(reqs, "/beamlines/id3a/datasets/%2Fbeamline=id3a%2Fbtr=btr101%2Fcycle=2024-3%2Fsample_name=thaumatin-a/sparql") {
		t.Errorf("dataset route not used with an encoded DID; requests:\n%s", reqs)
	}
}

func TestDatasetTriplesFallsBack(t *testing.T) {
	c, n := newClient(t)
	n.FailDatasetRoute = true
	res, err := c.DatasetTriples(context.Background(), fakenode.GraphThaumatin)
	if err != nil || len(res.Rows) != 2 {
		t.Fatalf("fallback failed: %+v, %v", res, err)
	}
	reqs := n.Requests()
	if last := reqs[len(reqs)-1]; !strings.HasPrefix(last, "/sparql?g=") {
		t.Errorf("expected fallback to the global g= filter, last request %q", last)
	}
}

func TestStatusError(t *testing.T) {
	c, n := newClient(t)
	n.RejectSearch = "bad"
	_, err := c.Search(context.Background(), "bad")
	se, ok := err.(*StatusError)
	if !ok || se.Code != 400 {
		t.Fatalf("expected *StatusError with code 400, got %T %v", err, err)
	}
}

func TestBeamlines(t *testing.T) {
	c, _ := newClient(t)
	bls, err := c.Beamlines(context.Background())
	if err != nil || len(bls) != 2 || bls[1].ID != "id3a" || bls[1].Type != "test" {
		t.Fatalf("Beamlines = %+v, %v", bls, err)
	}
}

func TestParseGraphIRI(t *testing.T) {
	info := ParseGraphIRI(fakenode.GraphThaumatin)
	if info.Beamline != "id3a" || info.Sample != "thaumatin-a" {
		t.Errorf("ParseGraphIRI = %+v", info)
	}
	bl, did, ok := DatasetRefFromGraph(fakenode.GraphThaumatin)
	if !ok || bl != "id3a" || did != "/beamline=id3a/btr=btr101/cycle=2024-3/sample_name=thaumatin-a" {
		t.Errorf("DatasetRefFromGraph = %q %q %v", bl, did, ok)
	}
	if _, _, ok := DatasetRefFromGraph(fakenode.GraphMeta); ok {
		t.Error("the beamlines meta graph is not a dataset graph")
	}
}
