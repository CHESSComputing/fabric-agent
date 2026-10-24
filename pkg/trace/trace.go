// Package trace passively records what happened while one question was
// answered: the HTTP calls made to the node, the conversations with the
// language model, and follow-up actions. It exists so that --output-format
// json can explain how a result was found. Recording is off (and costs
// nothing) unless Start has been called; it never changes program behaviour.
package trace

import (
	"sync"
	"time"
)

// Message is one chat message sent to the language model.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// LLMCall is one request/response exchange with the language model.
type LLMCall struct {
	Provider   string    `json:"provider"`
	Model      string    `json:"model"`
	URL        string    `json:"url"`
	Started    string    `json:"started"`
	DurationMS int64     `json:"duration_ms"`
	Request    []Message `json:"request_messages"`
	Response   string    `json:"response"`
	Error      string    `json:"error,omitempty"`
}

// APICall is one HTTP request to a FabricNode service.
type APICall struct {
	Method       string `json:"method"`
	URL          string `json:"url"`
	Started      string `json:"started"`
	DurationMS   int64  `json:"duration_ms"`
	Status       int    `json:"status,omitempty"`
	Bytes        int    `json:"response_bytes"`
	Error        string `json:"error,omitempty"`
	ResponseBody string `json:"response_body,omitempty"`
}

// Action is a follow-up step such as a download or a registration.
type Action struct {
	Type   string            `json:"type"`
	Detail map[string]string `json:"detail,omitempty"`
}

// Snapshot is a copy of everything recorded since Start.
type Snapshot struct {
	LLM     []LLMCall
	API     []APICall
	Actions []Action
}

var (
	mu         sync.Mutex
	on         bool
	omitBodies bool
	snap       Snapshot
	pending    *LLMCall
	pendingAt  time.Time
)

// Start begins (or restarts) recording. With omitBodies the node response
// bodies are not kept, which makes the record much smaller.
func Start(omit bool) {
	mu.Lock()
	defer mu.Unlock()
	on = true
	omitBodies = omit
	snap = Snapshot{}
	pending = nil
}

// Stop ends recording.
func Stop() {
	mu.Lock()
	on = false
	mu.Unlock()
}

func Enabled() bool {
	mu.Lock()
	defer mu.Unlock()
	return on
}

// Take returns a copy of what has been recorded.
func Take() Snapshot {
	mu.Lock()
	defer mu.Unlock()
	return Snapshot{
		LLM:     append([]LLMCall(nil), snap.LLM...),
		API:     append([]APICall(nil), snap.API...),
		Actions: append([]Action(nil), snap.Actions...),
	}
}

func RecordAPI(c APICall) {
	mu.Lock()
	defer mu.Unlock()
	if !on {
		return
	}
	if omitBodies {
		c.ResponseBody = ""
	}
	snap.API = append(snap.API, c)
}

func RecordAction(typ string, detail map[string]string) {
	mu.Lock()
	defer mu.Unlock()
	if on {
		snap.Actions = append(snap.Actions, Action{Type: typ, Detail: detail})
	}
}

// BeginLLM and EndLLM bracket one LLM exchange. LLM calls are sequential, so a
// single pending slot is enough.
func BeginLLM(provider, model, url string, msgs []Message) {
	mu.Lock()
	defer mu.Unlock()
	if !on {
		return
	}
	pendingAt = time.Now()
	pending = &LLMCall{
		Provider: provider,
		Model:    model,
		URL:      url,
		Started:  pendingAt.Format(time.RFC3339Nano),
		Request:  append([]Message(nil), msgs...),
	}
}

func EndLLM(reply string, err error) {
	mu.Lock()
	defer mu.Unlock()
	if !on || pending == nil {
		return
	}
	pending.DurationMS = time.Since(pendingAt).Milliseconds()
	pending.Response = reply
	if err != nil {
		pending.Error = err.Error()
	}
	snap.LLM = append(snap.LLM, *pending)
	pending = nil
}
