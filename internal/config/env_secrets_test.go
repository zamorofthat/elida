package config

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadYAML(t *testing.T, yaml string) *Config {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "elida.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// Feedback #9(a): ${ENV} expands in backend url/api_key fields.
func TestEnvExpansionInBackendFields(t *testing.T) {
	t.Setenv("TEST_ELIDA_KEY", "sk-secret")
	t.Setenv("TEST_ELIDA_URL", "http://127.0.0.1:8001")
	cfg := loadYAML(t, "listen: \"127.0.0.1:0\"\nbackend: \"http://127.0.0.1:1\"\n"+
		"backends:\n  local:\n    url: \"${TEST_ELIDA_URL}\"\n    type: openai\n    default: true\n    api_key: \"${TEST_ELIDA_KEY}\"\n")
	b := cfg.Backends["local"]
	if b.URL != "http://127.0.0.1:8001" || b.APIKey != "sk-secret" {
		t.Errorf("expansion failed: url=%q key=%q", b.URL, b.APIKey)
	}
}

// Unset variable: value left literal (and a warning logged — behavior, not log, is asserted).
func TestEnvExpansionUnsetLeftLiteral(t *testing.T) {
	cfg := loadYAML(t, "listen: \"127.0.0.1:0\"\nbackend: \"http://127.0.0.1:1\"\n"+
		"backends:\n  local:\n    url: \"http://127.0.0.1:1\"\n    type: openai\n    default: true\n    api_key: \"${DEFINITELY_UNSET_VAR_XYZ}\"\n")
	if got := cfg.Backends["local"].APIKey; got != "${DEFINITELY_UNSET_VAR_XYZ}" {
		t.Errorf("unset var mangled: %q", got)
	}
}

// Unset variable in a backend api_key: beyond the generic expansion warning,
// a backend-specific warning must call out that the literal "${VAR}" text
// is about to be sent to that backend as a credential (MINOR finding).
func TestEnvExpansionUnsetBackendAPIKeyWarnsSpecifically(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	cfg := loadYAML(t, "listen: \"127.0.0.1:0\"\nbackend: \"http://127.0.0.1:1\"\n"+
		"backends:\n  local:\n    url: \"http://127.0.0.1:1\"\n    type: openai\n    default: true\n    api_key: \"${DEFINITELY_UNSET_VAR_XYZ}\"\n")
	if got := cfg.Backends["local"].APIKey; got != "${DEFINITELY_UNSET_VAR_XYZ}" {
		t.Fatalf("unset var mangled: %q", got)
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, "backend api_key references unset environment variable and will be sent as a literal credential") {
		t.Errorf("expected backend-specific unset-var warning, got log output: %s", logOutput)
	}
	if !strings.Contains(logOutput, "backend=local") {
		t.Errorf("expected warning to name the backend, got log output: %s", logOutput)
	}
	if !strings.Contains(logOutput, "var=DEFINITELY_UNSET_VAR_XYZ") {
		t.Errorf("expected warning to name the unset var, got log output: %s", logOutput)
	}
}

// SAFETY: policy rule patterns must never be expanded — a regex containing
// ${IDENT}-shaped text survives even when an env var of that name exists.
func TestPolicyPatternsNeverExpanded(t *testing.T) {
	t.Setenv("PATH_LIKE", "oops")
	cfg := loadYAML(t, "listen: \"127.0.0.1:0\"\nbackend: \"http://127.0.0.1:1\"\n"+
		"policy:\n  enabled: true\n  circuit_breaker:\n    enabled: false\n  rules:\n    - name: r1\n      type: content_match\n      patterns:\n        - \"${PATH_LIKE}$\"\n      action: flag\n")
	if got := cfg.Policy.Rules[len(cfg.Policy.Rules)-1].Patterns[0]; got != "${PATH_LIKE}$" {
		t.Errorf("policy pattern was expanded: %q", got)
	}
}

// Feedback #9(b): empty api_key auto-reads <NAME>_API_KEY then the type var.
func TestAutoProviderKeyByName(t *testing.T) {
	t.Setenv("NEMOTRON_API_KEY", "nvapi-1")
	cfg := loadYAML(t, "listen: \"127.0.0.1:0\"\nbackend: \"http://127.0.0.1:1\"\n"+
		"backends:\n  nemotron:\n    url: \"http://127.0.0.1:2\"\n    type: openai\n    default: true\n")
	if got := cfg.Backends["nemotron"].APIKey; got != "nvapi-1" {
		t.Errorf("name-based auto key failed: %q", got)
	}
}

func TestAutoProviderKeyByType(t *testing.T) {
	t.Setenv("MISTRAL_API_KEY", "msk-1")
	cfg := loadYAML(t, "listen: \"127.0.0.1:0\"\nbackend: \"http://127.0.0.1:1\"\n"+
		"backends:\n  eu-llm:\n    url: \"http://127.0.0.1:3\"\n    type: mistral\n    default: true\n")
	if got := cfg.Backends["eu-llm"].APIKey; got != "msk-1" {
		t.Errorf("type-based auto key failed: %q", got)
	}
}

// Explicit api_key always wins over env conventions.
func TestExplicitKeyBeatsAutoKey(t *testing.T) {
	t.Setenv("MISTRAL_API_KEY", "env-key")
	cfg := loadYAML(t, "listen: \"127.0.0.1:0\"\nbackend: \"http://127.0.0.1:1\"\n"+
		"backends:\n  m:\n    url: \"http://127.0.0.1:3\"\n    type: mistral\n    default: true\n    api_key: \"explicit\"\n")
	if got := cfg.Backends["m"].APIKey; got != "explicit" {
		t.Errorf("explicit key overridden: %q", got)
	}
}

// TestMaxCapturedChunksDefaultAndLoad verifies StorageConfig.MaxCapturedChunks:
// - defaults to 100
// - loads from YAML correctly
// - normalizes <= 0 values to 100
func TestMaxCapturedChunksDefaultAndLoad(t *testing.T) {
	// Test default value
	cfg := DefaultConfig()
	if cfg.Storage.MaxCapturedChunks != 100 {
		t.Errorf("default = %d, want 100", cfg.Storage.MaxCapturedChunks)
	}

	// Test load from YAML with explicit value 500
	cfg = loadYAML(t, "listen: \"127.0.0.1:0\"\nbackend: \"http://127.0.0.1:1\"\n"+
		"storage:\n  enabled: true\n  max_captured_chunks: 500\n")
	if cfg.Storage.MaxCapturedChunks != 500 {
		t.Errorf("loaded 500: got %d, want 500", cfg.Storage.MaxCapturedChunks)
	}

	// Test load from YAML with zero (should normalize to 100)
	cfg = loadYAML(t, "listen: \"127.0.0.1:0\"\nbackend: \"http://127.0.0.1:1\"\n"+
		"storage:\n  enabled: true\n  max_captured_chunks: 0\n")
	if cfg.Storage.MaxCapturedChunks != 100 {
		t.Errorf("loaded 0 (should normalize): got %d, want 100", cfg.Storage.MaxCapturedChunks)
	}

	// Test load from YAML with negative value (should normalize to 100)
	cfg = loadYAML(t, "listen: \"127.0.0.1:0\"\nbackend: \"http://127.0.0.1:1\"\n"+
		"storage:\n  enabled: true\n  max_captured_chunks: -50\n")
	if cfg.Storage.MaxCapturedChunks != 100 {
		t.Errorf("loaded -50 (should normalize): got %d, want 100", cfg.Storage.MaxCapturedChunks)
	}
}
