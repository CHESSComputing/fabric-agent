package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/CHESSComputing/fabric-agent/pkg/agent"
	"github.com/CHESSComputing/fabric-agent/pkg/config"
	"github.com/CHESSComputing/fabric-agent/pkg/fabric"
	"github.com/CHESSComputing/fabric-agent/pkg/workflow"
)

func main() {
	configPath := flag.String("config", "config.yaml", "Path to YAML configuration file")
	queryFlag := flag.String("query", "", "Single question for non-interactive execution")
	yes := flag.Bool("yes", false, "Answer 'yes' to confirmation prompts (e.g. dataset registration)")
	flag.Parse()

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		fmt.Printf("Error loading configuration '%s': %v\n", *configPath, err)
		os.Exit(1)
	}

	fabricClient := fabric.NewClient(&cfg.FabricNode)
	aiAgent, err := agent.NewAgent(&cfg.Agent, fabricClient)
	if err != nil {
		fmt.Printf("Error creating agent: %v\n", err)
		os.Exit(1)
	}
	engine := workflow.NewEngine(cfg, fabricClient)

	printHeader(cfg, aiAgent)

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	confirm := func(prompt string) bool {
		if *yes {
			return true
		}
		if *queryFlag != "" { // non-interactive and no --yes
			return false
		}
		fmt.Printf("%s [y/N] ", prompt)
		if !scanner.Scan() {
			return false
		}
		a := strings.ToLower(strings.TrimSpace(scanner.Text()))
		return a == "y" || a == "yes"
	}

	if *queryFlag != "" {
		if !runQuery(cfg, engine, aiAgent, *queryFlag, confirm) {
			os.Exit(1)
		}
		return
	}

	for {
		fmt.Print("\nfabric-agent> ")
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}
		if input == "exit" || input == "quit" {
			fmt.Println("Exiting CLI. Goodbye!")
			break
		}
		runQuery(cfg, engine, aiAgent, input, confirm)
	}
}

// runQuery handles one question and reports whether it succeeded.
func runQuery(cfg *config.Config, engine *workflow.Engine, aiAgent *agent.Agent, question string, confirm workflow.Confirm) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	ans, err := aiAgent.Ask(ctx, question)
	if err != nil {
		fmt.Printf("Agent error: %v\n", err)
		return false
	}
	printAnswer(cfg, ans)

	if err := engine.Run(ctx, ans, confirm); err != nil {
		fmt.Printf("Workflow execution error: %v\n", err)
		return false
	}
	return true
}

func printAnswer(cfg *config.Config, ans *agent.Answer) {
	p := ans.Plan
	if p.Clarification != "" && ans.Result == nil {
		fmt.Printf("\n[AI Agent] I need a bit more information: %s\n", p.Clarification)
		return
	}
	if p.Explanation != "" {
		fmt.Printf("\n[AI Agent] %s\n", p.Explanation)
	}
	fmt.Printf("[AI Agent] intent=%s, attempts=%d\n", p.Intent, ans.Attempts)
	fmt.Println("\n--- SPARQL ---")
	fmt.Println(p.SPARQL)
	fmt.Println("--------------")
	if ans.Result != nil {
		ans.Result.WriteTable(os.Stdout, cfg.Agent.MaxRows)
	}
	if ans.Summary != "" {
		fmt.Printf("\n[AI Agent] %s\n", ans.Summary)
	}
}

func printHeader(cfg *config.Config, a *agent.Agent) {
	fmt.Println("==================================================================")
	fmt.Println("       FOXDEN FabricNode AI Data Discovery & Processing Agent     ")
	fmt.Println("==================================================================")
	if a.UsesLLM() {
		fmt.Printf("Language model: %s (%s)\n", cfg.Agent.Model, cfg.Agent.Mode)
	} else {
		fmt.Println("No LLM configured: using the keyword-based fallback planner.")
		fmt.Println("Set agent.mode to openai, anthropic or ollama for real questions.")
	}
	fmt.Println("Ask in plain English, or type 'exit' to quit.")
	fmt.Println("Example: 'Which datasets on beamline ID3A contain thaumatin?'")
	fmt.Println("------------------------------------------------------------------")
}
