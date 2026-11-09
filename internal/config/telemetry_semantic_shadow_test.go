package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTelemetrySemanticShadowLogsDefaultsOff(t *testing.T) {
	if DefaultConfig().Telemetry.SemanticShadowLogs {
		t.Fatal("telemetry.semantic_shadow_logs must default to false")
	}
}

func TestTelemetrySemanticShadowLogsParses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "elida.yaml")
	yaml := "listen: \"127.0.0.1:0\"\nbackend: \"http://127.0.0.1:1\"\n" +
		"telemetry:\n  semantic_shadow_logs: true\n" +
		"backends:\n  local:\n    url: \"http://127.0.0.1:1\"\n    type: openai\n    default: true\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Telemetry.SemanticShadowLogs {
		t.Fatal("telemetry.semantic_shadow_logs: true was not loaded")
	}
}

func TestShippedConfigKeepsSemanticShadowLogsOff(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "configs", "elida.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Telemetry.SemanticShadowLogs {
		t.Fatal("the shipped config must not enable semantic shadow logs")
	}
}
