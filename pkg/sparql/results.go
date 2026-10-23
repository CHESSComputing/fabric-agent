package sparql

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"
)

// Term is one RDF term in the SPARQL 1.1 JSON results format.
type Term struct {
	Type     string `json:"type"`
	Value    string `json:"value"`
	Datatype string `json:"datatype,omitempty"`
	Lang     string `json:"xml:lang,omitempty"`
}

type rawResults struct {
	Head struct {
		Vars []string `json:"vars"`
	} `json:"head"`
	Results *struct {
		Bindings []map[string]Term `json:"bindings"`
	} `json:"results"`
	Boolean *bool `json:"boolean"`
}

// Result is a parsed query response. When the endpoint does not answer in
// SPARQL JSON (e.g. CONSTRUCT returning Turtle) Structured is false and only
// Raw is populated.
type Result struct {
	Vars       []string
	Rows       []map[string]string
	Boolean    *bool
	Structured bool
	Raw        []byte
}

// ParseResults parses a SPARQL JSON results document. It never fails: anything
// it cannot interpret is returned as an unstructured Result carrying Raw.
func ParseResults(body []byte) *Result {
	res := &Result{Raw: body}

	var rr rawResults
	if err := json.Unmarshal(body, &rr); err != nil || (rr.Results == nil && rr.Boolean == nil) {
		return res
	}

	res.Structured = true
	res.Vars = rr.Head.Vars
	res.Boolean = rr.Boolean
	if rr.Results != nil {
		seen := map[string]bool{}
		for _, v := range res.Vars {
			seen[v] = true
		}
		var extra []string
		for _, b := range rr.Results.Bindings {
			row := make(map[string]string, len(b))
			for k, t := range b {
				row[k] = t.Value
				if !seen[k] {
					seen[k] = true
					extra = append(extra, k)
				}
			}
			res.Rows = append(res.Rows, row)
		}
		sort.Strings(extra)
		res.Vars = append(res.Vars, extra...)
	}
	return res
}

// IsEmpty reports whether a structured SELECT returned no rows.
func (r *Result) IsEmpty() bool {
	return r.Structured && r.Boolean == nil && len(r.Rows) == 0
}

// Values returns every distinct cell value, in first-seen order.
func (r *Result) Values() []string {
	var out []string
	seen := map[string]bool{}
	for _, row := range r.Rows {
		for _, v := range r.Vars {
			if val, ok := row[v]; ok && !seen[val] {
				seen[val] = true
				out = append(out, val)
			}
		}
	}
	return out
}

// WriteTable prints the result as an aligned text table (at most maxRows rows).
func (r *Result) WriteTable(w io.Writer, maxRows int) {
	if !r.Structured {
		fmt.Fprintf(w, "%s\n", clip(strings.TrimSpace(string(r.Raw)), 4000))
		return
	}
	if r.Boolean != nil {
		fmt.Fprintf(w, "ASK result: %v\n", *r.Boolean)
		return
	}
	if len(r.Rows) == 0 {
		fmt.Fprintln(w, "(no results)")
		return
	}

	rows := r.Rows
	truncated := 0
	if maxRows > 0 && len(rows) > maxRows {
		truncated = len(rows) - maxRows
		rows = rows[:maxRows]
	}

	const maxCol = 70
	widths := make([]int, len(r.Vars))
	for i, v := range r.Vars {
		widths[i] = utf8.RuneCountInString(v)
	}
	for _, row := range rows {
		for i, v := range r.Vars {
			if l := utf8.RuneCountInString(clip(row[v], maxCol)); l > widths[i] {
				widths[i] = l
			}
		}
	}

	line := func(cells []string) {
		var sb strings.Builder
		for i, c := range cells {
			if i > 0 {
				sb.WriteString("  ")
			}
			sb.WriteString(c)
			sb.WriteString(strings.Repeat(" ", widths[i]-utf8.RuneCountInString(c)))
		}
		fmt.Fprintln(w, strings.TrimRight(sb.String(), " "))
	}

	line(r.Vars)
	sep := make([]string, len(r.Vars))
	for i := range sep {
		sep[i] = strings.Repeat("-", widths[i])
	}
	line(sep)
	for _, row := range rows {
		cells := make([]string, len(r.Vars))
		for i, v := range r.Vars {
			cells[i] = clip(row[v], maxCol)
		}
		line(cells)
	}
	fmt.Fprintf(w, "(%d row(s)", len(r.Rows))
	if truncated > 0 {
		fmt.Fprintf(w, ", %d not shown", truncated)
	}
	fmt.Fprintln(w, ")")
}

// CompactText renders the result as tab-separated text for use in an LLM
// prompt, bounded by maxRows and maxChars.
func (r *Result) CompactText(maxRows, maxChars int) string {
	if !r.Structured {
		return clip(string(r.Raw), maxChars)
	}
	if r.Boolean != nil {
		return fmt.Sprintf("ASK result: %v", *r.Boolean)
	}
	var sb strings.Builder
	sb.WriteString(strings.Join(r.Vars, "\t"))
	sb.WriteString("\n")
	for i, row := range r.Rows {
		if maxRows > 0 && i >= maxRows {
			fmt.Fprintf(&sb, "... (%d more rows)\n", len(r.Rows)-i)
			break
		}
		cells := make([]string, len(r.Vars))
		for j, v := range r.Vars {
			cells[j] = row[v]
		}
		sb.WriteString(strings.Join(cells, "\t"))
		sb.WriteString("\n")
	}
	return clip(sb.String(), maxChars)
}

// clip shortens s to at most n runes, marking the cut with an ellipsis.
func clip(s string, n int) string {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
