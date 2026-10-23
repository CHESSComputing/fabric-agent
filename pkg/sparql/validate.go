// Package sparql contains helpers for treating LLM-generated SPARQL as
// untrusted input: validation, LIMIT enforcement and result parsing.
package sparql

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	prologueRe  = regexp.MustCompile(`(?i)^(?:\s*(?:PREFIX\s+[\w.\-]*:|BASE))*\s*(\w+)`)
	forbiddenRe = regexp.MustCompile(`(?i)(?:^|[^\w?$:\-])(INSERT|DELETE|LOAD|CLEAR|CREATE|DROP|COPY|MOVE|ADD|WITH|USING|SERVICE)(?:$|[^\w:\-])`)
	limitRe     = regexp.MustCompile(`(?i)\bLIMIT\s+(\d+)`)
	unsafeIRIRe = regexp.MustCompile("[\\s<>\"{}|\\\\^`]")
)

// Validate checks that q is a single read-only SPARQL query
// (SELECT, ASK, CONSTRUCT or DESCRIBE), rejects update operations and
// federated SERVICE calls, and makes sure SELECT/CONSTRUCT/DESCRIBE queries
// carry a top-level LIMIT of at most maxLimit. It returns the normalized query.
func Validate(q string, maxLimit int) (string, error) {
	q = strings.TrimSpace(q)
	q = strings.TrimRight(q, "; \t\r\n")
	if q == "" {
		return "", errors.New("empty query")
	}

	masked := mask(q)

	m := prologueRe.FindStringSubmatch(masked)
	if m == nil {
		return "", errors.New("could not find a query form (SELECT, ASK, CONSTRUCT or DESCRIBE)")
	}
	form := strings.ToUpper(m[1])
	switch form {
	case "SELECT", "ASK", "CONSTRUCT", "DESCRIBE":
	default:
		return "", fmt.Errorf("only read-only queries are allowed, got %q", form)
	}

	if f := forbiddenRe.FindStringSubmatch(masked); f != nil {
		return "", fmt.Errorf("keyword %s is not allowed", strings.ToUpper(f[1]))
	}

	if form == "ASK" || maxLimit <= 0 {
		return q, nil
	}

	start, end, n, ok := topLevelLimit(masked)
	switch {
	case !ok:
		q = q + "\nLIMIT " + strconv.Itoa(maxLimit)
	case n > maxLimit:
		q = q[:start] + "LIMIT " + strconv.Itoa(maxLimit) + q[end:]
	}
	return q, nil
}

// IsSafeIRI reports whether s can be embedded between < and > in a query.
func IsSafeIRI(s string) bool {
	return s != "" && !unsafeIRIRe.MatchString(s)
}

// topLevelLimit finds a LIMIT clause outside of any { } group.
func topLevelLimit(masked string) (start, end, n int, ok bool) {
	for _, loc := range limitRe.FindAllStringSubmatchIndex(masked, -1) {
		depth := 0
		for _, c := range masked[:loc[0]] {
			switch c {
			case '{':
				depth++
			case '}':
				depth--
			}
		}
		if depth != 0 {
			continue
		}
		v, err := strconv.Atoi(masked[loc[2]:loc[3]])
		if err != nil {
			continue
		}
		return loc[0], loc[1], v, true
	}
	return 0, 0, 0, false
}

// mask returns a copy of q, with the same byte length, in which comments,
// string literals and <IRIs> are replaced by spaces. Keyword checks run on the
// masked copy so that e.g. "#DROP" or FILTER(CONTAINS(?x, "delete")) do not
// trigger false positives, and a '#' inside an IRI is not taken as a comment.
func mask(q string) string {
	b := []byte(q)
	out := make([]byte, len(b))
	copy(out, b)

	blank := func(from, to int) {
		if to > len(out) {
			to = len(out)
		}
		for k := from; k < to; k++ {
			if out[k] != '\n' {
				out[k] = ' '
			}
		}
	}

	i := 0
	for i < len(b) {
		c := b[i]
		switch {
		case c == '#':
			j := i
			for j < len(b) && b[j] != '\n' {
				j++
			}
			blank(i, j)
			i = j

		case c == '"' || c == '\'':
			var j int
			if i+2 < len(b) && b[i+1] == c && b[i+2] == c { // long string """..."""
				delim := strings.Repeat(string(c), 3)
				end := strings.Index(q[i+3:], delim)
				if end < 0 {
					j = len(b)
				} else {
					j = i + 3 + end + 3
				}
			} else {
				j = i + 1
				for j < len(b) && b[j] != c && b[j] != '\n' {
					if b[j] == '\\' {
						j++
					}
					j++
				}
				j++ // closing quote
			}
			blank(i, j)
			i = j

		case c == '<':
			// An IRI has no whitespace; "?a < 5" and "?a <= 5" are comparisons.
			j := i + 1
			for j < len(b) && b[j] != '>' && b[j] != ' ' && b[j] != '\t' &&
				b[j] != '\n' && b[j] != '\r' && b[j] != '<' && b[j] != '"' {
				j++
			}
			if j < len(b) && b[j] == '>' && j > i+1 {
				blank(i, j+1)
				i = j + 1
			} else {
				i++
			}

		default:
			i++
		}
	}
	return string(out)
}
