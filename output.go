package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/CHESSComputing/fabric-agent/pkg/agent"
	"github.com/CHESSComputing/fabric-agent/pkg/config"
	"github.com/CHESSComputing/fabric-agent/pkg/trace"
)

// printAnswer renders an answer on stdout in the "stdout" (key : value) or
// "table" format.
func printAnswer(cfg *config.Config, ans *agent.Answer, format string) {
	p := ans.Plan
	if len(ans.Steps) == 0 {
		if p.Clarification != "" {
			fmt.Printf("\n[AI Agent] I need a bit more information: %s\n", p.Clarification)
		}
		return
	}
	if p.Explanation != "" {
		fmt.Printf("\n[AI Agent] %s\n", p.Explanation)
	}
	fmt.Printf("[AI Agent] intent=%s, attempts=%d\n", p.Intent, ans.Attempts)
	for i, st := range ans.Steps {
		fmt.Printf("\n--- Call %d/%d: %s ---\n", i+1, len(ans.Steps), st.Call)
		if format == "table" {
			st.Result.WriteTable(os.Stdout, cfg.Agent.MaxRows)
		} else {
			st.Result.WriteKeyValue(os.Stdout, cfg.Agent.MaxRows)
		}
	}
	if ans.Summary != "" {
		fmt.Printf("\n[AI Agent] %s\n", ans.Summary)
	}
}

// ---- JSON output ----
//
// One record per question. It contains the answer (calls[].rows, every row
// with the same keys) and everything needed to understand how it was found:
// the plan, the node URLs that were called with their responses, and the
// complete conversation with the language model.

type jsonRecord struct {
	Input         string          `json:"input"`
	Timestamp     string          `json:"timestamp"`
	DurationMS    int64           `json:"duration_ms"`
	Status        string          `json:"status"` // ok, clarification or error
	Error         string          `json:"error,omitempty"`
	Environment   jsonEnv         `json:"environment"`
	Query         []string        `json:"query"` // the calls, in readable form
	Plan          *jsonPlan       `json:"plan,omitempty"`
	Attempts      int             `json:"attempts,omitempty"`
	Calls         []jsonCall      `json:"calls"`
	Graphs        []string        `json:"graphs"`
	Summary       string          `json:"summary,omitempty"`
	Actions       []trace.Action  `json:"actions"`
	WorkflowError string          `json:"workflow_error,omitempty"`
	LLMCalls      []trace.LLMCall `json:"llm_calls"`
	NodeAPICalls  []trace.APICall `json:"node_api_calls"`
}

type jsonEnv struct {
	AgentMode  string `json:"agent_mode"`
	Model      string `json:"model,omitempty"`
	DataURL    string `json:"data_url"`
	CatalogURL string `json:"catalog_url"`
}

type jsonPlan struct {
	Intent        string       `json:"intent"`
	Explanation   string       `json:"explanation,omitempty"`
	Clarification string       `json:"clarification,omitempty"`
	Calls         []agent.Call `json:"calls"`
}

type jsonCall struct {
	Call        agent.Call          `json:"call"`
	Description string              `json:"description"`
	Columns     []string            `json:"columns"`
	RowCount    int                 `json:"row_count"`
	Rows        []map[string]string `json:"rows"`
	Notes       []string            `json:"notes,omitempty"`
	Boolean     *bool               `json:"boolean,omitempty"`
	Raw         string              `json:"raw,omitempty"`
}

// buildRecord assembles the JSON record for one question. ans may be nil when
// the agent failed (askErr set).
func buildRecord(cfg *config.Config, question string, started time.Time, ans *agent.Answer, askErr, workflowErr error) jsonRecord {
	snap := trace.Take()
	rec := jsonRecord{
		Input:      question,
		Timestamp:  started.Format(time.RFC3339),
		DurationMS: time.Since(started).Milliseconds(),
		Status:     "ok",
		Environment: jsonEnv{
			AgentMode:  cfg.Agent.Mode,
			Model:      cfg.Agent.Model,
			DataURL:    cfg.FabricNode.DataURL,
			CatalogURL: cfg.FabricNode.CatalogURL,
		},
		Query:        []string{},
		Calls:        []jsonCall{},
		Graphs:       []string{},
		Actions:      snap.Actions,
		LLMCalls:     snap.LLM,
		NodeAPICalls: snap.API,
	}
	if rec.Actions == nil {
		rec.Actions = []trace.Action{}
	}
	if rec.LLMCalls == nil {
		rec.LLMCalls = []trace.LLMCall{}
	}
	if rec.NodeAPICalls == nil {
		rec.NodeAPICalls = []trace.APICall{}
	}

	if askErr != nil {
		rec.Status = "error"
		rec.Error = askErr.Error()
		return rec
	}
	if workflowErr != nil {
		rec.WorkflowError = workflowErr.Error()
	}
	if ans == nil {
		return rec
	}

	p := ans.Plan
	rec.Plan = &jsonPlan{
		Intent:        string(p.Intent),
		Explanation:   p.Explanation,
		Clarification: p.Clarification,
		Calls:         append([]agent.Call{}, p.Calls...),
	}
	rec.Attempts = ans.Attempts
	rec.Summary = ans.Summary
	if ans.Graphs != nil {
		rec.Graphs = ans.Graphs
	}
	for _, st := range ans.Steps {
		rec.Query = append(rec.Query, st.Call.String())
		rec.Calls = append(rec.Calls, jsonCallFrom(st))
	}
	if len(ans.Steps) == 0 && p.Clarification != "" {
		rec.Status = "clarification"
	}
	return rec
}

func jsonCallFrom(st agent.Step) jsonCall {
	r := st.Result
	c := jsonCall{
		Call:        st.Call,
		Description: st.Call.String(),
		Columns:     r.Vars,
		Rows:        []map[string]string{},
		Notes:       r.Notes,
		Boolean:     r.Boolean,
	}
	if c.Columns == nil {
		c.Columns = []string{}
	}
	if r.Structured {
		for _, row := range r.Rows {
			rec := make(map[string]string, len(r.Vars)) // every record has the same keys
			for _, v := range r.Vars {
				rec[v] = row[v]
			}
			c.Rows = append(c.Rows, rec)
		}
	} else {
		c.Raw = string(r.Raw)
	}
	c.RowCount = len(c.Rows)
	return c
}

// writeJSON prints rec indented, without escaping &, < and > so URLs stay readable.
func writeJSON(w io.Writer, rec jsonRecord) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rec); err != nil {
		fmt.Fprintf(os.Stderr, "error writing JSON output: %v\n", err)
	}
}
