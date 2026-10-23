package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CHESSComputing/fabric-agent/pkg/config"
	"github.com/CHESSComputing/fabric-agent/pkg/fabric"
	"github.com/CHESSComputing/fabric-agent/pkg/llm"
)

// scriptedLLM returns canned replies in order and records the conversations.
type scriptedLLM struct {
	replies []string
	calls   [][]llm.Message
}

func (s *scriptedLLM) Chat(ctx context.Context, msgs []llm.Message, opts llm.Options) (string, error) {
	s.calls = append(s.calls, append([]llm.Message(nil), msgs...))
	if len(s.calls) > len(s.replies) {
		return "", fmt.Errorf("no more scripted replies")
	}
	return s.replies[len(s.calls)-1], nil
}

const graphA = "http://node/graphs/sample_name=thaumatin-a"

// fakeNode emulates the parts of FabricNode the agent talks to. Queries that
// contain failOn get an HTTP 400.
func fakeNode(t *testing.T, failOn string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/graphs":
			fmt.Fprintf(w, `{"graphs":[%q,"http://node/graphs/beamlines"]}`, graphA)
		case "/sparql":
			q := r.URL.Query().Get("query")
			if failOn != "" && strings.Contains(q, failOn) {
				http.Error(w, "unsupported: "+failOn, http.StatusBadRequest)
				return
			}
			if strings.Contains(q, "?v") { // schema introspection
				fmt.Fprint(w, `{"head":{"vars":["v"]},"results":{"bindings":[]}}`)
				return
			}
			fmt.Fprintf(w, `{"head":{"vars":["graph"]},"results":{"bindings":[{"graph":{"type":"uri","value":%q}}]}}`, graphA)
		case "/.well-known/void":
			fmt.Fprint(w, "@prefix void: <http://rdfs.org/ns/void#> .")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestAgent(t *testing.T, srv *httptest.Server, l llm.Client) *Agent {
	t.Helper()
	fcfg := &config.FabricNodeConfig{CatalogURL: srv.URL, DataURL: srv.URL, TimeoutSeconds: 5}
	acfg := &config.AgentConfig{Mode: "openai", MaxRows: 50, MaxRetries: 2, SchemaMaxChars: 1000, SchemaTTLSeconds: 60}
	return NewAgentWithLLM(acfg, fabric.NewClient(fcfg), l)
}

func reply(intent, q string) string {
	return fmt.Sprintf(`{"intent":%q,"sparql":%q,"explanation":"test","clarification":""}`, intent, q)
}

func TestAskHappyPath(t *testing.T) {
	srv := fakeNode(t, "")
	l := &scriptedLLM{replies: []string{
		"```json\n" + reply("download", "SELECT DISTINCT ?graph WHERE { GRAPH ?graph { ?s ?p ?o } }") + "\n```",
	}}
	a := newTestAgent(t, srv, l)

	ans, err := a.Ask(context.Background(), "download thaumatin datasets")
	if err != nil {
		t.Fatal(err)
	}
	if ans.Plan.Intent != IntentDownload || ans.Attempts != 1 {
		t.Errorf("plan=%+v attempts=%d", ans.Plan, ans.Attempts)
	}
	if !strings.Contains(ans.Plan.SPARQL, "LIMIT 50") {
		t.Errorf("LIMIT not enforced: %s", ans.Plan.SPARQL)
	}
	if len(ans.Graphs) != 1 || ans.Graphs[0] != graphA {
		t.Errorf("graphs = %v", ans.Graphs)
	}
	if !strings.Contains(l.calls[0][0].Content, graphA) {
		t.Error("system prompt does not contain discovered graph list")
	}
}

func TestAskRepairsAfterEndpointError(t *testing.T) {
	srv := fakeNode(t, "CONTAINS")
	l := &scriptedLLM{replies: []string{
		reply("query", `SELECT ?graph WHERE { GRAPH ?graph { ?s ?p ?o FILTER(CONTAINS(STR(?o), "x")) } }`),
		reply("query", "SELECT ?graph WHERE { GRAPH ?graph { ?s ?p ?o } }"),
	}}
	a := newTestAgent(t, srv, l)

	ans, err := a.Ask(context.Background(), "anything")
	if err != nil {
		t.Fatal(err)
	}
	if ans.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", ans.Attempts)
	}
	last := l.calls[1][len(l.calls[1])-1].Content
	if !strings.Contains(last, "unsupported: CONTAINS") {
		t.Errorf("endpoint error not fed back to the LLM: %q", last)
	}
}

func TestAskRejectsUpdatesAndGivesUp(t *testing.T) {
	srv := fakeNode(t, "")
	bad := reply("query", "DROP ALL")
	l := &scriptedLLM{replies: []string{bad, bad, bad}}
	a := newTestAgent(t, srv, l)

	if _, err := a.Ask(context.Background(), "wipe everything"); err == nil {
		t.Fatal("expected error for update query")
	}
}

func TestAskClarification(t *testing.T) {
	srv := fakeNode(t, "")
	l := &scriptedLLM{replies: []string{`{"intent":"query","sparql":"","explanation":"","clarification":"Which beamline?"}`}}
	a := newTestAgent(t, srv, l)

	ans, err := a.Ask(context.Background(), "show data")
	if err != nil {
		t.Fatal(err)
	}
	if ans.Plan.Clarification != "Which beamline?" || ans.Result != nil {
		t.Errorf("unexpected answer: %+v", ans)
	}
}

func TestHeuristicFallback(t *testing.T) {
	srv := fakeNode(t, "")
	a := newTestAgent(t, srv, nil)

	ans, err := a.Ask(context.Background(), "Find all ID3A datasets with thaumatin")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ans.Plan.SPARQL, `"thaumatin"`) || strings.Contains(ans.Plan.SPARQL, `"with"`) {
		t.Errorf("keywords not extracted properly: %s", ans.Plan.SPARQL)
	}
}

func TestExtractJSON(t *testing.T) {
	got := extractJSON("Sure! ```json\n{\"a\": 1}\n``` hope that helps")
	if got != `{"a": 1}` {
		t.Errorf("got %q", got)
	}
	if extractJSON("no json here") != "" {
		t.Error("expected empty string")
	}
}
