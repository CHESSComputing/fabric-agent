package agent

import (
	"fmt"
	"strings"
)

const systemPromptHead = `You translate a researcher's question into calls to the REST API of a CHESS Knowledge Fabric node. The node stores X-ray beamline experiments as RDF named graphs: one graph per dataset, with the graph name encoding beamline, cycle, BTR and sample name.

Reply with ONLY a JSON object (no prose, no markdown):
{"intent": "query" or "download" or "process",
 "calls": [ 1 to 3 calls ],
 "explanation": "one sentence for the user",
 "clarification": ""}

Available calls ("op"):
1. {"op":"find","keywords":["word"],"beamline":"id"}
   Datasets whose graph name OR any text value contains ALL keywords. "beamline" is optional. Use this for "which datasets mention / contain / are about ...".
2. {"op":"list_graphs","beamline":"id","keywords":["word"]}
   List dataset graphs, optionally of one beamline and/or whose graph name contains the keywords (sample name, cycle such as 2024-3, BTR number).
3. {"op":"triples","beamline":"id","s":"<full IRI>","p":"<full IRI>","o":"<exact value>","graph":"<graph IRI>"}
   Raw triples. Every field is optional but matched EXACTLY: no wildcards, no partial words. Use full IRIs taken from the lists below.
4. {"op":"describe","iri":"<full IRI>"}
   All triples in which the IRI is subject or object.
5. {"op":"beamlines"}
   List the beamlines of this node.

Rules:
- intent "query" (default): show, list, count or find. "download": fetch/download/save matching datasets. "process": analyse, process, derive or register new datasets from matching ones.
- For "download" and "process" use find or list_graphs so that dataset graphs are returned.
- Use few, distinctive keywords (sample, material, technique, detector, cycle). Never use generic words such as dataset, data, find, show, all, mention.
- Keywords are ANDed. For alternatives ("X or Y") use separate calls.
- "beamline" must be one of the ids listed below, in lower case.
- If the question is too vague or has nothing to do with this node, return "calls": [] and ask ONE short question in "clarification".`

const examplesTmpl = `
## Examples
Q: Which datasets mention {{SAMPLE}}?
{"intent":"query","calls":[{"op":"find","keywords":["{{SAMPLE}}"]}],"explanation":"Finds datasets whose name or text values contain {{SAMPLE}}.","clarification":""}
Q: List all datasets measured on beamline {{BL}}
{"intent":"query","calls":[{"op":"list_graphs","beamline":"{{BL}}"}],"explanation":"Lists every dataset of beamline {{BL}}.","clarification":""}
Q: Download the {{SAMPLE}} data from beamline {{BL}}
{"intent":"download","calls":[{"op":"find","keywords":["{{SAMPLE}}"],"beamline":"{{BL}}"}],"explanation":"Finds the {{SAMPLE}} datasets of {{BL}} and downloads them.","clarification":""}
Q: What beamlines are available?
{"intent":"query","calls":[{"op":"beamlines"}],"explanation":"Lists the beamlines.","clarification":""}`

func systemPrompt(s *Schema) string {
	var b strings.Builder
	b.WriteString(systemPromptHead)

	section := func(title, body string) {
		if strings.TrimSpace(body) != "" {
			fmt.Fprintf(&b, "\n\n## %s\n%s", title, body)
		}
	}
	section("Beamlines of this node", beamlineLines(s))
	section("Sample names seen in dataset graphs", listLines(s.Samples, 60))
	section("Example dataset graphs", listLines(s.Graphs, 8))
	section(`Predicates in use (for "triples")`, listLines(s.Predicates, 60))
	section("Classes in use", listLines(s.Classes, 30))

	sample, bl := "sample-name", "id1"
	if len(s.Samples) > 0 {
		sample = s.Samples[0]
	}
	if len(s.Beamlines) > 0 {
		bl = s.Beamlines[0].ID
	}
	b.WriteString("\n")
	b.WriteString(strings.NewReplacer("{{SAMPLE}}", sample, "{{BL}}", bl).Replace(examplesTmpl))

	if len(s.Graphs) == 0 {
		b.WriteString("\n\nNote: the node reported no dataset graphs (or discovery failed); prefer a clarification.")
	}
	return b.String()
}

func beamlineLines(s *Schema) string {
	var lines []string
	for _, bl := range s.Beamlines {
		line := "- " + bl.ID
		if bl.Title != "" {
			line += ": " + bl.Title
		}
		if bl.Type != "" {
			line += " (" + bl.Type + ")"
		}
		if len(s.Graphs) > 0 {
			line += fmt.Sprintf(", %d dataset(s)", s.PerBL[bl.ID])
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
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
			fmt.Fprintf(&b, "Q: %s\ncalls: %s\n\n", t.Question, t.Calls)
		}
	}
	b.WriteString("Question: ")
	b.WriteString(question)
	return b.String()
}
