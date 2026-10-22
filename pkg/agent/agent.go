package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/CHESSComputing/fabric-agent/pkg/config"
	"github.com/CHESSComputing/fabric-agent/pkg/fabric"
)

type Agent struct {
	cfg          *config.AgentConfig
	fabricClient *fabric.Client
}

type AgentPlan struct {
	OriginalQuery string             `json:"original_query"`
	SearchQuery   fabric.SearchQuery `json:"search_query"`
	Action        string             `json:"action"`
}

func NewAgent(cfg *config.AgentConfig, client *fabric.Client) *Agent {
	return &Agent{
		cfg:          cfg,
		fabricClient: client,
	}
}

func (a *Agent) ProcessQuestion(ctx context.Context, question string) (*AgentPlan, error) {
	fmt.Printf("\n[AI Agent] Analyzing question: %q...\n", question)

	sq := fabric.SearchQuery{
		QueryString: question,
		Filter:      make(map[string]interface{}),
		Limit:       5,
	}

	qLower := strings.ToLower(question)

	// Map user intent to existing sample graphs in FabricNode
	if strings.Contains(qLower, "thaumatin") {
		sq.Filter["sample"] = "thaumatin-a"
	} else if strings.Contains(qLower, "lysozyme") {
		sq.Filter["sample"] = "lysozyme-1"
	} else if strings.Contains(qLower, "silicon") {
		sq.Filter["sample"] = "silicon-std"
	} else if strings.Contains(qLower, "nanoparticle") || strings.Contains(qLower, "fe3o4") {
		sq.Filter["sample"] = "fe3o4-nanoparticles"
	}

	if strings.Contains(qLower, "fast") {
		sq.Filter["beamline"] = "fast"
	} else if strings.Contains(qLower, "id3a") || strings.Contains(qLower, "crystallography") {
		sq.Filter["beamline"] = "id3a"
	}

	return &AgentPlan{
		OriginalQuery: question,
		SearchQuery:   sq,
		Action:        "DISCOVER_AND_PROCESS",
	}, nil
}
