package config

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestPanelConfigDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Panel.ToolChainShadow {
		t.Error("ToolChainShadow default = false, want true")
	}
	if cfg.Panel.ToolChainWeight != 1.0 {
		t.Errorf("ToolChainWeight default = %v, want 1.0", cfg.Panel.ToolChainWeight)
	}
}

func TestPanelConfigLoadDefaultsWhenKeysAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "elida.yaml")
	if err := os.WriteFile(path, []byte("panel:\n  tool_chain_artifact: /x.json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Panel.ToolChainShadow || cfg.Panel.ToolChainWeight != 1.0 {
		t.Errorf("absent keys: shadow=%v weight=%v, want true/1.0", cfg.Panel.ToolChainShadow, cfg.Panel.ToolChainWeight)
	}
}

func TestPanelConfigYAMLParse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "elida.yaml")
	body := "panel:\n  tool_chain_shadow: false\n  tool_chain_weight: 0.5\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Panel.ToolChainShadow || cfg.Panel.ToolChainWeight != 0.5 {
		t.Errorf("shadow=%v weight=%v, want false/0.5", cfg.Panel.ToolChainShadow, cfg.Panel.ToolChainWeight)
	}
}

func TestPanelConfigToolChainWeightValidation(t *testing.T) {
	for _, w := range []float64{1.5, -0.1, math.NaN(), math.Inf(1)} {
		cfg := DefaultConfig()
		cfg.Panel.ToolChainWeight = w
		result := cfg.Validate()
		if result.Valid || !hasFieldError(result, "panel.tool_chain_weight") {
			t.Errorf("weight %v: expected error on panel.tool_chain_weight, got valid=%v errors=%+v", w, result.Valid, result.Errors)
		}
	}
	for _, w := range []float64{0, 0.5, 1} {
		cfg := DefaultConfig()
		cfg.Panel.ToolChainWeight = w
		if hasFieldError(cfg.Validate(), "panel.tool_chain_weight") {
			t.Errorf("weight %v should be accepted", w)
		}
	}
}
