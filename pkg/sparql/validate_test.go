package sparql

import (
	"strings"
	"testing"
)

func TestValidateAccepts(t *testing.T) {
	tests := []struct {
		name, in   string
		wantSuffix string
	}{
		{"adds limit", "SELECT * WHERE { ?s ?p ?o }", "LIMIT 100"},
		{"keeps small limit", "SELECT * WHERE { ?s ?p ?o } LIMIT 5", "LIMIT 5"},
		{"caps large limit", "SELECT * WHERE { ?s ?p ?o } LIMIT 100000", "LIMIT 100"},
		{"ask untouched", "ASK { ?s ?p ?o }", "ASK { ?s ?p ?o }"},
		{"prefix prologue", "PREFIX sosa: <http://www.w3.org/ns/sosa/> SELECT ?o WHERE { ?o a sosa:Observation }", "LIMIT 100"},
		{"hash inside IRI", "SELECT ?s WHERE { ?s a <http://x.org/ns#Thing> }", "LIMIT 100"},
		{"keyword in string literal", `SELECT ?s WHERE { ?s ?p ?o FILTER(CONTAINS(LCASE(STR(?o)), "delete")) }`, "LIMIT 100"},
		{"keyword as variable", "SELECT ?delete ?load WHERE { ?s ?p ?delete . ?s ?q ?load }", "LIMIT 100"},
		{"keyword in comment", "SELECT ?s WHERE { ?s ?p ?o } # DROP ALL", "LIMIT 100"},
		{"subquery limit ignored", "SELECT ?s WHERE { { SELECT ?s WHERE { ?s ?p ?o } LIMIT 3 } }", "LIMIT 100"},
		{"less-than comparison", "SELECT ?s WHERE { ?s ?p ?v FILTER(?v < 5 && ?v > 1) }", "LIMIT 100"},
		{"trailing semicolon", "SELECT * WHERE { ?s ?p ?o };", "LIMIT 100"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Validate(tc.in, 100)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.HasSuffix(got, tc.wantSuffix) {
				t.Errorf("got %q, want suffix %q", got, tc.wantSuffix)
			}
		})
	}
}

func TestValidateRejects(t *testing.T) {
	tests := []struct{ name, in string }{
		{"empty", "   "},
		{"insert", "INSERT DATA { <a:b> <a:c> <a:d> }"},
		{"delete where", "DELETE WHERE { ?s ?p ?o }"},
		{"with delete", "WITH <g:1> DELETE { ?s ?p ?o } WHERE { ?s ?p ?o }"},
		{"drop", "DROP ALL"},
		{"prefix then drop", "PREFIX a: <http://a/> DROP GRAPH <http://a/g>"},
		{"service", "SELECT * WHERE { SERVICE <http://evil.example/sparql> { ?s ?p ?o } }"},
		{"chained update", "SELECT * WHERE { ?s ?p ?o } ; DROP ALL"},
		{"prose", "Here is your query"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := Validate(tc.in, 100); err == nil {
				t.Errorf("expected error, got %q", got)
			}
		})
	}
}

func TestIsSafeIRI(t *testing.T) {
	if !IsSafeIRI("http://localhost:8782/graphs/sample_name=silicon-std") {
		t.Error("normal IRI rejected")
	}
	for _, bad := range []string{"", "http://x/a> } DROP ALL #", "http://x/a b", "http://x/\"q"} {
		if IsSafeIRI(bad) {
			t.Errorf("unsafe IRI accepted: %q", bad)
		}
	}
}
