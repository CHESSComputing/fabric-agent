package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/CHESSComputing/fabric-agent/pkg/fabric"
	"github.com/CHESSComputing/fabric-agent/pkg/sparql"
)

// Operations the planner may choose from. They map onto what the FabricNode
// data-service can really do (see pkg/fabric/api.go).
const (
	OpFind       = "find"        // graphs whose name or text values contain all keywords
	OpListGraphs = "list_graphs" // dataset graphs, optionally filtered
	OpTriples    = "triples"     // exact-match triple pattern
	OpDescribe   = "describe"    // triples around one IRI
	OpBeamlines  = "beamlines"   // beamline catalog
)

// maxCalls bounds how many calls one plan may contain.
const maxCalls = 3

// StringList accepts either a JSON array or a single string (which is split on
// whitespace and commas); small language models produce both.
type StringList []string

func (s *StringList) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*s = strings.FieldsFunc(one, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return fmt.Errorf("expected a string or a list of strings")
	}
	*s = many
	return nil
}

// Call is one request against the node.
type Call struct {
	Op       string     `json:"op"`
	Beamline string     `json:"beamline,omitempty"`
	Keywords StringList `json:"keywords,omitempty"`
	S        string     `json:"s,omitempty"`
	P        string     `json:"p,omitempty"`
	O        string     `json:"o,omitempty"`
	Graph    string     `json:"graph,omitempty"`
	IRI      string     `json:"iri,omitempty"`
}

// normalize cleans up what the model produced.
func (c *Call) normalize() {
	c.Op = strings.ToLower(strings.TrimSpace(c.Op))
	c.Beamline = strings.ToLower(strings.TrimSpace(c.Beamline))
	seen := map[string]bool{}
	var kws []string
	for _, k := range c.Keywords {
		k = strings.TrimSpace(k)
		if k == "" || seen[strings.ToLower(k)] {
			continue
		}
		seen[strings.ToLower(k)] = true
		kws = append(kws, k)
	}
	c.Keywords = kws
	c.S, c.P, c.O = strings.TrimSpace(c.S), strings.TrimSpace(c.P), strings.TrimSpace(c.O)
	c.Graph, c.IRI = strings.TrimSpace(c.Graph), strings.TrimSpace(c.IRI)
}

// Validate reports why a call cannot be executed.
func (c Call) Validate() error {
	if c.Beamline != "" {
		if _, err := fabric.CleanBeamline(c.Beamline); err != nil {
			return err
		}
	}
	switch c.Op {
	case OpFind:
		if len(c.Keywords) == 0 {
			return fmt.Errorf(`op "find" needs at least one keyword`)
		}
	case OpDescribe:
		if c.IRI == "" {
			return fmt.Errorf(`op "describe" needs "iri"`)
		}
	case OpListGraphs, OpTriples, OpBeamlines:
	default:
		return fmt.Errorf("unknown op %q (valid: find, list_graphs, triples, describe, beamlines)", c.Op)
	}
	return nil
}

// String is a compact human-readable form, e.g. find keywords=[thaumatin] beamline=id3a.
func (c Call) String() string {
	parts := []string{c.Op}
	if len(c.Keywords) > 0 {
		parts = append(parts, "keywords=["+strings.Join(c.Keywords, ",")+"]")
	}
	for _, kv := range [][2]string{{"beamline", c.Beamline}, {"s", c.S}, {"p", c.P}, {"o", c.O}, {"graph", c.Graph}, {"iri", c.IRI}} {
		if kv[1] != "" {
			parts = append(parts, kv[0]+"="+kv[1])
		}
	}
	return strings.Join(parts, " ")
}

// runCall executes one call against the node and returns a table.
func (a *Agent) runCall(ctx context.Context, c Call) (*sparql.Result, error) {
	switch c.Op {
	case OpBeamlines:
		bls, err := a.fabric.Beamlines(ctx)
		if err != nil {
			return nil, err
		}
		res := &sparql.Result{Vars: []string{"beamline", "title", "type"}, Structured: true}
		for _, b := range bls {
			res.Rows = append(res.Rows, map[string]string{"beamline": b.ID, "title": b.Title, "type": b.Type})
		}
		return res, nil

	case OpListGraphs:
		graphs, err := a.fabric.Graphs(ctx, c.Beamline)
		if err != nil {
			return nil, err
		}
		var keep []string
		for _, g := range graphs {
			if matchesAll(g, c.Keywords) {
				keep = append(keep, g)
			}
		}
		return graphTable(keep, nil), nil

	case OpFind:
		return a.find(ctx, c)

	case OpTriples:
		return a.fabric.MatchTriples(ctx, fabric.TripleFilter{Beamline: c.Beamline, S: c.S, P: c.P, O: c.O, Graph: c.Graph})

	case OpDescribe:
		return a.fabric.Describe(ctx, c.IRI)
	}
	return nil, fmt.Errorf("unknown op %q", c.Op)
}

// find returns the dataset graphs matching ALL keywords, where a keyword
// matches if it occurs in the graph name (the node's literal search cannot see
// IRIs, and sample names live in the graph name) or in a text value of the
// graph (via the node's search endpoint).
func (a *Agent) find(ctx context.Context, c Call) (*sparql.Result, error) {
	graphs, err := a.fabric.Graphs(ctx, c.Beamline)
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(graphs))
	for _, g := range graphs {
		known[g] = true
	}

	hits := map[string]map[string]string{} // graph -> keyword -> how it matched
	mark := func(g, kw, how string) {
		if hits[g] == nil {
			hits[g] = map[string]string{}
		}
		if _, ok := hits[g][kw]; !ok {
			hits[g][kw] = how
		}
	}

	var notes []string
	for _, kw := range c.Keywords {
		for _, g := range graphs {
			if containsNorm(g, kw) {
				mark(g, kw, "graph name")
			}
		}
		res, err := a.fabric.Search(ctx, kw)
		if err != nil {
			return nil, err
		}
		for _, row := range res.Rows {
			if g := row["g"]; known[g] {
				mark(g, kw, "text value")
			}
		}
		for _, n := range res.Notes {
			if !containsString(notes, n) {
				notes = append(notes, n)
			}
		}
	}

	var matched []string
	why := map[string]string{}
	for _, g := range graphs {
		h := hits[g]
		if len(h) != len(c.Keywords) {
			continue
		}
		matched = append(matched, g)
		var parts []string
		for _, kw := range c.Keywords {
			parts = append(parts, kw+": "+h[kw])
		}
		why[g] = strings.Join(parts, "; ")
	}

	res := graphTable(matched, why)
	res.Notes = notes
	return res, nil
}

// graphTable renders graph IRIs as a table with the beamline and sample name
// read off the IRI. why (optional) adds a "matched_by" column.
func graphTable(graphs []string, why map[string]string) *sparql.Result {
	res := &sparql.Result{Vars: []string{"graph", "beamline", "sample"}, Structured: true}
	if why != nil {
		res.Vars = append(res.Vars, "matched_by")
	}
	for _, g := range graphs {
		info := fabric.ParseGraphIRI(g)
		row := map[string]string{"graph": g, "beamline": info.Beamline, "sample": info.Sample}
		if why != nil {
			row["matched_by"] = why[g]
		}
		res.Rows = append(res.Rows, row)
	}
	return res
}

// norm lower-cases s and drops separators so that "thaumatin a",
// "thaumatin-a" and "Thaumatin_A" compare equal.
func norm(s string) string {
	return strings.NewReplacer("-", "", "_", "", " ", "", ".", "").Replace(strings.ToLower(s))
}

func containsNorm(s, kw string) bool {
	k := norm(kw)
	return k != "" && strings.Contains(norm(s), k)
}

func matchesAll(s string, kws []string) bool {
	for _, k := range kws {
		if !containsNorm(s, k) {
			return false
		}
	}
	return true
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
