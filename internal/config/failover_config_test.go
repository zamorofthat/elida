package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFailoverConfigDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Failover.Enabled {
		t.Error("failover must default to disabled")
	}
	if cfg.Failover.MaxRetries != 2 {
		t.Errorf("MaxRetries default = %d, want 2", cfg.Failover.MaxRetries)
	}
}

func TestFailoverConfigLoadsFromYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "elida.yaml")
	yaml := "listen: \"127.0.0.1:0\"\nbackend: \"http://127.0.0.1:1\"\n" +
		"failover:\n  enabled: true\n  max_retries: 3\n  fallback_order: [local, nemotron]\n" +
		"backends:\n  local:\n    url: \"http://127.0.0.1:1\"\n    type: openai\n    default: true\n" +
		"  nemotron:\n    url: \"https://integrate.api.nvidia.com/v1\"\n    type: openai\n" +
		"    model: \"nvidia/llama-3.3-nemotron-super-49b-v1\"\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Failover.Enabled || cfg.Failover.MaxRetries != 3 {
		t.Errorf("failover block not loaded: %+v", cfg.Failover)
	}
	if got := cfg.Failover.FallbackOrder; len(got) != 2 || got[0] != "local" {
		t.Errorf("fallback_order = %v", got)
	}
	if cfg.Backends["nemotron"].Model != "nvidia/llama-3.3-nemotron-super-49b-v1" {
		t.Errorf("backend model not loaded: %q", cfg.Backends["nemotron"].Model)
	}
}
