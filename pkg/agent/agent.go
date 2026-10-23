// Package agent turns natural-language questions into SPARQL queries for a
// FabricNode, runs them, repairs them when the endpoint or validator objects,
// and optionally summarizes the answer.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/CHESSComputing/fabric-agent/pkg/config"
	"github.com/CHESSComputing/fabric-agent/pkg/fabric"
	"github.com/CHESSComputing/fabric-agent/pkg/llm"
	"github.com/CHESSComputing/fabric-agent/pkg/sparql"
)

// Intent says what the user wants done with the query results.
type Intent string

const (
	IntentQuery    Intent = "query"    // just show the answer
	IntentDownload Intent = "download" // fetch the matching graphs
	IntentProcess  Intent = "process"  // download, derive and register
)

// Plan is the LLM's (validated) interpretation of a question.
type Plan struct {
	Question      string
	Intent        Intent
	SPARQL        string
	Explanation   string
	Clarification string // non-empty: the agent needs more information
}

// Answer is the outcome of Ask.
type Answer struct {
	Plan     *Plan
	Result   *sparql.Result // nil when Plan.Clarification is set
	Attempts int
	Summary  string   // natural-language answer, if summarization is enabled
	Graphs   []string // named graphs found in Result
}

type turn struct{ Question, SPARQL string }

type Agent struct {
	cfg    *config.AgentConfig
	fabric *fabric.Client
	llm    llm.Client // nil in heuristic mode

	mu       sync.Mutex
	schema   *Schema
	schemaAt time.Time
	recent   []turn
}

// NewAgent creates an agent using the LLM back-end selected in cfg.
func NewAgent(cfg *config.AgentConfig, client *fabric.Client) (*Agent, error) {
	var l llm.Client
	if llm.Enabled(cfg.Mode) {
		var err error
		if l, err = llm.New(cfg); err != nil {
			return nil, err
		}
	}
	return NewAgentWithLLM(cfg, client, l), nil
}

// NewAgentWithLLM is like NewAgent but takes the LLM client explicitly
// (nil selects the heuristic planner). Useful for tests.
func NewAgentWithLLM(cfg *config.AgentConfig, client *fabric.Client, l llm.Client) *Agent {
	return &Agent{cfg: cfg, fabric: client, llm: l}
}

// UsesLLM reports whether questions are interpreted by a language model.
func (a *Agent) UsesLLM() bool { return a.llm != nil }

// Ask interprets question, queries the node and returns the answer.
func (a *Agent) Ask(ctx context.Context, question string) (*Answer, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return nil, errors.New("empty question")
	}
	fmt.Printf("\n[AI Agent] Analyzing question: %q...\n", question)

	sch, err := a.loadSchema(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[AI Agent] warning: schema discovery incomplete: %v\n", err)
	}

	var ans *Answer
	if a.llm == nil {
		ans, err = a.askHeuristic(ctx, question, sch)
	} else {
		ans, err = a.askLLM(ctx, question, sch)
	}
	if err != nil {
		return nil, err
	}

	if ans.Plan.Clarification == "" {
		a.remember(question, ans.Plan.SPARQL)
	}
	if ans.Result != nil {
		ans.Graphs = graphsIn(ans.Result, sch.Graphs)
		if a.cfg.Summarize && a.llm != nil && ans.Result.Structured {
			summary, serr := a.summarize(ctx, question, ans)
			if serr != nil {
				fmt.Fprintf(os.Stderr, "[AI Agent] warning: summary failed: %v\n", serr)
			}
			ans.Summary = summary
		}
	}
	return ans, nil
}

func (a *Agent) askLLM(ctx context.Context, question string, sch *Schema) (*Answer, error) {
	a.mu.Lock()
	recent := append([]turn(nil), a.recent...)
	a.mu.Unlock()

	msgs := []llm.Message{
		{Role: "system", Content: systemPrompt(a.cfg, sch)},
		{Role: "user", Content: userPrompt(question, recent)},
	}
	opts := llm.Options{Temperature: a.cfg.Temperature, MaxTokens: a.cfg.MaxTokens, JSON: true}

	maxAttempts := a.cfg.MaxRetries + 1
	var lastErr error
	var lastOK *Answer
	emptyRetried := false

	feedback := func(format string, args ...interface{}) {
		msgs = append(msgs, llm.Message{Role: "user", Content: fmt.Sprintf(format, args...)})
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		raw, err := a.llm.Chat(ctx, msgs, opts)
		if err != nil {
			return nil, fmt.Errorf("LLM request failed: %w", err)
		}
		msgs = append(msgs, llm.Message{Role: "assistant", Content: raw})

		plan, err := parsePlan(raw, question)
		if err != nil {
			lastErr = err
			feedback("Your reply was not usable: %v. Reply again with ONLY the JSON object described in the rules.", err)
			continue
		}
		if plan.Clarification != "" && strings.TrimSpace(plan.SPARQL) == "" {
			return &Answer{Plan: plan, Attempts: attempt}, nil
		}

		q, err := sparql.Validate(plan.SPARQL, a.cfg.MaxRows)
		if err != nil {
			lastErr = err
			feedback("The query was rejected by the validator: %v. Fix it and reply with the JSON object again.", err)
			continue
		}
		plan.SPARQL = q

		res, err := a.fabric.Query(ctx, q)
		if err != nil {
			lastErr = err
			feedback("The SPARQL endpoint returned an error:\n%v\nFix the query (it may use a feature the endpoint does not support) and reply with the JSON object again.", err)
			continue
		}

		lastOK = &Answer{Plan: plan, Result: res, Attempts: attempt}
		if res.IsEmpty() && !emptyRetried && attempt < maxAttempts {
			emptyRetried = true
			feedback("The query ran but returned no results. If the filters were too strict (exact string match, wrong IRI, wrong case) relax them and reply with a new JSON object. If the data really does not exist, return the same query again.")
			continue
		}
		return lastOK, nil
	}

	if lastOK != nil {
		return lastOK, nil
	}
	return nil, fmt.Errorf("no working query after %d attempt(s): %w", maxAttempts, lastErr)
}

func (a *Agent) askHeuristic(ctx context.Context, question string, sch *Schema) (*Answer, error) {
	plan := heuristicPlan(question, a.cfg.MaxRows)
	q, err := sparql.Validate(plan.SPARQL, a.cfg.MaxRows)
	if err != nil {
		return nil, err
	}
	plan.SPARQL = q
	res, err := a.fabric.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	return &Answer{Plan: plan, Result: res, Attempts: 1}, nil
}

func (a *Agent) summarize(ctx context.Context, question string, ans *Answer) (string, error) {
	msgs := []llm.Message{
		{Role: "system", Content: "You assist beamline scientists. Answer the question concisely (at most five sentences) using ONLY the query results provided. If the results are empty, say that nothing was found. Never invent data."},
		{Role: "user", Content: fmt.Sprintf("Question: %s\n\nSPARQL used:\n%s\n\nResults (tab-separated):\n%s",
			question, ans.Plan.SPARQL, ans.Result.CompactText(40, 6000))},
	}
	out, err := a.llm.Chat(ctx, msgs, llm.Options{Temperature: a.cfg.Temperature, MaxTokens: a.cfg.MaxTokens})
	return strings.TrimSpace(out), err
}

func (a *Agent) loadSchema(ctx context.Context) (*Schema, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	ttl := time.Duration(a.cfg.SchemaTTLSeconds) * time.Second
	if a.schema != nil && time.Since(a.schemaAt) < ttl {
		return a.schema, nil
	}
	s, err := discoverSchema(ctx, a.fabric, a.cfg.SchemaMaxChars)
	if err != nil {
		if a.schema != nil { // keep the older, better schema and retry next time
			return a.schema, err
		}
		a.schema = s
		a.schemaAt = time.Time{}
		return s, err
	}
	a.schema, a.schemaAt = s, time.Now()
	return s, nil
}

func (a *Agent) remember(question, q string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recent = append(a.recent, turn{question, q})
	if len(a.recent) > 3 {
		a.recent = a.recent[len(a.recent)-3:]
	}
}

// graphsIn returns the cell values of res that are known named graphs; if the
// graph list is unavailable it falls back to the ?graph column.
func graphsIn(res *sparql.Result, known []string) []string {
	var out []string
	if len(known) > 0 {
		set := make(map[string]bool, len(known))
		for _, g := range known {
			set[g] = true
		}
		for _, v := range res.Values() {
			if set[v] {
				out = append(out, v)
			}
		}
		return out
	}
	seen := map[string]bool{}
	for _, row := range res.Rows {
		if g, ok := row["graph"]; ok && !seen[g] {
			seen[g] = true
			out = append(out, g)
		}
	}
	return out
}

type planJSON struct {
	Intent        string `json:"intent"`
	SPARQL        string `json:"sparql"`
	Explanation   string `json:"explanation"`
	Clarification string `json:"clarification"`
}

func parsePlan(raw, question string) (*Plan, error) {
	js := extractJSON(raw)
	if js == "" {
		return nil, errors.New("no JSON object found")
	}
	var p planJSON
	if err := json.Unmarshal([]byte(js), &p); err != nil {
		return nil, fmt.Errorf("invalid JSON: %v", err)
	}
	if strings.TrimSpace(p.SPARQL) == "" && strings.TrimSpace(p.Clarification) == "" {
		return nil, errors.New(`both "sparql" and "clarification" are empty`)
	}

	intent := IntentQuery
	switch Intent(strings.ToLower(strings.TrimSpace(p.Intent))) {
	case IntentDownload:
		intent = IntentDownload
	case IntentProcess:
		intent = IntentProcess
	}
	return &Plan{
		Question:      question,
		Intent:        intent,
		SPARQL:        strings.TrimSpace(p.SPARQL),
		Explanation:   strings.TrimSpace(p.Explanation),
		Clarification: strings.TrimSpace(p.Clarification),
	}, nil
}

// extractJSON returns the outermost {...} in s, tolerating code fences and
// chatty preambles.
func extractJSON(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end < start {
		return ""
	}
	return s[start : end+1]
}
