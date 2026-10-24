// Package agent turns natural-language questions into calls against the
// FabricNode API, executes them, repairs the plan when the node rejects it,
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
	"github.com/CHESSComputing/fabric-agent/pkg/logx"
	"github.com/CHESSComputing/fabric-agent/pkg/sparql"
)

// Intent says what the user wants done with the results.
type Intent string

const (
	IntentQuery    Intent = "query"    // just show the answer
	IntentDownload Intent = "download" // fetch the matching graphs
	IntentProcess  Intent = "process"  // download, derive and register
)

// Plan is the interpretation of a question: what to call and why.
type Plan struct {
	Question      string
	Intent        Intent
	Calls         []Call
	Explanation   string
	Clarification string // non-empty (and Calls empty): the agent needs more information
}

func (p *Plan) callsJSON() string {
	b, err := json.Marshal(p.Calls)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// Step is one executed call and its result table.
type Step struct {
	Call   Call
	Result *sparql.Result
}

// Answer is the outcome of Ask.
type Answer struct {
	Plan     *Plan
	Steps    []Step // empty when Plan.Clarification is set
	Attempts int
	Summary  string   // natural-language answer, if summarization is enabled
	Graphs   []string // dataset graphs found in the results
}

type turn struct{ Question, Calls string }

type Agent struct {
	cfg    *config.AgentConfig
	fabric *fabric.Client
	llm    llm.Client // nil in heuristic mode

	mu       sync.Mutex
	schema   *Schema
	schemaAt time.Time
	recent   []turn

	preflightOK bool
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
	logx.Infof("question: %q", question)

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

	if len(ans.Plan.Calls) > 0 {
		a.remember(question, ans.Plan.callsJSON())
	}
	ans.Graphs = graphsIn(ans.Steps, sch.Graphs)
	logx.Infof("done: %d call(s), %d dataset graph(s) in the results", len(ans.Steps), len(ans.Graphs))

	if a.cfg.Summarize && a.llm != nil && len(ans.Steps) > 0 {
		summary, serr := a.summarize(ctx, question, ans)
		if serr != nil {
			fmt.Fprintf(os.Stderr, "[AI Agent] warning: summary failed: %v\n", serr)
		}
		ans.Summary = summary
	}
	return ans, nil
}

func (a *Agent) askLLM(ctx context.Context, question string, sch *Schema) (*Answer, error) {
	a.mu.Lock()
	recent := append([]turn(nil), a.recent...)
	a.mu.Unlock()

	if err := a.preflight(ctx); err != nil {
		return nil, err
	}

	system := systemPrompt(sch)
	user := userPrompt(question, recent)
	logx.Block(logx.Debug, fmt.Sprintf("LLM system prompt (%d chars)", len(system)), system)
	logx.Block(logx.Debug, "LLM user prompt", user)

	msgs := []llm.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}
	opts := llm.Options{Temperature: a.cfg.Temperature, MaxTokens: a.cfg.MaxTokens, NumCtx: a.cfg.NumCtx, JSON: true}

	maxAttempts := a.cfg.MaxRetries + 1
	var lastErr error
	var lastOK *Answer
	emptyRetried := false

	retry := func(format string, args ...interface{}) {
		msg := fmt.Sprintf(format, args...)
		logx.Infof("asking the model to repair its answer: %s", firstLine(msg))
		msgs = append(msgs, llm.Message{Role: "user", Content: msg})
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		logx.Infof("planning attempt %d/%d", attempt, maxAttempts)
		raw, err := a.chat(ctx, msgs, opts)
		if err != nil {
			return nil, fmt.Errorf("LLM request failed: %w", err)
		}
		msgs = append(msgs, llm.Message{Role: "assistant", Content: raw})

		plan, err := parsePlan(raw, question)
		if err != nil {
			lastErr = err
			logx.Infof("plan rejected: %v", err)
			retry("Your reply was not usable: %v. Reply again with ONLY the JSON object described in the rules.", err)
			continue
		}
		logx.Infof("plan: intent=%s, %d call(s): %s", plan.Intent, len(plan.Calls), plan.callsJSON())
		if len(plan.Calls) == 0 {
			return &Answer{Plan: plan, Attempts: attempt}, nil // clarification requested
		}

		steps, err := a.execute(ctx, plan)
		if err != nil {
			lastErr = err
			var se *fabric.StatusError
			if !errors.As(err, &se) {
				return nil, err // node unreachable or similar: rewriting the plan will not help
			}
			retry("The node rejected the call: %v\nFix the call and reply with the JSON object again.", err)
			continue
		}

		lastOK = &Answer{Plan: plan, Steps: steps, Attempts: attempt}
		if allEmpty(steps) && !emptyRetried && attempt < maxAttempts {
			emptyRetried = true
			retry("The calls ran but found nothing. If the keywords were too specific or too many, use fewer or more distinctive ones, or switch to list_graphs. If the data really does not exist, return the same calls again.")
			continue
		}
		return lastOK, nil
	}

	if lastOK != nil {
		return lastOK, nil
	}
	return nil, fmt.Errorf("no working plan after %d attempt(s): %w", maxAttempts, lastErr)
}

func (a *Agent) askHeuristic(ctx context.Context, question string, sch *Schema) (*Answer, error) {
	plan := heuristicPlan(question, sch)
	logx.Infof("heuristic plan: intent=%s, %d call(s): %s", plan.Intent, len(plan.Calls), plan.callsJSON())
	steps, err := a.execute(ctx, plan)
	if err != nil {
		return nil, err
	}
	return &Answer{Plan: plan, Steps: steps, Attempts: 1}, nil
}

// execute runs the calls of plan in order.
func (a *Agent) execute(ctx context.Context, plan *Plan) ([]Step, error) {
	var steps []Step
	for i, c := range plan.Calls {
		logx.Infof("executing call %d/%d: %s", i+1, len(plan.Calls), c)
		res, err := a.runCall(ctx, c)
		if err != nil {
			return steps, fmt.Errorf("call %d (%s) failed: %w", i+1, c, err)
		}
		logx.Infof("call %d returned %s", i+1, describeResult(res))
		steps = append(steps, Step{Call: c, Result: res})
	}
	return steps, nil
}

// preflight checks once that the LLM back-end is reachable, if it can.
func (a *Agent) preflight(ctx context.Context) error {
	pf, ok := a.llm.(llm.Preflighter)
	if !ok {
		return nil
	}
	a.mu.Lock()
	done := a.preflightOK
	a.mu.Unlock()
	if done {
		return nil
	}
	if err := pf.Preflight(ctx); err != nil {
		return err
	}
	a.mu.Lock()
	a.preflightOK = true
	a.mu.Unlock()
	return nil
}

// chat calls the LLM and, while waiting, reports every 10 seconds that it is
// still waiting, so a slow local model does not look like a hang.
func (a *Agent) chat(ctx context.Context, msgs []llm.Message, opts llm.Options) (string, error) {
	done := make(chan struct{})
	defer close(done)
	start := time.Now()
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				logx.Infof("still waiting for the language model... %s elapsed (large local models can take minutes on CPU)",
					time.Since(start).Round(time.Second))
			}
		}
	}()
	return a.llm.Chat(ctx, msgs, opts)
}

func (a *Agent) summarize(ctx context.Context, question string, ans *Answer) (string, error) {
	var b strings.Builder
	for i, st := range ans.Steps {
		fmt.Fprintf(&b, "Call %d: %s\n%s\n", i+1, st.Call, st.Result.CompactText(30, 3000))
	}
	msgs := []llm.Message{
		{Role: "system", Content: "You assist beamline scientists. Answer the question concisely (at most five sentences) using ONLY the results provided. If the results are empty, say that nothing was found. Never invent data."},
		{Role: "user", Content: fmt.Sprintf("Question: %s\n\nResults (tab-separated):\n%s", question, b.String())},
	}
	logx.Infof("asking the model to summarize the results")
	out, err := a.chat(ctx, msgs, llm.Options{Temperature: a.cfg.Temperature, MaxTokens: a.cfg.MaxTokens, NumCtx: a.cfg.NumCtx})
	return strings.TrimSpace(out), err
}

func (a *Agent) loadSchema(ctx context.Context) (*Schema, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	ttl := time.Duration(a.cfg.SchemaTTLSeconds) * time.Second
	if a.schema != nil && time.Since(a.schemaAt) < ttl {
		logx.Debugf("using cached schema (age %s)", time.Since(a.schemaAt).Round(time.Second))
		return a.schema, nil
	}
	s, err := discoverSchema(ctx, a.fabric)
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

func (a *Agent) remember(question, calls string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recent = append(a.recent, turn{question, calls})
	if len(a.recent) > 3 {
		a.recent = a.recent[len(a.recent)-3:]
	}
}

// graphsIn returns the cell values of all results that are known dataset graphs.
func graphsIn(steps []Step, known []string) []string {
	set := make(map[string]bool, len(known))
	for _, g := range known {
		set[g] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, st := range steps {
		for _, v := range st.Result.Values() {
			if set[v] && !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out
}

func allEmpty(steps []Step) bool {
	for _, st := range steps {
		if !st.Result.IsEmpty() {
			return false
		}
	}
	return true
}

func describeResult(r *sparql.Result) string {
	if r.IsEmpty() {
		return "no rows"
	}
	return fmt.Sprintf("%d row(s)", len(r.Rows))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

type planJSON struct {
	Intent        string `json:"intent"`
	Calls         []Call `json:"calls"`
	Call          *Call  `json:"call"` // some models emit a single call
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
	if len(p.Calls) == 0 && p.Call != nil {
		p.Calls = []Call{*p.Call}
	}
	if len(p.Calls) > maxCalls {
		logx.Infof("plan has %d calls; keeping the first %d", len(p.Calls), maxCalls)
		p.Calls = p.Calls[:maxCalls]
	}
	for i := range p.Calls {
		p.Calls[i].normalize()
		if err := p.Calls[i].Validate(); err != nil {
			return nil, fmt.Errorf("call %d is invalid: %v", i+1, err)
		}
	}
	clar := strings.TrimSpace(p.Clarification)
	if len(p.Calls) == 0 && clar == "" {
		return nil, errors.New(`"calls" is empty and "clarification" is empty`)
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
		Calls:         p.Calls,
		Explanation:   strings.TrimSpace(p.Explanation),
		Clarification: clar,
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
