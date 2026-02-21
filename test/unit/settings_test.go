package unit

import (
	"os"
	"path/filepath"
	"testing"

	"elida/internal/config"
)

func TestSettingsStore_GetDefaults(t *testing.T) {
	dir := t.TempDir()
	store, err := config.NewSettingsStore(dir)
	if err != nil {
		t.Fatalf("failed to create settings store: %v", err)
	}

	defaults := store.GetDefaults()

	// Check default values
	if defaults.Policy.Enabled == nil || !*defaults.Policy.Enabled {
		t.Error("expected policy.enabled to be true by default")
	}
	if defaults.Policy.Mode == nil || *defaults.Policy.Mode != "enforce" {
		t.Error("expected policy.mode to be 'enforce' by default")
	}
	if defaults.Policy.Preset == nil || *defaults.Policy.Preset != "standard" {
		t.Error("expected policy.preset to be 'standard' by default")
	}
	if defaults.Failover.Enabled == nil || *defaults.Failover.Enabled {
		t.Error("expected failover.enabled to be false by default")
	}
	if defaults.Policy.RiskLadder == nil {
		t.Fatal("expected risk_ladder to be configured by default")
	}
	if defaults.Policy.RiskLadder.WarnScore == nil || *defaults.Policy.RiskLadder.WarnScore != 5 {
		t.Error("expected risk_ladder.warn_score to be 5 by default")
	}
}

func TestSettingsStore_SaveAndLoadLocal(t *testing.T) {
	dir := t.TempDir()
	store, err := config.NewSettingsStore(dir)
	if err != nil {
		t.Fatalf("failed to create settings store: %v", err)
	}

	// Modify local settings
	audit := "audit"
	strict := "strict"
	local := config.Settings{
		Policy: config.PolicySettings{
			Mode:   &audit,
			Preset: &strict,
		},
	}

	err = store.SaveLocal(local)
	if err != nil {
		t.Fatalf("failed to save local settings: %v", err)
	}

	// Check file was created
	settingsPath := filepath.Join(dir, "settings.json")
	if _, statErr := os.Stat(settingsPath); os.IsNotExist(statErr) {
		t.Error("settings.json file was not created")
	}

	// Create new store to test loading
	store2, err := config.NewSettingsStore(dir)
	if err != nil {
		t.Fatalf("failed to create second settings store: %v", err)
	}

	loaded := store2.GetLocal()
	if loaded.Policy.Mode == nil || *loaded.Policy.Mode != "audit" {
		t.Error("failed to load saved policy.mode")
	}
	if loaded.Policy.Preset == nil || *loaded.Policy.Preset != "strict" {
		t.Error("failed to load saved policy.preset")
	}
}

func TestSettingsStore_GetMerged(t *testing.T) {
	dir := t.TempDir()
	store, err := config.NewSettingsStore(dir)
	if err != nil {
		t.Fatalf("failed to create settings store: %v", err)
	}

	// Save partial local settings
	audit := "audit"
	local := config.Settings{
		Policy: config.PolicySettings{
			Mode: &audit, // Only override mode, not preset
		},
	}
	if err := store.SaveLocal(local); err != nil {
		t.Fatalf("failed to save local settings: %v", err)
	}

	// Get merged
	merged := store.GetMerged()

	// Mode should be from local
	if merged.Policy.Mode == nil || *merged.Policy.Mode != "audit" {
		t.Error("merged mode should be 'audit' from local")
	}

	// Preset should still be from defaults
	if merged.Policy.Preset == nil || *merged.Policy.Preset != "standard" {
		t.Error("merged preset should be 'standard' from defaults")
	}

	// Risk ladder should be from defaults
	if merged.Policy.RiskLadder == nil || *merged.Policy.RiskLadder.WarnScore != 5 {
		t.Error("merged risk_ladder should come from defaults")
	}
}

func TestSettingsStore_ResetToDefault(t *testing.T) {
	dir := t.TempDir()
	store, err := config.NewSettingsStore(dir)
	if err != nil {
		t.Fatalf("failed to create settings store: %v", err)
	}

	// Save local settings
	audit := "audit"
	local := config.Settings{
		Policy: config.PolicySettings{
			Mode: &audit,
		},
	}
	if err := store.SaveLocal(local); err != nil {
		t.Fatalf("failed to save local settings: %v", err)
	}

	// Verify local is set
	if store.GetLocal().Policy.Mode == nil {
		t.Error("local settings should be set")
	}

	// Reset
	err = store.ResetToDefault()
	if err != nil {
		t.Fatalf("failed to reset settings: %v", err)
	}

	// Verify local is cleared
	if store.GetLocal().Policy.Mode != nil {
		t.Error("local settings should be cleared after reset")
	}

	// Verify file is removed
	settingsPath := filepath.Join(dir, "settings.json")
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Error("settings.json should be removed after reset")
	}
}

func TestSettingsStore_GetDiff(t *testing.T) {
	dir := t.TempDir()
	store, err := config.NewSettingsStore(dir)
	if err != nil {
		t.Fatalf("failed to create settings store: %v", err)
	}

	// No local settings = no diff
	diff := store.GetDiff()
	if len(diff) != 0 {
		t.Errorf("expected no diff without local settings, got %d", len(diff))
	}

	// Add local settings
	audit := "audit"
	strict := "strict"
	throttle := 20
	local := config.Settings{
		Policy: config.PolicySettings{
			Mode:   &audit,
			Preset: &strict,
			RiskLadder: &config.RiskLadderSettings{
				ThrottleScore: &throttle,
			},
		},
	}
	if err := store.SaveLocal(local); err != nil {
		t.Fatalf("failed to save local settings: %v", err)
	}

	// Check diff
	diff = store.GetDiff()
	if len(diff) != 3 {
		t.Errorf("expected 3 diffs, got %d: %+v", len(diff), diff)
	}

	// Check specific diffs
	if d, ok := diff["policy.mode"]; ok {
		if d.DefaultValue != "enforce" || d.LocalValue != "audit" {
			t.Errorf("unexpected policy.mode diff: %+v", d)
		}
	} else {
		t.Error("expected policy.mode in diff")
	}

	if d, ok := diff["policy.preset"]; ok {
		if d.DefaultValue != "standard" || d.LocalValue != "strict" {
			t.Errorf("unexpected policy.preset diff: %+v", d)
		}
	} else {
		t.Error("expected policy.preset in diff")
	}
}

func TestSettingsStore_MergeRiskLadder(t *testing.T) {
	dir := t.TempDir()
	store, err := config.NewSettingsStore(dir)
	if err != nil {
		t.Fatalf("failed to create settings store: %v", err)
	}

	// Only override one threshold
	newWarn := 10
	local := config.Settings{
		Policy: config.PolicySettings{
			RiskLadder: &config.RiskLadderSettings{
				WarnScore: &newWarn,
			},
		},
	}
	if err := store.SaveLocal(local); err != nil {
		t.Fatalf("failed to save local settings: %v", err)
	}

	merged := store.GetMerged()
	rl := merged.Policy.RiskLadder

	// Warn should be overridden
	if rl.WarnScore == nil || *rl.WarnScore != 10 {
		t.Errorf("warn_score should be 10 from local, got %v", rl.WarnScore)
	}

	// Others should be from defaults
	if rl.ThrottleScore == nil || *rl.ThrottleScore != 15 {
		t.Errorf("throttle_score should be 15 from defaults, got %v", rl.ThrottleScore)
	}
	if rl.BlockScore == nil || *rl.BlockScore != 30 {
		t.Errorf("block_score should be 30 from defaults, got %v", rl.BlockScore)
	}
	if rl.TerminateScore == nil || *rl.TerminateScore != 50 {
		t.Errorf("terminate_score should be 50 from defaults, got %v", rl.TerminateScore)
	}
}

func TestSettingsStore_FailoverSettings(t *testing.T) {
	dir := t.TempDir()
	store, err := config.NewSettingsStore(dir)
	if err != nil {
		t.Fatalf("failed to create settings store: %v", err)
	}

	// Enable failover and set custom order
	enabled := true
	local := config.Settings{
		Failover: config.FailoverSettings{
			Enabled:       &enabled,
			FallbackOrder: []string{"groq", "openai", "ollama"},
		},
	}
	if err := store.SaveLocal(local); err != nil {
		t.Fatalf("failed to save local settings: %v", err)
	}

	merged := store.GetMerged()

	if merged.Failover.Enabled == nil || !*merged.Failover.Enabled {
		t.Error("failover should be enabled")
	}
	if len(merged.Failover.FallbackOrder) != 3 {
		t.Errorf("expected 3 fallback backends, got %d", len(merged.Failover.FallbackOrder))
	}
	if merged.Failover.FallbackOrder[0] != "groq" {
		t.Errorf("expected groq first, got %s", merged.Failover.FallbackOrder[0])
	}
}
