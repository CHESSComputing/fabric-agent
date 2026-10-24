package agent

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/CHESSComputing/fabric-agent/pkg/fabric"
	"github.com/CHESSComputing/fabric-agent/pkg/logx"
)

const rdfType = "http://www.w3.org/1999/02/22-rdf-syntax-ns#type"

// Schema is what the agent knows about a FabricNode: its beamlines, which
// datasets (graphs) exist, and which predicates/classes appear in the data.
// It is read from the node at run time, so nothing is hardcoded.
type Schema struct {
	Beamlines  []fabric.BeamlineInfo
	Graphs     []string
	Samples    []string       // distinct sample names found in graph IRIs
	PerBL      map[string]int // beamline id -> number of dataset graphs
	Predicates []string
	Classes    []string
}

// discoverSchema gathers as much as it can. It always returns a non-nil
// Schema; the error (if any) lists the parts that could not be fetched.
func discoverSchema(ctx context.Context, fc *fabric.Client) (*Schema, error) {
	start := time.Now()
	s := &Schema{PerBL: map[string]int{}}
	var problems []string

	logx.Infof("schema discovery: reading graphs, beamlines and a triple sample from the node")

	graphs, err := fc.Graphs(ctx, "")
	if err != nil {
		problems = append(problems, err.Error())
	} else {
		s.Graphs = graphs
	}

	bls, err := fc.Beamlines(ctx)
	if err != nil {
		problems = append(problems, err.Error())
	} else {
		s.Beamlines = bls
	}

	// A sample of triples (the node caps it at 100) reveals the vocabulary in use.
	if sample, err := fc.MatchTriples(ctx, fabric.TripleFilter{}); err != nil {
		problems = append(problems, "triple sample: "+err.Error())
	} else {
		preds := map[string]int{}
		classes := map[string]int{}
		for _, row := range sample.Rows {
			preds[row["p"]]++
			if row["p"] == rdfType {
				classes[row["o"]]++
			}
		}
		s.Predicates = sortedKeys(preds)
		s.Classes = sortedKeys(classes)
	}

	samples := map[string]int{}
	for _, g := range s.Graphs {
		info := fabric.ParseGraphIRI(g)
		if info.Beamline != "" {
			s.PerBL[info.Beamline]++
		}
		if info.Sample != "" {
			samples[info.Sample]++
		}
	}
	s.Samples = sortedKeys(samples)

	// The catalog and the data can disagree (e.g. catalog ids 1a3, 2a, ... while
	// the graphs are named id1, id3a, fast). Offer the model every beamline that
	// has datasets, not only the catalog ones, or it will ask for empty ones.
	have := map[string]bool{}
	for _, b := range s.Beamlines {
		have[b.ID] = true
	}
	for _, id := range sortedKeys(s.PerBL) {
		if !have[id] {
			s.Beamlines = append(s.Beamlines, fabric.BeamlineInfo{ID: id})
		}
	}
	sort.Slice(s.Beamlines, func(i, j int) bool { return s.Beamlines[i].ID < s.Beamlines[j].ID })

	logx.Infof("schema discovery finished in %s: %d graph(s), %d beamline(s), %d sample name(s), %d predicate(s), %d class(es)",
		time.Since(start).Round(time.Millisecond), len(s.Graphs), len(s.Beamlines), len(s.Samples), len(s.Predicates), len(s.Classes))
	logx.Block(logx.Debug, "schema: beamlines", beamlineLines(s))
	logx.Block(logx.Debug, "schema: sample names", strings.Join(s.Samples, "\n"))
	logx.Block(logx.Debug, "schema: predicates", strings.Join(s.Predicates, "\n"))

	if len(problems) > 0 {
		return s, errors.New(strings.Join(problems, "; "))
	}
	return s, nil
}

func (s *Schema) hasBeamline(id string) bool {
	for _, b := range s.Beamlines {
		if b.ID == id {
			return true
		}
	}
	return false
}
