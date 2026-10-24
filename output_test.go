package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CHESSComputing/fabric-agent/internal/fakenode"
	"github.com/CHESSComputing/fabric-agent/pkg/agent"
	"github.com/CHESSComputing/fabric-agent/pkg/config"
	"github.com/CHESSComputing/fabric-agent/pkg/fabric"
	"github.com/CHESSComputing/fabric-agent/pkg/logx"
	"github.com/CHESSComputing/fabric-agent/pkg/sparql"
	"github.com/CHESSComputing/fabric-agent/pkg/workflow"
)

func TestBuildRecordStructure(t *testing.T) {
	cfg := &config.Config{Agent: config.AgentConfig{Mode: "ollama", Model: "mistral"}}
	url := "https://x.org/graph/id3a/btr=1&cycle=2024-3/sample_name=thaumatin-a"
	ans := &agent.Answer{
		Plan: &agent.Plan{Intent: agent.IntentQuery, Explanation: "e", Calls: []agent.Call{{Op: agent.OpFind, Keywords: agent.StringList{"thaumatin"}}}},
		Steps: []agent.Step{{
			Call: agent.Call{Op: agent.OpFind, Keywords: agent.StringList{"thaumatin"}},
			Result: &sparql.Result{Structured: true, Vars: []string{"graph", "beamline"},
				Rows: []map[string]string{{"graph": url, "beamline": "id3a"}, {"graph": "g2"}}},
		}},
		Attempts: 1,
	}

	var buf bytes.Buffer
	writeJSON(&buf, buildRecord(cfg, "q?", time.Now(), ans, nil, nil))
	if !strings.Contains(buf.String(), "btr=1&cycle") {
		t.Errorf("URLs must not be HTML-escaped:\n%s", buf.String())
	}

	var got map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"input", "timestamp", "status", "environment", "query", "plan", "calls", "graphs", "actions", "llm_calls", "node_api_calls"} {
		if _, ok := got[k]; !ok {
			t.Errorf("record lacks %q", k)
		}
	}
	rows := got["calls"].([]interface{})[0].(map[string]interface{})["rows"].([]interface{})
	if len(rows) != 2 {
		t.Fatalf("rows = %v", rows)
	}
	for i, r := range rows { // every record has the same keys, even if a value is empty
		m := r.(map[string]interface{})
		if _, ok := m["graph"]; !ok {
			t.Errorf("row %d lacks graph", i)
		}
		if _, ok := m["beamline"]; !ok {
			t.Errorf("row %d lacks beamline", i)
		}
	}
	if got["llm_calls"] == nil || got["node_api_calls"] == nil || got["actions"] == nil {
		t.Error("empty lists must be [] and not null")
	}
}

func TestBuildRecordError(t *testing.T) {
	var buf bytes.Buffer
	writeJSON(&buf, buildRecord(&config.Config{}, "q", time.Now(), nil, fmt.Errorf("LLM request failed"), nil))
	var got map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["status"] != "error" || got["error"] != "LLM request failed" {
		t.Errorf("record = %v", got)
	}
}

// fakeOllama answers like an Ollama server whose model replies with planJSON.
func fakeOllama(t *testing.T, planJSON string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			fmt.Fprint(w, `{"models":[{"name":"mistral:latest"}]}`)
		case "/api/ps":
			fmt.Fprint(w, `{"models":[]}`)
		case "/api/chat":
			reply, _ := json.Marshal(map[string]interface{}{
				"message":           map[string]string{"role": "assistant", "content": planJSON},
				"done_reason":       "stop",
				"prompt_eval_count": 10,
				"eval_count":        5,
			})
			w.Write(reply)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestJSONEndToEnd runs the real agent against a fake node and a fake Ollama and
// checks that the JSON record explains how the result was found.
func TestJSONEndToEnd(t *testing.T) {
	logx.SetLevel(logx.Off)
	node := fakenode.New(t)
	ollama := fakeOllama(t, `{"intent":"query","calls":[{"op":"find","keywords":["thaumatin"]}],"explanation":"Finds thaumatin.","clarification":""}`)

	cfg := &config.Config{
		FabricNode: config.FabricNodeConfig{CatalogURL: node.URL, DataURL: node.URL, TimeoutSeconds: 5},
		Agent: config.AgentConfig{Mode: "ollama", BaseURL: ollama.URL, Model: "mistral", MaxRetries: 2,
			MaxRows: 50, TimeoutSeconds: 5, SchemaTTLSeconds: 60, NumCtx: 4096, MaxTokens: 256},
		Output:  config.OutputConfig{Format: "json"},
		Storage: config.StorageConfig{DownloadDir: t.TempDir(), OutputDir: t.TempDir()},
	}
	client := fabric.NewClient(&cfg.FabricNode)
	ag, err := agent.NewAgent(&cfg.Agent, client)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	ok := runQuery(cfg, workflow.NewEngine(cfg, client), ag, "Which datasets mention thaumatin?",
		func(string) bool { return false }, "json", &out)
	if !ok {
		t.Fatal("runQuery reported failure")
	}

	var rec struct {
		Input   string   `json:"input"`
		Status  string   `json:"status"`
		Query   []string `json:"query"`
		Graphs  []string `json:"graphs"`
		Calls   []struct {
			RowCount int                 `json:"row_count"`
			Rows     []map[string]string `json:"rows"`
		} `json:"calls"`
		LLMCalls []struct {
			Provider string `json:"provider"`
			Request  []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"request_messages"`
			Response string `json:"response"`
		} `json:"llm_calls"`
		NodeAPICalls []struct {
			Method       string `json:"method"`
			URL          string `json:"url"`
			Status       int    `json:"status"`
			ResponseBody string `json:"response_body"`
		} `json:"node_api_calls"`
	}
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil {
		t.Fatalf("output is not a single JSON document: %v\n%s", err, out.String())
	}

	if rec.Input != "Which datasets mention thaumatin?" || rec.Status != "ok" {
		t.Errorf("input/status = %q/%q", rec.Input, rec.Status)
	}
	if len(rec.Query) != 1 || rec.Query[0] != "find keywords=[thaumatin]" {
		t.Errorf("query = %v", rec.Query)
	}
	if len(rec.Calls) != 1 || rec.Calls[0].RowCount != 1 || rec.Calls[0].Rows[0]["graph"] != fakenode.GraphThaumatin {
		t.Errorf("calls = %+v", rec.Calls)
	}
	if len(rec.Graphs) != 1 || rec.Graphs[0] != fakenode.GraphThaumatin {
		t.Errorf("graphs = %v", rec.Graphs)
	}

	if len(rec.LLMCalls) != 1 || rec.LLMCalls[0].Provider != "ollama" ||
		len(rec.LLMCalls[0].Request) != 2 || rec.LLMCalls[0].Request[1].Content != "Question: Which datasets mention thaumatin?" ||
		!strings.Contains(rec.LLMCalls[0].Response, `"op":"find"`) {
		t.Errorf("llm_calls = %+v", rec.LLMCalls)
	}

	urls := ""
	bodies := 0
	for _, c := range rec.NodeAPICalls {
		urls += c.Method + " " + c.URL + "\n"
		if c.ResponseBody != "" {
			bodies++
		}
		if c.Status != 200 {
			t.Errorf("unexpected status %d for %s", c.Status, c.URL)
		}
	}
	for _, want := range []string{"/graphs", "/catalog/beamlines", "/sparql?search=thaumatin"} {
		if !strings.Contains(urls, want) {
			t.Errorf("node_api_calls lacks %q:\n%s", want, urls)
		}
	}
	if bodies == 0 {
		t.Error("response bodies were not recorded")
	}
	// the Ollama preflight GETs are not node calls and must not appear
	if strings.Contains(urls, "/api/tags") {
		t.Errorf("LLM server calls leaked into node_api_calls:\n%s", urls)
	}
}
