package agent

import (
	"fmt"
	"strings"

	"github.com/CHESSComputing/fabric-agent/pkg/config"
)

const systemPromptHead = `You are the query planner of a data-discovery agent for a CHESS Knowledge Fabric node: an RDF triple store with named graphs that is queried with SPARQL 1.1.
Convert the user's question, written in natural language, into ONE read-only SPARQL query that answers it.

Rules:
1. Reply with ONLY a JSON object (no prose, no markdown fences) with these keys:
   "intent": "query", "download" or "process"
   "sparql": the SPARQL query as a string
   "explanation": one sentence telling the user what the query does
   "clarification": empty string normally. If the question is too ambiguous, or cannot be answered from the schema below, put ONE short question for the user here and leave "sparql" empty.
2. Intent: "query" = the user wants to see, list, count or compare information (default). "download" = the user wants to fetch/download/save the data of matching datasets. "process" = the user wants to analyse, process, derive or register new datasets from matching ones.
3. For "download" and "process" the query MUST be SELECT DISTINCT ?graph WHERE { ... } so that ?graph holds the IRIs of the matching named graphs.
4. Only SELECT, ASK, CONSTRUCT or DESCRIBE. Never updates. Never SERVICE.
5. Use only graph IRIs, classes, properties and prefixes that appear in the schema below, plus standard W3C vocabularies (rdf, rdfs, xsd, owl, sosa, ssn, prov, dcterms, void). Declare every PREFIX you use. Never invent IRIs.
6. For text from the question (sample names, beamlines, materials) prefer case-insensitive substring tests such as FILTER(CONTAINS(LCASE(STR(?x)), "thaumatin")) unless the examples show an exact value.
7. Always end with LIMIT N where N <= %d.
8. Imitate the example queries below when one is close to the question.`

func systemPrompt(cfg *config.AgentConfig, s *Schema) string {
	var b strings.Builder
	fmt.Fprintf(&b, systemPromptHead, cfg.MaxRows)
	if h := strings.TrimSpace(cfg.DialectHint); h != "" {
		b.WriteString("\n9. Endpoint notes: ")
		b.WriteString(h)
	}

	section := func(title, body string) {
		if strings.TrimSpace(body) == "" {
			return
		}
		fmt.Fprintf(&b, "\n\n## %s\n%s", title, body)
	}

	section("Named graphs", listLines(s.Graphs, 150))
	section("Classes in use", listLines(s.Classes, 60))
	section("Properties in use", listLines(s.Predicates, 80))
	section("Service description (VoID)", s.Void)
	section("SHACL shapes", s.SHACL)
	section("Example queries", s.Examples)

	if len(s.Graphs) == 0 && s.Void == "" && s.Examples == "" {
		b.WriteString("\n\n## Note\nSchema discovery failed, so you have no vocabulary information. " +
			"Prefer a clarification or a very generic exploratory query.")
	}
	return b.String()
}

func listLines(items []string, max int) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	for i, it := range items {
		if i >= max {
			fmt.Fprintf(&b, "... and %d more\n", len(items)-max)
			break
		}
		b.WriteString("- ")
		b.WriteString(it)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func userPrompt(question string, recent []turn) string {
	var b strings.Builder
	if len(recent) > 0 {
		b.WriteString("Earlier in this session (the new question may be a follow-up):\n")
		for _, t := range recent {
			fmt.Fprintf(&b, "Q: %s\nSPARQL: %s\n\n", t.Question, t.SPARQL)
		}
	}
	b.WriteString("Question: ")
	b.WriteString(question)
	return b.String()
}
