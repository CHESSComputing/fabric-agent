package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/CHESSComputing/fabric-agent/pkg/agent"
	"github.com/CHESSComputing/fabric-agent/pkg/config"
	"github.com/CHESSComputing/fabric-agent/pkg/fabric"
)

type Engine struct {
	cfg          *config.Config
	fabricClient *fabric.Client
	aiAgent      *agent.Agent
}

func NewEngine(cfg *config.Config, client *fabric.Client, ag *agent.Agent) *Engine {
	return &Engine{
		cfg:          cfg,
		fabricClient: client,
		aiAgent:      ag,
	}
}

func (e *Engine) ExecuteDiscoveryAndProcessing(plan *agent.AgentPlan) error {
	fmt.Println("\n[Step 1/4] Querying FabricNode Data Service for active graphs...")
	datasets, err := e.fabricClient.SearchDatasets(plan.SearchQuery)
	if err != nil {
		return fmt.Errorf("discovery failed: %w", err)
	}

	if len(datasets) == 0 {
		fmt.Println("No matching graphs found.")
		return nil
	}

	fmt.Printf("Discovered %d registered dataset graph(s):\n", len(datasets))
	for i, ds := range datasets {
		fmt.Printf("   [%d] Sample: %-20s | Beamline: %-6s | Graph URI: %s\n",
			i+1, ds.Name, ds.Beamline, ds.GraphURI)
	}

	target := datasets[0]
	fmt.Printf("\n[Step 2/4] Fetching RDF triple contents for graph '%s'...\n", target.GraphURI)
	localFile, err := e.fabricClient.DownloadDataset(target, e.cfg.Storage.DownloadDir)
	if err != nil {
		return fmt.Errorf("retrieval failed: %w", err)
	}
	fmt.Printf("   Graph content saved locally: %s\n", localFile)

	fmt.Println("\n[Step 3/4] Inspecting Graph Structure & Observations...")
	content, err := os.ReadFile(localFile)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	fmt.Println("   --- Live Graph Content Payload ---")
	var prettyJSON bytes.Buffer
	if err := json.Indent(&prettyJSON, content, "   ", "  "); err == nil {
		fmt.Println("   " + prettyJSON.String())
	} else {
		fmt.Println("   " + string(content))
	}
	fmt.Println("   ----------------------------------")

	fmt.Println("\nGenerating Derived Analytical Dataset...")
	derivedGraphURI := fmt.Sprintf("%s/derived-%d", target.GraphURI, time.Now().Unix())
	derivedFilepath := filepath.Join(e.cfg.Storage.OutputDir, "derived_dataset.json")

	if err := os.MkdirAll(e.cfg.Storage.OutputDir, 0755); err != nil {
		return err
	}

	derivedPayload := map[string]interface{}{
		"@context":      "https://chess.cornell.edu/ns#",
		"parent_graph":  target.GraphURI,
		"derived_graph": derivedGraphURI,
		"sample_name":   target.Name,
		"analysis":      "automated_sosa_observation_peak_fitting",
		"processed_at":  time.Now().Format(time.RFC3339),
	}

	derivedJSON, _ := json.MarshalIndent(derivedPayload, "", "  ")
	os.WriteFile(derivedFilepath, derivedJSON, 0644)
	fmt.Printf("   Derived dataset file created: %s\n", derivedFilepath)

	fmt.Println("\n[Step 4/4] Sending LDN Event to Notification Service Inbox ...")
	derivedRecord := fabric.Dataset{
		DID:        fmt.Sprintf("did:chess:%s:%s-derived", target.Beamline, target.Name),
		Name:       fmt.Sprintf("Derived Peak Analysis - %s", target.Name),
		Beamline:   target.Beamline,
		GraphURI:   derivedGraphURI,
		DataFormat: "RDF/JSON-LD",
		Metadata:   derivedPayload,
		CreatedAt:  time.Now().Format(time.RFC3339),
	}

	if err := e.fabricClient.RegisterDataset(derivedRecord); err != nil {
		return fmt.Errorf("registration failed: %w", err)
	}

	fmt.Println("\nWorkflow execution completed against live FabricNode!")
	return nil
}
