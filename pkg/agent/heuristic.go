package agent

import (
	"regexp"
	"strings"
)

var wordRe = regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9_.\-]*`)

var stopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an and all any are as at be by can data dataset datasets find for from get give
		has have how i in is it list me my of on or show that the their them there these this those to was were what
		when where which with without please about into over under above below mention mentions mentioning contain
		contains containing measured using related download fetch save process analyse analyze derive register
		beamline beamlines available exist exists`) {
		stopwords[w] = true
	}
}

// heuristicPlan is the no-LLM fallback: it keeps the informative words of the
// question as keywords for a "find" call, recognizes a beamline id if one of
// the node's beamlines is named, and detects download/process verbs. It is
// deliberately simple; use an LLM mode for real questions.
func heuristicPlan(question string, sch *Schema) *Plan {
	lower := strings.ToLower(question)

	intent := IntentQuery
	switch {
	case containsAny(lower, "process", "analy", "derive", "register"):
		intent = IntentProcess
	case containsAny(lower, "download", "fetch", "save"):
		intent = IntentDownload
	}

	beamline := ""
	var kws []string
	seen := map[string]bool{}
	for _, w := range wordRe.FindAllString(lower, -1) {
		if seen[w] {
			continue
		}
		seen[w] = true
		if sch != nil && sch.hasBeamline(w) {
			beamline = w
			continue
		}
		if stopwords[w] || len(w) < 2 {
			continue
		}
		kws = append(kws, w)
	}

	call := Call{Op: OpFind, Keywords: kws, Beamline: beamline}
	explanation := "Keyword search (no LLM configured): datasets whose name or text values contain all the key words of your question."
	switch {
	case len(kws) == 0 && beamline == "" && strings.Contains(lower, "beamline"):
		call = Call{Op: OpBeamlines}
		explanation = "Lists the beamlines of the node."
	case len(kws) == 0:
		call = Call{Op: OpListGraphs, Beamline: beamline}
		explanation = "Lists the datasets of the node."
	}
	return &Plan{Question: question, Intent: intent, Calls: []Call{call}, Explanation: explanation}
}

func containsAny(s string, subs ...string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}
