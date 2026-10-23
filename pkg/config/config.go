package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
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

	// GraphDumpQuery is the SPARQL template used when a named graph is
	// downloaded. Placeholders: {{graph}} (IRI, without <>) and {{limit}}.
	GraphDumpQuery string `yaml:"graph_dump_query"`
	GraphDumpLimit int    `yaml:"graph_dump_limit"`
}

// AgentConfig controls how natural-language questions are turned into SPARQL.
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

	// MaxRetries is the number of times the agent may ask the LLM to repair a
	// query that was rejected by the validator or by the SPARQL endpoint.
	// Unset/0 means 2; use a negative value to disable repairs.
	MaxRetries int `yaml:"max_retries"`
	// MaxRows caps the LIMIT of generated queries and the rows printed.
	MaxRows int `yaml:"max_rows"`

	SchemaMaxChars   int `yaml:"schema_max_chars"`   // per VoID/SHACL/examples document
	SchemaTTLSeconds int `yaml:"schema_ttl_seconds"` // schema cache lifetime

	// Summarize asks the LLM to answer the question in prose from the results.
	Summarize bool `yaml:"summarize"`
	// DialectHint is free text appended to the LLM prompt, e.g. "The endpoint
	// does not support FILTER or property paths."
	DialectHint string `yaml:"dialect_hint"`
}

type StorageConfig struct {
	DownloadDir string `yaml:"download_dir"`
	OutputDir   string `yaml:"output_dir"`
}

// DefaultGraphDumpQuery is used when graph_dump_query is not configured.
const DefaultGraphDumpQuery = "SELECT ?s ?p ?o WHERE { GRAPH <{{graph}}> { ?s ?p ?o } } LIMIT {{limit}}"

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
	fn := &c.FabricNode
	if fn.TimeoutSeconds <= 0 {
		fn.TimeoutSeconds = 30
	}
	if fn.GraphDumpQuery == "" {
		fn.GraphDumpQuery = DefaultGraphDumpQuery
	}
	if fn.GraphDumpLimit <= 0 {
		fn.GraphDumpLimit = 50
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
	switch {
	case a.MaxRetries == 0: // unset
		a.MaxRetries = 2
	case a.MaxRetries < 0: // explicitly disabled
		a.MaxRetries = 0
	}
	if a.MaxRows <= 0 {
		a.MaxRows = 100
	}
	if a.SchemaMaxChars <= 0 {
		a.SchemaMaxChars = 6000
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
