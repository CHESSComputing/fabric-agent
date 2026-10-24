package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/CHESSComputing/fabric-agent/internal/fakenode"
	"github.com/CHESSComputing/fabric-agent/pkg/config"
	"github.com/CHESSComputing/fabric-agent/pkg/fabric"
	"github.com/CHESSComputing/fabric-agent/pkg/llm"
	"github.com/CHESSComputing/fabric-agent/pkg/logx"
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

func newTestAgent(t *testing.T, n *fakenode.Node, l llm.Client) *Agent {
	t.Helper()
	logx.SetLevel(logx.Off)
	fcfg := &config.FabricNodeConfig{CatalogURL: n.URL, DataURL: n.URL, TimeoutSeconds: 5}
	acfg := &config.AgentConfig{Mode: "openai", MaxRows: 50, MaxRetries: 2, SchemaTTLSeconds: 60}
	return NewAgentWithLLM(acfg, fabric.NewClient(fcfg), l)
}

func plan(intent, calls string) string {
	return fmt.Sprintf(`{"intent":%q,"calls":%s,"explanation":"test","clarification":""}`, intent, calls)
}

func lastUserMessage(msgs []llm.Message) string { return msgs[len(msgs)-1].Content }

func TestFindMatchesGraphName(t *testing.T) {
	n := fakenode.New(t)
	l := &scriptedLLM{replies: []string{
		"```json\n" + plan("download", `[{"op":"find","keywords":["thaumatin"]}]`) + "\n```",
	}}
	a := newTestAgent(t, n, l)

	ans, err := a.Ask(context.Background(), "download thaumatin datasets")
	if err != nil {
		t.Fatal(err)
	}
	if ans.Plan.Intent != IntentDownload || ans.Attempts != 1 || len(ans.Steps) != 1 {
		t.Fatalf("plan=%+v attempts=%d steps=%d", ans.Plan, ans.Attempts, len(ans.Steps))
	}
	if len(ans.Graphs) != 1 || ans.Graphs[0] != fakenode.GraphThaumatin {
		t.Errorf("graphs = %v", ans.Graphs)
	}
	row := ans.Steps[0].Result.Rows[0]
	if row["beamline"] != "id3a" || row["sample"] != "thaumatin-a" || !strings.Contains(row["matched_by"], "graph name") {
		t.Errorf("row = %v", row)
	}
	// the prompt is built from what the node reported, not from hardcoded values
	sys := l.calls[0][0].Content
	for _, want := range []string{"id3a", "Beamline ID3A", "thaumatin-a", "http://chess/detector"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt lacks %q", want)
		}
	}
}

func TestFindMatchesTextValue(t *testing.T) {
	n := fakenode.New(t)
	l := &scriptedLLM{replies: []string{plan("query", `[{"op":"find","keywords":["pilatus"]}]`)}}
	a := newTestAgent(t, n, l)

	ans, err := a.Ask(context.Background(), "which datasets used the Pilatus detector?")
	if err != nil {
		t.Fatal(err)
	}
	rows := ans.Steps[0].Result.Rows
	if len(rows) != 1 || rows[0]["graph"] != fakenode.GraphThaumatin || !strings.Contains(rows[0]["matched_by"], "text value") {
		t.Errorf("rows = %v", rows)
	}
}

func TestFindKeywordsAreAnded(t *testing.T) {
	n := fakenode.New(t)
	l := &scriptedLLM{replies: []string{
		plan("query", `[{"op":"find","keywords":["thaumatin","eiger"]}]`), // no single dataset has both
		plan("query", `[{"op":"find","keywords":["thaumatin"]}]`),
	}}
	a := newTestAgent(t, n, l)

	ans, err := a.Ask(context.Background(), "thaumatin measured with eiger")
	if err != nil {
		t.Fatal(err)
	}
	if ans.Attempts != 2 || len(ans.Graphs) != 1 {
		t.Errorf("attempts=%d graphs=%v", ans.Attempts, ans.Graphs)
	}
	if !strings.Contains(lastUserMessage(l.calls[1]), "found nothing") {
		t.Error("the model was not told that the first plan found nothing")
	}
}

func TestListGraphsByBeamlineAndKeyword(t *testing.T) {
	n := fakenode.New(t)
	l := &scriptedLLM{replies: []string{plan("query", `[{"op":"list_graphs","beamline":"ID1","keywords":["2024-3"]}]`)}}
	a := newTestAgent(t, n, l)

	ans, err := a.Ask(context.Background(), "datasets from cycle 2024-3 on ID1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ans.Graphs) != 1 || ans.Graphs[0] != fakenode.GraphSilicon {
		t.Errorf("graphs = %v", ans.Graphs)
	}
}

func TestRepairsInvalidCall(t *testing.T) {
	n := fakenode.New(t)
	l := &scriptedLLM{replies: []string{
		plan("query", `[{"op":"frobnicate"}]`),
		plan("query", `[{"op":"beamlines"}]`),
	}}
	a := newTestAgent(t, n, l)

	ans, err := a.Ask(context.Background(), "what beamlines exist?")
	if err != nil {
		t.Fatal(err)
	}
	if ans.Attempts != 2 || len(ans.Steps[0].Result.Rows) != 2 {
		t.Errorf("attempts=%d rows=%v", ans.Attempts, ans.Steps[0].Result.Rows)
	}
	if !strings.Contains(lastUserMessage(l.calls[1]), "unknown op") {
		t.Errorf("validation error not fed back: %q", lastUserMessage(l.calls[1]))
	}
}

func TestRepairsAfterNodeRejectsCall(t *testing.T) {
	n := fakenode.New(t)
	n.RejectSearch = "bad"
	l := &scriptedLLM{replies: []string{
		plan("query", `[{"op":"find","keywords":["bad"]}]`),
		plan("query", `[{"op":"find","keywords":["eiger2"]}]`),
	}}
	a := newTestAgent(t, n, l)

	ans, err := a.Ask(context.Background(), "anything")
	if err != nil {
		t.Fatal(err)
	}
	if ans.Attempts != 2 || len(ans.Graphs) != 1 {
		t.Errorf("attempts=%d graphs=%v", ans.Attempts, ans.Graphs)
	}
	if !strings.Contains(lastUserMessage(l.calls[1]), "search term rejected") {
		t.Errorf("node error not fed back: %q", lastUserMessage(l.calls[1]))
	}
}

func TestUnreachableNodeFailsFast(t *testing.T) {
	n := fakenode.New(t)
	l := &scriptedLLM{replies: []string{plan("query", `[{"op":"list_graphs"}]`), plan("query", `[{"op":"list_graphs"}]`)}}
	a := newTestAgent(t, n, l)
	n.Close()

	if _, err := a.Ask(context.Background(), "list everything"); err == nil {
		t.Fatal("expected an error")
	}
	if len(l.calls) != 1 {
		t.Errorf("the model was called %d times; a network failure must not trigger repairs", len(l.calls))
	}
}

func TestGivesUpAfterRepeatedInvalidPlans(t *testing.T) {
	n := fakenode.New(t)
	bad := plan("query", `[{"op":"find"}]`) // find without keywords
	l := &scriptedLLM{replies: []string{bad, bad, bad}}
	a := newTestAgent(t, n, l)

	if _, err := a.Ask(context.Background(), "x"); err == nil {
		t.Fatal("expected an error after exhausting retries")
	}
	if len(l.calls) != 3 {
		t.Errorf("model called %d times, want 3", len(l.calls))
	}
}

func TestClarification(t *testing.T) {
	n := fakenode.New(t)
	l := &scriptedLLM{replies: []string{`{"intent":"query","calls":[],"explanation":"","clarification":"Which beamline?"}`}}
	a := newTestAgent(t, n, l)

	ans, err := a.Ask(context.Background(), "show data")
	if err != nil {
		t.Fatal(err)
	}
	if ans.Plan.Clarification != "Which beamline?" || len(ans.Steps) != 0 {
		t.Errorf("unexpected answer: %+v", ans)
	}
}

func TestHeuristicFallback(t *testing.T) {
	n := fakenode.New(t)
	a := newTestAgent(t, n, nil)

	ans, err := a.Ask(context.Background(), "Please download all thaumatin datasets from ID3A")
	if err != nil {
		t.Fatal(err)
	}
	c := ans.Plan.Calls[0]
	if ans.Plan.Intent != IntentDownload || c.Op != OpFind || c.Beamline != "id3a" || len(c.Keywords) != 1 || c.Keywords[0] != "thaumatin" {
		t.Errorf("plan = %+v call = %+v", ans.Plan, c)
	}
	if len(ans.Graphs) != 1 || ans.Graphs[0] != fakenode.GraphThaumatin {
		t.Errorf("graphs = %v", ans.Graphs)
	}
}

func TestParsePlanIsLenient(t *testing.T) {
	p, err := parsePlan(`Sure: {"call":{"op":"List_Graphs","beamline":"ID3A","keywords":"2024-3, btr101"}}`, "q")
	if err != nil {
		t.Fatal(err)
	}
	c := p.Calls[0]
	if c.Op != OpListGraphs || c.Beamline != "id3a" || len(c.Keywords) != 2 || c.Keywords[1] != "btr101" {
		t.Errorf("call = %+v", c)
	}
	if p.Intent != IntentQuery {
		t.Errorf("intent defaults to query, got %q", p.Intent)
	}
	if _, err := parsePlan(`{"calls":[],"clarification":""}`, "q"); err == nil {
		t.Error("empty plan must be rejected")
	}
	if _, err := parsePlan(`{"calls":[{"op":"list_graphs","beamline":"a b"}]}`, "q"); err == nil {
		t.Error("invalid beamline must be rejected")
	}
}

func TestNormMatching(t *testing.T) {
	for _, kw := range []string{"thaumatin", "Thaumatin-A", "thaumatin a", "SAMPLE_NAME=thaumatin"} {
		if !containsNorm(fakenode.GraphThaumatin, kw) {
			t.Errorf("%q should match %s", kw, fakenode.GraphThaumatin)
		}
	}
	if containsNorm(fakenode.GraphThaumatin, "lysozyme") || containsNorm(fakenode.GraphThaumatin, "") {
		t.Error("unexpected match")
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

func TestPromptOffersBeamlinesThatHaveDataEvenIfNotInCatalog(t *testing.T) {
	n := fakenode.New(t)
	n.CatalogIDs = []string{"1a3", "2a"} // none of them has datasets
	logx.SetLevel(logx.Off)
	fc := fabric.NewClient(&config.FabricNodeConfig{CatalogURL: n.URL, DataURL: n.URL, TimeoutSeconds: 5})

	sch, err := discoverSchema(context.Background(), fc)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1a3", "2a", "id1", "id3a"} {
		if !sch.hasBeamline(id) {
			t.Errorf("beamline %s missing from schema", id)
		}
	}
	prompt := systemPrompt(sch)
	if !strings.Contains(prompt, "- id1: Beamline ID1") && !strings.Contains(prompt, "- id1, 1 dataset(s)") {
		t.Errorf("prompt does not list id1 with its dataset count:\n%s", beamlineLines(sch))
	}
	if !strings.Contains(beamlineLines(sch), "1a3: Beamline 1A3 (test), 0 dataset(s)") {
		t.Errorf("empty catalog beamline should be shown with 0 datasets:\n%s", beamlineLines(sch))
	}
}
