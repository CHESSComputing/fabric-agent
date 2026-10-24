package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/CHESSComputing/fabric-agent/pkg/logx"
)

type Config struct {
	// LogLevel controls diagnostic output on stderr: off, info, debug, trace.
	// Overridden by env FABRIC_AGENT_LOG_LEVEL and the --log-level flag.
	LogLevel   string           `yaml:"log_level"`
	FabricNode FabricNodeConfig `yaml:"fabric_node"`
	Agent      AgentConfig      `yaml:"agent"`
	Storage    StorageConfig    `yaml:"storage"`
}

type FabricNodeConfig struct {
	CatalogURL      string `yaml:"catalog_url"`
	DataURL         string `yaml:"data_url"`
	IdentityURL     string `yaml:"identity_url"`
	NotificationURL string `yaml:"notification_url"`
	AuthToken       string `yaml:"auth_token"` // or env FABRIC_AUTH_TOKEN
	TimeoutSeconds  int    `yaml:"timeout_seconds"`
}

// AgentConfig controls how natural-language questions are turned into calls
// against the FabricNode API.
//
// Mode:
//
//	openai     - OpenAI or any OpenAI-compatible server (set base_url)
//	anthropic  - Anthropic Messages API
//	ollama     - local Ollama server
//	heuristic  - no LLM, keyword based best-effort planner
//	             ("mock" and "live" are accepted as aliases)
type AgentConfig struct {
	Mode    string `yaml:"mode"`
	APIKey  string `yaml:"api_key"`  // or env OPENAI_API_KEY / ANTHROPIC_API_KEY
	BaseURL string `yaml:"base_url"` // optional endpoint override
	Model   string `yaml:"model"`

	// Temperature < 0 means "do not send the parameter" (needed by some
	// reasoning models that only accept their default).
	Temperature    float64 `yaml:"temperature"`
	MaxTokens      int     `yaml:"max_tokens"`
	TimeoutSeconds int     `yaml:"timeout_seconds"`
	// NumCtx is the context window requested from Ollama. Ollama's own
	// default is small and it silently cuts off long prompts.
	NumCtx int `yaml:"num_ctx"`

	// MaxRetries is the number of times the agent may ask the LLM to repair a
	// plan that was invalid, failed, or found nothing. Unset/0 means 2; use a
	// negative value to disable repairs.
	MaxRetries int `yaml:"max_retries"`
	// MaxRows caps the number of rows printed per result table.
	MaxRows int `yaml:"max_rows"`

	SchemaTTLSeconds int `yaml:"schema_ttl_seconds"` // schema cache lifetime

	// Summarize asks the LLM to answer the question in prose from the results.
	Summarize bool `yaml:"summarize"`
}

type StorageConfig struct {
	DownloadDir string `yaml:"download_dir"`
	OutputDir   string `yaml:"output_dir"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if env := os.Getenv("FABRIC_AGENT_LOG_LEVEL"); env != "" {
		c.LogLevel = env
	}
	c.LogLevel = strings.ToLower(strings.TrimSpace(c.LogLevel))
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}

	fn := &c.FabricNode
	if fn.TimeoutSeconds <= 0 {
		fn.TimeoutSeconds = 30
	}
	if fn.AuthToken == "" {
		fn.AuthToken = os.Getenv("FABRIC_AUTH_TOKEN")
	}

	a := &c.Agent
	a.Mode = strings.ToLower(strings.TrimSpace(a.Mode))
	switch a.Mode {
	case "", "mock", "live":
		a.Mode = "heuristic"
	}
	if a.TimeoutSeconds <= 0 {
		a.TimeoutSeconds = 120
	}
	if a.NumCtx <= 0 {
		a.NumCtx = 8192
	}
	if a.MaxTokens <= 0 {
		a.MaxTokens = 1024
	}
	switch {
	case a.MaxRetries == 0: // unset
		a.MaxRetries = 2
	case a.MaxRetries < 0: // explicitly disabled
		a.MaxRetries = 0
	}
	if a.MaxRows <= 0 {
		a.MaxRows = 100
	}
	if a.SchemaTTLSeconds <= 0 {
		a.SchemaTTLSeconds = 300
	}
	if a.APIKey == "" {
		switch a.Mode {
		case "openai":
			a.APIKey = os.Getenv("OPENAI_API_KEY")
		case "anthropic":
			a.APIKey = os.Getenv("ANTHROPIC_API_KEY")
		}
	}
	if a.Model == "" {
		switch a.Mode {
		case "openai":
			a.Model = "gpt-4o"
		case "anthropic":
			a.Model = "claude-sonnet-5-5"
		case "ollama":
			a.Model = "llama3.1"
		}
	}

	s := &c.Storage
	if s.DownloadDir == "" {
		s.DownloadDir = "./tmp/downloads"
	}
	if s.OutputDir == "" {
		s.OutputDir = "./tmp/derived"
	}
}

func (c *Config) validate() error {
	if _, err := logx.Parse(c.LogLevel); err != nil {
		return fmt.Errorf("log_level: %w", err)
	}
	a := &c.Agent
	switch a.Mode {
	case "heuristic", "ollama":
	case "openai", "anthropic":
		if a.APIKey == "" {
			return fmt.Errorf("agent.mode %q requires agent.api_key or the %s environment variable",
				a.Mode, map[string]string{"openai": "OPENAI_API_KEY", "anthropic": "ANTHROPIC_API_KEY"}[a.Mode])
		}
	default:
		return fmt.Errorf("unknown agent.mode %q (use openai, anthropic, ollama or heuristic)", a.Mode)
	}
	if c.FabricNode.DataURL == "" {
		return fmt.Errorf("fabric_node.data_url must be set")
	}
	return nil
}
