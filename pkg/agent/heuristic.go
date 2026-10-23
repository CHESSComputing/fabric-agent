package agent

import (
	"fmt"
	"regexp"
	"strings"
)

var wordRe = regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9_.\-]*`)

var stopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an and all any are as at be by can data dataset datasets find for from get give
		has have how i in is it list me my of on or show that the their them there these this those to was were what
		when where which with without please about into over under above below`) {
		stopwords[w] = true
	}
}

// heuristicPlan is the no-LLM fallback: it keeps the informative words of the
// question and returns graphs whose IRI or any literal/IRI object mentions any
// of them. It is deliberately simple; use an LLM mode for real questions.
func heuristicPlan(question string, maxRows int) *Plan {
	lower := strings.ToLower(question)

	intent := IntentQuery
	switch {
	case containsAny(lower, "process", "analy", "derive", "register"):
		intent = IntentProcess
	case containsAny(lower, "download", "fetch", "save"):
		intent = IntentDownload
	}

	var conds []string
	seen := map[string]bool{}
	for _, w := range wordRe.FindAllString(lower, -1) {
		if stopwords[w] || len(w) < 2 || seen[w] {
			continue
		}
		seen[w] = true
		conds = append(conds, fmt.Sprintf(`CONTAINS(LCASE(STR(?graph)), "%s") || CONTAINS(LCASE(STR(?o)), "%s")`, w, w))
	}

	filter := ""
	if len(conds) > 0 {
		filter = " FILTER(" + strings.Join(conds, " || ") + ")"
	}
	q := fmt.Sprintf("SELECT DISTINCT ?graph WHERE { GRAPH ?graph { ?s ?p ?o }%s } LIMIT %d", filter, maxRows)

	return &Plan{
		Question:    question,
		Intent:      intent,
		SPARQL:      q,
		Explanation: "Keyword search (no LLM configured): graphs mentioning any of the words in your question.",
	}
}

func containsAny(s string, subs ...string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}
