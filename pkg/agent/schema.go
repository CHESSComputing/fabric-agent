package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/CHESSComputing/fabric-agent/pkg/fabric"
)

// Schema is what the agent knows about a FabricNode. It follows the node's
// "progressive disclosure" design: service description (VoID), constraints
// (SHACL) and worked query examples are read at run time from the Catalog
// Service, so nothing about beamlines, samples or vocabularies is hardcoded.
type Schema struct {
	Graphs     []string
	Void       string
	SHACL      string
	Examples   string
	Classes    []string
	Predicates []string
}

// discoverSchema gathers as much as it can. It always returns a non-nil
// Schema; the error (if any) lists the parts that could not be fetched.
func discoverSchema(ctx context.Context, fc *fabric.Client, maxChars int) (*Schema, error) {
	s := &Schema{}
	var problems []string

	graphs, err := fc.ListGraphs(ctx)
	if err != nil {
		problems = append(problems, err.Error())
	} else {
		s.Graphs = graphs
	}

	docs := []struct {
		name string
		dst  *string
	}{
		{"void", &s.Void},
		{"shacl", &s.SHACL},
		{"sparql-examples", &s.Examples},
	}
	for _, d := range docs {
		txt, err := fc.WellKnown(ctx, d.name)
		if err != nil {
			problems = append(problems, fmt.Sprintf(".well-known/%s: %v", d.name, err))
			continue
		}
		*d.dst = clipText(txt, maxChars)
	}

	// Live vocabulary: what is actually used in the data. Best effort, because
	// not every endpoint supports GRAPH ?g.
	s.Classes = introspect(ctx, fc, "SELECT DISTINCT ?v WHERE { GRAPH ?g { ?s a ?v } } LIMIT 60")
	s.Predicates = introspect(ctx, fc, "SELECT DISTINCT ?v WHERE { GRAPH ?g { ?s ?v ?o } } LIMIT 80")

	if len(problems) > 0 {
		return s, errors.New(strings.Join(problems, "; "))
	}
	return s, nil
}

func introspect(ctx context.Context, fc *fabric.Client, q string) []string {
	res, err := fc.Query(ctx, q)
	if err != nil || !res.Structured {
		return nil
	}
	return res.Values()
}

func clipText(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "\n... [truncated]"
}
