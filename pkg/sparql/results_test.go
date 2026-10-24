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

func TestWriteKeyValue(t *testing.T) {
	long := "https://chess.cornell.edu/graph/id3a/btr=btr101/cycle=2024-3/sample_name=thaumatin-a/with/a/very/long/tail/that/must/not/be/cut"
	r := &Result{
		Structured: true,
		Vars:       []string{"graph", "beamline", "sample"},
		Rows: []map[string]string{
			{"graph": long, "beamline": "id3a", "sample": "thaumatin-a"},
			{"graph": "g2", "beamline": "", "sample": "s2"},
		},
		Notes: []string{"capped"},
	}
	var buf bytes.Buffer
	r.WriteKeyValue(&buf, 100)
	want := "graph   : " + long + "\n" +
		"beamline: id3a\n" +
		"sample  : thaumatin-a\n" +
		"---\n" +
		"graph   : g2\n" +
		"beamline:\n" + // empty value: no trailing space
		"sample  : s2\n" +
		"---\n" +
		"Total   : 2 row(s)\n" +
		"Note    : capped\n"
	if buf.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", buf.String(), want)
	}

	buf.Reset()
	r.WriteKeyValue(&buf, 1)
	if !strings.Contains(buf.String(), "Total   : 2 row(s) (1 shown; raise agent.max_rows to see more)") {
		t.Errorf("truncation not reported:\n%s", buf.String())
	}

	buf.Reset()
	(&Result{Structured: true, Vars: []string{"x"}}).WriteKeyValue(&buf, 10)
	if buf.String() != "Total: 0 row(s)\n" {
		t.Errorf("empty result printed %q", buf.String())
	}
}

func TestWriteKeyValueMultilineValue(t *testing.T) {
	r := &Result{Structured: true, Vars: []string{"o"}, Rows: []map[string]string{{"o": "line1\nline2"}}}
	var buf bytes.Buffer
	r.WriteKeyValue(&buf, 10)
	if !strings.HasPrefix(buf.String(), "o    : line1\n       line2\n---\n") {
		t.Errorf("continuation lines not aligned:\n%q", buf.String())
	}
}
