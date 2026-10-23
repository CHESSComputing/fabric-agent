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
)

// Message is one chat turn. Role is "system", "user" or "assistant".
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Options are per-request settings. Temperature < 0 omits the parameter.
type Options struct {
	Temperature float64
	MaxTokens   int
	JSON        bool // ask the back-end for a JSON object where it supports it
}

// Client sends a conversation to an LLM and returns the assistant's text.
type Client interface {
	Chat(ctx context.Context, msgs []Message, opts Options) (string, error)
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
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 500 {
			msg = msg[:500] + "..."
		}
		return fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, url, msg)
	}
	return json.Unmarshal(data, out)
}

// ---- OpenAI (and OpenAI-compatible servers) ----

type openAI struct {
	hc              *http.Client
	base, key, model string
}

func (o *openAI) Chat(ctx context.Context, msgs []Message, opts Options) (string, error) {
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

func (o *ollama) Chat(ctx context.Context, msgs []Message, opts Options) (string, error) {
	body := map[string]interface{}{"model": o.model, "messages": msgs, "stream": false}
	if opts.Temperature >= 0 {
		body["options"] = map[string]interface{}{"temperature": opts.Temperature}
	}
	if opts.JSON {
		body["format"] = "json"
	}

	var out struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := postJSON(ctx, o.hc, o.base+"/api/chat", nil, body, &out); err != nil {
		return "", err
	}
	return out.Message.Content, nil
}

// ---- Anthropic ----

type anthropic struct {
	hc               *http.Client
	base, key, model string
}

func (a *anthropic) Chat(ctx context.Context, msgs []Message, opts Options) (string, error) {
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
