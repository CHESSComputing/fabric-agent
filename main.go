package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/CHESSComputing/fabric-agent/pkg/agent"
	"github.com/CHESSComputing/fabric-agent/pkg/config"
	"github.com/CHESSComputing/fabric-agent/pkg/fabric"
	"github.com/CHESSComputing/fabric-agent/pkg/logx"
	"github.com/CHESSComputing/fabric-agent/pkg/trace"
	"github.com/CHESSComputing/fabric-agent/pkg/workflow"
)

func main() {
	configPath := flag.String("config", "config.yaml", "Path to YAML configuration file")
	queryFlag := flag.String("query", "", "Single question for non-interactive execution")
	yes := flag.Bool("yes", false, "Answer 'yes' to confirmation prompts (e.g. dataset registration)")
	logLevel := flag.String("log-level", "", "Diagnostic output on stderr: off, info, debug or trace (overrides log_level in the config file)")
	outputFormat := flag.String("output-format", "", "How answers are printed: stdout (key : value), table or json (overrides output.format in the config file)")
	flag.Parse()

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		fmt.Printf("Error loading configuration '%s': %v\n", *configPath, err)
		os.Exit(1)
	}

	// precedence: --log-level flag > FABRIC_AGENT_LOG_LEVEL > config file
	levelName := cfg.LogLevel
	if *logLevel != "" {
		levelName = *logLevel
	}
	level, err := logx.Parse(levelName)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	logx.SetLevel(level)

	// precedence: --output-format flag > config file
	format := cfg.Output.Format
	if *outputFormat != "" {
		if format, err = config.NormalizeFormat(*outputFormat); err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
	}
	// In JSON mode stdout must carry nothing but JSON: the original stdout is
	// kept for the records and all progress text goes to stderr.
	jsonOut := io.Writer(os.Stdout)
	if format == "json" {
		os.Stdout = os.Stderr
	}
	logx.Infof("configuration loaded from %s: node data=%s catalog=%s, agent mode=%s model=%s",
		*configPath, cfg.FabricNode.DataURL, cfg.FabricNode.CatalogURL, cfg.Agent.Mode, cfg.Agent.Model)

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
		if !runQuery(cfg, engine, aiAgent, *queryFlag, confirm, format, jsonOut) {
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
		runQuery(cfg, engine, aiAgent, input, confirm, format, jsonOut)
	}
}

// runQuery handles one question and reports whether it succeeded.
func runQuery(cfg *config.Config, engine *workflow.Engine, aiAgent *agent.Agent, question string, confirm workflow.Confirm, format string, jsonOut io.Writer) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	asJSON := format == "json"
	started := time.Now()
	if asJSON {
		trace.Start(cfg.Output.OmitBodies)
		defer trace.Stop()
	}

	ans, err := aiAgent.Ask(ctx, question)
	if err != nil {
		fmt.Printf("Agent error: %v\n", err)
		if asJSON {
			writeJSON(jsonOut, buildRecord(cfg, question, started, nil, err, nil))
		}
		return false
	}
	if !asJSON {
		printAnswer(cfg, ans, format)
	}

	werr := engine.Run(ctx, ans, confirm)
	if werr != nil {
		fmt.Printf("Workflow execution error: %v\n", werr)
	}
	if asJSON {
		writeJSON(jsonOut, buildRecord(cfg, question, started, ans, nil, werr))
	}
	return werr == nil
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
	fmt.Printf("Diagnostics: log level %q on stderr (change with --log-level or log_level in the config)\n", logx.GetLevel())
	fmt.Println("Ask in plain English, or type 'exit' to quit.")
	fmt.Println("Example: 'Which datasets on beamline ID3A contain thaumatin?'")
	fmt.Println("------------------------------------------------------------------")
}
