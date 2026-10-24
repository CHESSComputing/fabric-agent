// Package llm is a minimal, dependency-free chat client for the LLM back-ends
// supported by fabric-agent.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/CHESSComputing/fabric-agent/pkg/config"
	"github.com/CHESSComputing/fabric-agent/pkg/logx"
	"github.com/CHESSComputing/fabric-agent/pkg/trace"
)

// Message is one chat turn. Role is "system", "user" or "assistant".
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Options are per-request settings. Temperature < 0 omits the parameter.
type Options struct {
	Temperature float64
	MaxTokens   int  // cap on generated tokens (num_predict for Ollama)
	NumCtx      int  // context window; used by Ollama only
	JSON        bool // ask the back-end for a JSON object where it supports it
}

// Client sends a conversation to an LLM and returns the assistant's text.
type Client interface {
	Chat(ctx context.Context, msgs []Message, opts Options) (string, error)
}

// Preflighter is implemented by back-ends that can check, before the first
// request, that the server is reachable and the model is available.
type Preflighter interface {
	Preflight(ctx context.Context) error
}

// Enabled reports whether mode selects an LLM back-end (as opposed to the
// keyword-based heuristic planner).
func Enabled(mode string) bool {
	switch mode {
	case "openai", "anthropic", "ollama":
		return true
	}
	return false
}

// New builds the client selected by cfg.Mode.
func New(cfg *config.AgentConfig) (Client, error) {
	hc := &http.Client{Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second}
	switch cfg.Mode {
	case "openai":
		return &openAI{hc: hc, base: orDefault(cfg.BaseURL, "https://api.openai.com/v1"), key: cfg.APIKey, model: cfg.Model}, nil
	case "anthropic":
		return &anthropic{hc: hc, base: orDefault(cfg.BaseURL, "https://api.anthropic.com"), key: cfg.APIKey, model: cfg.Model}, nil
	case "ollama":
		return &ollama{hc: hc, base: orDefault(cfg.BaseURL, "http://localhost:11434"), model: cfg.Model}, nil
	}
	return nil, fmt.Errorf("agent.mode %q does not use an LLM", cfg.Mode)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return strings.TrimRight(v, "/")
}

// logRequest and logResponse bracket every LLM call with diagnostics.
func logRequest(provider, model, url string, msgs []Message) time.Time {
	chars := 0
	for _, m := range msgs {
		chars += len(m.Content)
	}
	logx.Infof("LLM request: provider=%s model=%s url=%s messages=%d prompt_chars=%d (~%d tokens)",
		provider, model, url, len(msgs), chars, chars/4)
	if trace.Enabled() {
		tm := make([]trace.Message, len(msgs))
		for i, m := range msgs {
			tm[i] = trace.Message{Role: m.Role, Content: m.Content}
		}
		trace.BeginLLM(provider, model, url, tm)
	}
	return time.Now()
}

func logResponse(start time.Time, reply string, err error) {
	trace.EndLLM(reply, err)
	took := time.Since(start).Round(time.Millisecond)
	if err != nil {
		logx.Infof("LLM request failed after %s: %v", took, err)
		return
	}
	logx.Infof("LLM reply received in %s (%d chars)", took, len(reply))
	logx.Block(logx.Debug, "LLM raw reply", reply)
}

func postJSON(ctx context.Context, hc *http.Client, url string, headers map[string]string, body, out interface{}) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	logx.Block(logx.Trace, fmt.Sprintf("LLM HTTP request body (%d bytes)", len(buf)), string(buf))

	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	logx.Debugf("LLM HTTP response: status=%d bytes=%d", resp.StatusCode, len(data))
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 500 {
			msg = msg[:500] + "..."
		}
		return fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, url, msg)
	}
	logx.Block(logx.Trace, "LLM HTTP response body", string(data))
	return json.Unmarshal(data, out)
}

// getJSON performs a short GET and decodes a JSON answer.
func getJSON(ctx context.Context, hc *http.Client, url string, out interface{}) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	logx.Infof("GET %s -> HTTP %d, %d bytes", url, resp.StatusCode, len(data))
	logx.Block(logx.Debug, "response body", string(data))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	return json.Unmarshal(data, out)
}

// ---- OpenAI (and OpenAI-compatible servers) ----

type openAI struct {
	hc               *http.Client
	base, key, model string
}

func (o *openAI) Chat(ctx context.Context, msgs []Message, opts Options) (string, error) {
	start := logRequest("openai", o.model, o.base+"/chat/completions", msgs)
	reply, err := o.chat(ctx, msgs, opts)
	logResponse(start, reply, err)
	return reply, err
}

func (o *openAI) chat(ctx context.Context, msgs []Message, opts Options) (string, error) {
	body := map[string]interface{}{"model": o.model, "messages": msgs}
	if opts.Temperature >= 0 {
		body["temperature"] = opts.Temperature
	}
	if opts.JSON {
		body["response_format"] = map[string]string{"type": "json_object"}
	}
	headers := map[string]string{}
	if o.key != "" {
		headers["Authorization"] = "Bearer " + o.key
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := postJSON(ctx, o.hc, o.base+"/chat/completions", headers, body, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("openai: response contained no choices")
	}
	return out.Choices[0].Message.Content, nil
}

// ---- Ollama ----

type ollama struct {
	hc          *http.Client
	base, model string
}

// Preflight checks that Ollama answers, that the configured model is
// installed, and reports which models are currently loaded in memory.
func (o *ollama) Preflight(ctx context.Context) error {
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := getJSON(ctx, o.hc, o.base+"/api/tags", &tags); err != nil {
		return fmt.Errorf("cannot reach Ollama at %s (is `ollama serve` running?): %w", o.base, err)
	}
	var names []string
	found := false
	for _, m := range tags.Models {
		names = append(names, m.Name)
		if m.Name == o.model || strings.TrimSuffix(m.Name, ":latest") == o.model {
			found = true
		}
	}
	logx.Infof("ollama: reachable at %s; installed models: %s", o.base, strings.Join(names, ", "))
	if !found {
		return fmt.Errorf("model %q is not installed in Ollama; run: ollama pull %s", o.model, o.model)
	}

	var ps struct {
		Models []struct {
			Name     string `json:"name"`
			Size     int64  `json:"size"`
			SizeVRAM int64  `json:"size_vram"`
		} `json:"models"`
	}
	if err := getJSON(ctx, o.hc, o.base+"/api/ps", &ps); err != nil {
		logx.Infof("ollama: could not read loaded models (older Ollama?): %v", err)
		return nil
	}
	if len(ps.Models) == 0 {
		logx.Infof("ollama: no model is loaded yet; the first request must load %q into memory, which can take a minute or more", o.model)
	}
	for _, m := range ps.Models {
		where := "CPU only"
		if m.SizeVRAM > 0 {
			where = fmt.Sprintf("%d MB in GPU memory", m.SizeVRAM>>20)
		}
		logx.Infof("ollama: loaded model %s (%d MB total, %s)", m.Name, m.Size>>20, where)
	}
	return nil
}

func (o *ollama) Chat(ctx context.Context, msgs []Message, opts Options) (string, error) {
	start := logRequest("ollama", o.model, o.base+"/api/chat", msgs)
	reply, err := o.chat(ctx, msgs, opts)
	logResponse(start, reply, err)
	return reply, err
}

func (o *ollama) chat(ctx context.Context, msgs []Message, opts Options) (string, error) {
	body := map[string]interface{}{"model": o.model, "messages": msgs, "stream": false}
	options := map[string]interface{}{}
	if opts.Temperature >= 0 {
		options["temperature"] = opts.Temperature
	}
	if opts.NumCtx > 0 {
		options["num_ctx"] = opts.NumCtx
	}
	if opts.MaxTokens > 0 {
		options["num_predict"] = opts.MaxTokens // bounds runaway output in JSON mode
	}
	if len(options) > 0 {
		body["options"] = options
	}
	if opts.JSON {
		body["format"] = "json"
	}

	var out struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		DoneReason      string `json:"done_reason"`
		PromptEvalCount int    `json:"prompt_eval_count"`
		EvalCount       int    `json:"eval_count"`
	}
	if err := postJSON(ctx, o.hc, o.base+"/api/chat", nil, body, &out); err != nil {
		return "", err
	}
	logx.Infof("ollama: prompt_tokens=%d completion_tokens=%d done_reason=%q num_ctx=%d",
		out.PromptEvalCount, out.EvalCount, out.DoneReason, opts.NumCtx)
	if out.DoneReason == "length" {
		logx.Infof("ollama: reply was cut off at num_predict=%d tokens; increase agent.max_tokens", opts.MaxTokens)
	}
	if opts.NumCtx > 0 && out.PromptEvalCount >= opts.NumCtx {
		logx.Infof("ollama: prompt filled the whole context window; increase agent.num_ctx")
	}
	return out.Message.Content, nil
}

// ---- Anthropic ----

type anthropic struct {
	hc               *http.Client
	base, key, model string
}

func (a *anthropic) Chat(ctx context.Context, msgs []Message, opts Options) (string, error) {
	start := logRequest("anthropic", a.model, a.base+"/v1/messages", msgs)
	reply, err := a.chat(ctx, msgs, opts)
	logResponse(start, reply, err)
	return reply, err
}

func (a *anthropic) chat(ctx context.Context, msgs []Message, opts Options) (string, error) {
	var system []string
	var conv []Message
	for _, m := range msgs {
		if m.Role == "system" {
			system = append(system, m.Content)
		} else {
			conv = append(conv, m)
		}
	}
	maxTokens := opts.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 2048
	}
	body := map[string]interface{}{"model": a.model, "max_tokens": maxTokens, "messages": conv}
	if len(system) > 0 {
		body["system"] = strings.Join(system, "\n\n")
	}
	if opts.Temperature >= 0 {
		body["temperature"] = opts.Temperature
	}
	headers := map[string]string{"x-api-key": a.key, "anthropic-version": "2023-06-01"}

	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := postJSON(ctx, a.hc, a.base+"/v1/messages", headers, body, &out); err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String(), nil
}
