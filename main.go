package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/CHESSComputing/fabric-agent/pkg/agent"
	"github.com/CHESSComputing/fabric-agent/pkg/config"
	"github.com/CHESSComputing/fabric-agent/pkg/fabric"
	"github.com/CHESSComputing/fabric-agent/pkg/workflow"
)

func main() {
	configPath := flag.String("config", "config.yaml", "Path to YAML configuration file")
	queryFlag := flag.String("query", "", "Single query string for non-interactive execution")
	flag.Parse()

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		fmt.Printf("Error loading configuration '%s': %v\n", *configPath, err)
		os.Exit(1)
	}

	fabricClient := fabric.NewClient(&cfg.FabricNode)
	aiAgent := agent.NewAgent(&cfg.Agent, fabricClient)
	engine := workflow.NewEngine(cfg, fabricClient, aiAgent)

	printHeader()

	if *queryFlag != "" {
		runQuery(engine, aiAgent, *queryFlag)
		return
	}

	scanner := bufio.NewScanner(os.Stdin)
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

		runQuery(engine, aiAgent, input)
	}
}

func runQuery(engine *workflow.Engine, aiAgent *agent.Agent, question string) {
	ctx := context.Background()
	plan, err := aiAgent.ProcessQuestion(ctx, question)
	if err != nil {
		fmt.Printf("Agent error: %v\n", err)
		return
	}

	if err := engine.ExecuteDiscoveryAndProcessing(plan); err != nil {
		fmt.Printf("Workflow execution error: %v\n", err)
	}
}

func printHeader() {
	fmt.Println("==================================================================")
	fmt.Println("       FOXDEN FabricNode AI Data Discovery & Processing Agent     ")
	fmt.Println("==================================================================")
	fmt.Println("Type your dataset prompt or 'exit' to quit.")
	fmt.Println("Example: 'Find all ID3A x-ray diffraction datasets above 300K'")
	fmt.Println("------------------------------------------------------------------")
}
