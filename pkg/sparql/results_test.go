package sparql

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseSelect(t *testing.T) {
	body := []byte(`{"head":{"vars":["graph","n"]},"results":{"bindings":[
		{"graph":{"type":"uri","value":"http://g/1"},"n":{"type":"literal","value":"3"}},
		{"graph":{"type":"uri","value":"http://g/2"}}]}}`)
	r := ParseResults(body)
	if !r.Structured || len(r.Rows) != 2 {
		t.Fatalf("bad parse: %+v", r)
	}
	if r.IsEmpty() {
		t.Error("non-empty result reported empty")
	}
	if got := r.Values(); len(got) != 3 {
		t.Errorf("Values() = %v", got)
	}
	var buf bytes.Buffer
	r.WriteTable(&buf, 10)
	if !strings.Contains(buf.String(), "http://g/2") || !strings.Contains(buf.String(), "2 row(s)") {
		t.Errorf("unexpected table:\n%s", buf.String())
	}
}

func TestParseEmptyAskAndUnstructured(t *testing.T) {
	if r := ParseResults([]byte(`{"head":{"vars":["x"]},"results":{"bindings":[]}}`)); !r.IsEmpty() {
		t.Error("expected empty result")
	}
	if r := ParseResults([]byte(`{"head":{},"boolean":true}`)); r.Boolean == nil || !*r.Boolean {
		t.Error("ASK not parsed")
	}
	r := ParseResults([]byte("@prefix a: <http://a/> ."))
	if r.Structured || r.IsEmpty() {
		t.Errorf("turtle should be unstructured and not 'empty': %+v", r)
	}
}
