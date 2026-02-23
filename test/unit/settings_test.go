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
	settingsPath := filepath.Join(dir, "settings.yaml")
	if _, statErr := os.Stat(settingsPath); os.IsNotExist(statErr) {
		t.Error("settings.yaml file was not created")
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
	err = store.SaveLocal(local)
	if err != nil {
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
	err = store.SaveLocal(local)
	if err != nil {
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
	settingsPath := filepath.Join(dir, "settings.yaml")
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Error("settings.yaml should be removed after reset")
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
	err = store.SaveLocal(local)
	if err != nil {
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
	err = store.SaveLocal(local)
	if err != nil {
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
	err = store.SaveLocal(local)
	if err != nil {
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

func TestNewSettingsStoreFromConfig(t *testing.T) {
	dir := t.TempDir()

	// Create a Config with custom values
	cfg := &config.Config{
		Policy: config.PolicyConfig{
			Enabled: true,
			Mode:    "audit",
			Preset:  "strict",
			RiskLadder: config.RiskLadderConfig{
				Enabled: true,
				Thresholds: []config.RiskThresholdConfig{
					{Score: 10, Action: "warn"},
					{Score: 25, Action: "throttle"},
					{Score: 40, Action: "block"},
					{Score: 60, Action: "terminate"},
				},
			},
		},
		Storage: config.StorageConfig{
			CaptureMode:           "all",
			MaxCaptureSize:        20000,
			MaxCapturedPerSession: 200,
		},
	}

	store, err := config.NewSettingsStoreFromConfig(cfg, dir)
	if err != nil {
		t.Fatalf("failed to create settings store from config: %v", err)
	}

	defaults := store.GetDefaults()

	// Check that Config values are used as defaults
	if defaults.Policy.Enabled == nil || !*defaults.Policy.Enabled {
		t.Error("expected policy.enabled from config (true)")
	}
	if defaults.Policy.Mode == nil || *defaults.Policy.Mode != "audit" {
		t.Errorf("expected policy.mode from config (audit), got %v", defaults.Policy.Mode)
	}
	if defaults.Policy.Preset == nil || *defaults.Policy.Preset != "strict" {
		t.Errorf("expected policy.preset from config (strict), got %v", defaults.Policy.Preset)
	}

	// Check risk ladder thresholds from config
	if defaults.Policy.RiskLadder == nil {
		t.Fatal("expected risk_ladder to be set from config")
	}
	if defaults.Policy.RiskLadder.WarnScore == nil || *defaults.Policy.RiskLadder.WarnScore != 10 {
		t.Errorf("expected warn_score=10 from config, got %v", defaults.Policy.RiskLadder.WarnScore)
	}
	if defaults.Policy.RiskLadder.ThrottleScore == nil || *defaults.Policy.RiskLadder.ThrottleScore != 25 {
		t.Errorf("expected throttle_score=25 from config, got %v", defaults.Policy.RiskLadder.ThrottleScore)
	}
	if defaults.Policy.RiskLadder.BlockScore == nil || *defaults.Policy.RiskLadder.BlockScore != 40 {
		t.Errorf("expected block_score=40 from config, got %v", defaults.Policy.RiskLadder.BlockScore)
	}
	if defaults.Policy.RiskLadder.TerminateScore == nil || *defaults.Policy.RiskLadder.TerminateScore != 60 {
		t.Errorf("expected terminate_score=60 from config, got %v", defaults.Policy.RiskLadder.TerminateScore)
	}

	// Check capture settings from storage config
	if defaults.Capture.Mode == nil || *defaults.Capture.Mode != "all" {
		t.Errorf("expected capture.mode from config (all), got %v", defaults.Capture.Mode)
	}
	if defaults.Capture.MaxCaptureSize == nil || *defaults.Capture.MaxCaptureSize != 20000 {
		t.Errorf("expected max_capture_size from config (20000), got %v", defaults.Capture.MaxCaptureSize)
	}
	if defaults.Capture.MaxPerSession == nil || *defaults.Capture.MaxPerSession != 200 {
		t.Errorf("expected max_per_session from config (200), got %v", defaults.Capture.MaxPerSession)
	}
}

func TestNewSettingsStoreFromConfig_LocalOverrides(t *testing.T) {
	dir := t.TempDir()

	// Config says "audit" mode
	cfg := &config.Config{
		Policy: config.PolicyConfig{
			Mode: "audit",
		},
	}

	store, err := config.NewSettingsStoreFromConfig(cfg, dir)
	if err != nil {
		t.Fatalf("failed to create settings store: %v", err)
	}

	// Save local override to "enforce"
	enforce := "enforce"
	err = store.SaveLocal(config.Settings{
		Policy: config.PolicySettings{
			Mode: &enforce,
		},
	})
	if err != nil {
		t.Fatalf("failed to save local settings: %v", err)
	}

	// Merged should use local override
	merged := store.GetMerged()
	if merged.Policy.Mode == nil || *merged.Policy.Mode != "enforce" {
		t.Errorf("expected local override (enforce), got %v", merged.Policy.Mode)
	}

	// Defaults should still be from config
	defaults := store.GetDefaults()
	if defaults.Policy.Mode == nil || *defaults.Policy.Mode != "audit" {
		t.Errorf("defaults should still be from config (audit), got %v", defaults.Policy.Mode)
	}
}

func TestNewSettingsStoreFromConfig_PersistenceAcrossRestart(t *testing.T) {
	dir := t.TempDir()

	// Simulate initial startup with Config
	cfg := &config.Config{
		Policy: config.PolicyConfig{
			Enabled: true,
			Mode:    "audit",
			Preset:  "strict",
			RiskLadder: config.RiskLadderConfig{
				Enabled: true,
				Thresholds: []config.RiskThresholdConfig{
					{Score: 10, Action: "warn"},
					{Score: 30, Action: "block"},
				},
			},
		},
		Storage: config.StorageConfig{
			CaptureMode:    "all",
			MaxCaptureSize: 50000,
		},
	}

	store1, err := config.NewSettingsStoreFromConfig(cfg, dir)
	if err != nil {
		t.Fatalf("failed to create initial settings store: %v", err)
	}

	// User makes changes via UI (local overrides)
	enabled := true
	newThrottle := 20
	err = store1.SaveLocal(config.Settings{
		Failover: config.FailoverSettings{
			Enabled:       &enabled,
			FallbackOrder: []string{"groq", "openai"},
		},
		Policy: config.PolicySettings{
			RiskLadder: &config.RiskLadderSettings{
				ThrottleScore: &newThrottle,
			},
		},
	})
	if err != nil {
		t.Fatalf("failed to save local settings: %v", err)
	}

	// Simulate restart - create new store with SAME config and directory
	store2, err := config.NewSettingsStoreFromConfig(cfg, dir)
	if err != nil {
		t.Fatalf("failed to create settings store after restart: %v", err)
	}

	// Verify defaults still come from Config
	defaults := store2.GetDefaults()
	if defaults.Policy.Mode == nil || *defaults.Policy.Mode != "audit" {
		t.Errorf("after restart: defaults should be from config (audit), got %v", defaults.Policy.Mode)
	}
	if defaults.Capture.Mode == nil || *defaults.Capture.Mode != "all" {
		t.Errorf("after restart: capture.mode should be from config (all), got %v", defaults.Capture.Mode)
	}

	// Verify local overrides were loaded from settings.yaml
	local := store2.GetLocal()
	if local.Failover.Enabled == nil || !*local.Failover.Enabled {
		t.Error("after restart: local failover.enabled should be true")
	}
	if len(local.Failover.FallbackOrder) != 2 || local.Failover.FallbackOrder[0] != "groq" {
		t.Errorf("after restart: local fallback_order should be [groq, openai], got %v", local.Failover.FallbackOrder)
	}
	if local.Policy.RiskLadder == nil || local.Policy.RiskLadder.ThrottleScore == nil || *local.Policy.RiskLadder.ThrottleScore != 20 {
		t.Error("after restart: local throttle_score should be 20")
	}

	// Verify merged combines both correctly
	merged := store2.GetMerged()

	// From Config defaults
	if merged.Policy.Mode == nil || *merged.Policy.Mode != "audit" {
		t.Errorf("merged mode should be from config (audit), got %v", merged.Policy.Mode)
	}
	if merged.Policy.RiskLadder.WarnScore == nil || *merged.Policy.RiskLadder.WarnScore != 10 {
		t.Errorf("merged warn_score should be from config (10), got %v", merged.Policy.RiskLadder.WarnScore)
	}
	if merged.Policy.RiskLadder.BlockScore == nil || *merged.Policy.RiskLadder.BlockScore != 30 {
		t.Errorf("merged block_score should be from config (30), got %v", merged.Policy.RiskLadder.BlockScore)
	}

	// From local overrides (settings.yaml)
	if merged.Failover.Enabled == nil || !*merged.Failover.Enabled {
		t.Error("merged failover.enabled should be from local (true)")
	}
	if merged.Policy.RiskLadder.ThrottleScore == nil || *merged.Policy.RiskLadder.ThrottleScore != 20 {
		t.Errorf("merged throttle_score should be from local (20), got %v", merged.Policy.RiskLadder.ThrottleScore)
	}
}
