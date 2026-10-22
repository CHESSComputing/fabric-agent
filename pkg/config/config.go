package config

import (
	"os"

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
	AuthToken       string `yaml:"auth_token"`
}

type AgentConfig struct {
	Mode        string  `yaml:"mode"`
	APIKey      string  `yaml:"api_key"`
	Model       string  `yaml:"model"`
	Temperature float64 `yaml:"temperature"`
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

	return &cfg, nil
}
