package trace

import (
	"errors"
	"testing"
)

func TestDisabledRecordsNothing(t *testing.T) {
	Start(false) // clear anything recorded by other tests, then switch off
	Stop()
	RecordAPI(APICall{URL: "http://x"})
	RecordAction("download", nil)
	BeginLLM("p", "m", "u", []Message{{Role: "user", Content: "hi"}})
	EndLLM("reply", nil)
	s := Take()
	if len(s.API)+len(s.LLM)+len(s.Actions) != 0 {
		t.Errorf("recorded while disabled: %+v", s)
	}
}

func TestRecordsAndResets(t *testing.T) {
	Start(false)
	t.Cleanup(Stop)
	RecordAPI(APICall{Method: "GET", URL: "http://x", Status: 200, ResponseBody: "body"})
	RecordAction("download", map[string]string{"file": "f"})
	BeginLLM("ollama", "mistral", "http://llm", []Message{{Role: "system", Content: "s"}, {Role: "user", Content: "q"}})
	EndLLM("answer", errors.New("boom"))

	s := Take()
	if len(s.API) != 1 || s.API[0].ResponseBody != "body" {
		t.Errorf("api = %+v", s.API)
	}
	if len(s.LLM) != 1 || len(s.LLM[0].Request) != 2 || s.LLM[0].Response != "answer" || s.LLM[0].Error != "boom" {
		t.Errorf("llm = %+v", s.LLM)
	}
	if len(s.Actions) != 1 || s.Actions[0].Detail["file"] != "f" {
		t.Errorf("actions = %+v", s.Actions)
	}

	Start(false) // restarting clears the record
	if s := Take(); len(s.API)+len(s.LLM)+len(s.Actions) != 0 {
		t.Errorf("Start did not reset: %+v", s)
	}
}

func TestOmitBodies(t *testing.T) {
	Start(true)
	t.Cleanup(Stop)
	RecordAPI(APICall{URL: "http://x", Bytes: 4, ResponseBody: "body"})
	if s := Take(); s.API[0].ResponseBody != "" || s.API[0].Bytes != 4 {
		t.Errorf("api = %+v", s.API)
	}
}
