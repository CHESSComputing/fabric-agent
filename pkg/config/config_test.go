package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeFormat(t *testing.T) {
	for in, want := range map[string]string{
		"": "stdout", "stdout": "stdout", "TEXT": "stdout", "key-value": "stdout",
		"table": "table", "json": "json", " JSON ": "json",
	} {
		got, err := NormalizeFormat(in)
		if err != nil || got != want {
			t.Errorf("NormalizeFormat(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := NormalizeFormat("xml"); err == nil {
		t.Error("expected an error for an unknown format")
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigOutputSection(t *testing.T) {
	base := "fabric_node:\n  data_url: http://localhost:8782\nagent:\n  mode: heuristic\n"

	cfg, err := LoadConfig(writeConfig(t, base))
	if err != nil || cfg.Output.Format != "stdout" || cfg.Output.OmitBodies {
		t.Fatalf("defaults: %+v, %v", cfg.Output, err)
	}

	cfg, err = LoadConfig(writeConfig(t, base+"output:\n  format: JSON\n  omit_bodies: true\n"))
	if err != nil || cfg.Output.Format != "json" || !cfg.Output.OmitBodies {
		t.Fatalf("json: %+v, %v", cfg.Output, err)
	}

	_, err = LoadConfig(writeConfig(t, base+"output:\n  format: yaml\n"))
	if err == nil || !strings.Contains(err.Error(), "output.format") {
		t.Errorf("expected an output.format error, got %v", err)
	}
}
