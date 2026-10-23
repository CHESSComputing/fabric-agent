// Package workflow runs the actions that follow a successful discovery query:
// downloading the matching graphs and, optionally, deriving and registering a
// new dataset.
package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/CHESSComputing/fabric-agent/pkg/agent"
	"github.com/CHESSComputing/fabric-agent/pkg/config"
	"github.com/CHESSComputing/fabric-agent/pkg/fabric"
)

// maxGraphsPerRun bounds how many graphs one request may download/process.
const maxGraphsPerRun = 5

// previewBytes is how much of a downloaded graph is echoed to the terminal.
const previewBytes = 3000

// Confirm asks the user a yes/no question; it must return false when it cannot ask.
type Confirm func(prompt string) bool

type Engine struct {
	cfg          *config.Config
	fabricClient *fabric.Client
}

func NewEngine(cfg *config.Config, client *fabric.Client) *Engine {
	return &Engine{cfg: cfg, fabricClient: client}
}

// Run performs the follow-up actions for ans according to its intent.
func (e *Engine) Run(ctx context.Context, ans *agent.Answer, confirm Confirm) error {
	if ans.Result == nil || ans.Plan.Intent == agent.IntentQuery {
		return nil // clarification requested, or nothing further to do
	}
	if len(ans.Graphs) == 0 {
		fmt.Println("\nThe query returned no named graphs to work with.")
		return nil
	}

	graphs := ans.Graphs
	if len(graphs) > maxGraphsPerRun {
		fmt.Printf("\nQuery matched %d graphs; only the first %d will be used.\n", len(graphs), maxGraphsPerRun)
		graphs = graphs[:maxGraphsPerRun]
	}

	fmt.Printf("\n[Step 1/3] Downloading %d graph(s) to %s ...\n", len(graphs), e.cfg.Storage.DownloadDir)
	files := make(map[string]string, len(graphs))
	for _, g := range graphs {
		path, err := e.fabricClient.DownloadGraph(ctx, g, e.cfg.Storage.DownloadDir)
		if err != nil {
			return fmt.Errorf("retrieval failed: %w", err)
		}
		files[g] = path
		fmt.Printf("   %s\n     -> %s\n", g, path)
	}

	fmt.Println("\n[Step 2/3] Inspecting downloaded content...")
	for _, g := range graphs {
		preview(files[g])
	}

	if ans.Plan.Intent != agent.IntentProcess {
		return nil
	}

	fmt.Println("\n[Step 3/3] Deriving and registering new dataset(s)...")
	if confirm == nil || !confirm(fmt.Sprintf("Register %d derived dataset(s) with the Notification Service at %s?",
		len(graphs), e.cfg.FabricNode.NotificationURL)) {
		fmt.Println("   Skipped registration (not confirmed; use --yes to confirm non-interactively).")
		return nil
	}
	if err := os.MkdirAll(e.cfg.Storage.OutputDir, 0o755); err != nil {
		return err
	}
	for _, g := range graphs {
		if err := e.derive(ctx, g); err != nil {
			return err
		}
	}
	fmt.Println("\nWorkflow execution completed.")
	return nil
}

func (e *Engine) derive(ctx context.Context, graphURI string) error {
	target := fabric.DatasetFromGraph(graphURI)
	now := time.Now()
	derivedGraphURI := fmt.Sprintf("%s/derived-%d", graphURI, now.Unix())

	payload := map[string]interface{}{
		"@context":      "https://chess.cornell.edu/ns#",
		"parent_graph":  graphURI,
		"derived_graph": derivedGraphURI,
		"sample_name":   target.Name,
		"analysis":      "automated_sosa_observation_peak_fitting",
		"processed_at":  now.Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	file := filepath.Join(e.cfg.Storage.OutputDir, "derived_"+fabric.SafeName(graphURI)+".json")
	if err := os.WriteFile(file, data, 0o644); err != nil {
		return fmt.Errorf("writing derived dataset: %w", err)
	}
	fmt.Printf("   Derived dataset file: %s\n", file)

	record := fabric.Dataset{
		DID:        target.DID + "-derived",
		Name:       "Derived Peak Analysis - " + target.Name,
		Beamline:   target.Beamline,
		GraphURI:   derivedGraphURI,
		DataFormat: "RDF/JSON-LD",
		Metadata:   payload,
		CreatedAt:  now.Format(time.RFC3339),
	}
	if err := e.fabricClient.RegisterDataset(ctx, record); err != nil {
		return fmt.Errorf("registration failed: %w", err)
	}
	fmt.Printf("   Registered %s via LDN\n", derivedGraphURI)
	return nil
}

func preview(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("   cannot read %s: %v\n", path, err)
		return
	}
	text := string(data)
	more := ""
	if len(text) > previewBytes {
		text = text[:previewBytes]
		more = fmt.Sprintf("\n   ... (%d more bytes in file)", len(data)-previewBytes)
	}
	fmt.Printf("   --- %s ---\n%s%s\n", filepath.Base(path), text, more)
}
