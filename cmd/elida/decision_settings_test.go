package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elida/internal/config"
)

// settingsApp is a decision app (gate provider) with a settings store whose
// settings.yaml holds yamlBody, set up the way main does.
//
//nolint:unparam // mode is part of the helper's contract; every current test starts in audit.
func settingsApp(t *testing.T, mode, yamlBody string, mutate func(*config.Config)) *app {
	t.Helper()
	quietLogs(t)
	cfg := decisionTestConfig(t)
	cfg.Policy.Enabled = true
	cfg.Policy.Mode = "enforce"
	cfg.Decision.Mode = mode
	if mutate != nil {
		mutate(cfg)
	}
	a := newDecisionApp(t, cfg, &gate{})
	dir := t.TempDir()
	a.configPath = filepath.Join(dir, "elida.yaml")
	if yamlBody != "" {
		if err := os.WriteFile(filepath.Join(dir, "settings.yaml"), []byte(yamlBody), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	a.initSessionStore()
	a.initPolicyEngine()
	if err := a.setupDecision(context.Background()); err != nil {
		t.Fatalf("setupDecision: %v", err)
	}
	t.Cleanup(func() { a.shutdownDecision(context.Background()) })
	a.initSettings()
	return a
}

func decisionModeSettings(s string) config.DecisionSettings { return config.DecisionSettings{Mode: &s} }

func TestDecisionSettingsApply_ModeChangeReachesTheRunnerAndStatus(t *testing.T) {
	a := settingsApp(t, config.DecisionModeAudit, "", nil)
	if err := a.ApplyDecisionSettings(decisionModeSettings(config.DecisionModeShadow), "enforce"); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if a.decisionRunner.EffectiveMode() != config.DecisionModeShadow || a.DecisionStatus().Mode != config.DecisionModeShadow {
		t.Fatalf("runner=%q status=%q", a.decisionRunner.EffectiveMode(), a.DecisionStatus().Mode)
	}
	if lim := a.DecisionStatus().Limits; lim.MaxConcurrency != a.cfg.Decision.MaxConcurrency || lim.ThresholdSet != a.cfg.Decision.ThresholdSet {
		t.Fatalf("limits = %+v", lim)
	}
}

// Review Focus 5.
func TestDecisionSettingsApply_UnchangedDecisionSavesWhenNotRunning(t *testing.T) {
	a := settingsApp(t, config.DecisionModeAudit, "", func(c *config.Config) { c.Decision.Enabled = false })
	a.initControlAPI()
	srv := a.controlHandler

	get := httptest.NewRecorder()
	srv.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/control/settings", nil))
	put := httptest.NewRecorder()
	srv.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/control/settings", strings.NewReader(get.Body.String())))
	if put.Code != http.StatusOK {
		t.Fatalf("unchanged save while not running: %d %s", put.Code, put.Body.String())
	}
	change := httptest.NewRecorder()
	srv.ServeHTTP(change, httptest.NewRequest(http.MethodPut, "/control/settings", strings.NewReader(`{"decision":{"mode":"shadow"}}`)))
	if change.Code != http.StatusBadRequest || !strings.Contains(change.Body.String(), "not running") {
		t.Fatalf("a decision change while not running: %d %s", change.Code, change.Body.String())
	}
}

func TestDecisionStartupSettings_AppliesTheSavedMode(t *testing.T) {
	a := settingsApp(t, config.DecisionModeAudit, "decision:\n  mode: shadow\n", nil)
	if err := a.applyStartupDecisionSettings(); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if a.decisionRunner.EffectiveMode() != config.DecisionModeShadow {
		t.Fatalf("effective = %q, want the settings.yaml mode", a.decisionRunner.EffectiveMode())
	}
}

// Review Focus 4.
func TestDecisionStartupSettings_StaleRestartOnlyValuesIgnored(t *testing.T) {
	a := settingsApp(t, config.DecisionModeAudit, "decision:\n  max_concurrency: 1\n", nil)
	if err := a.applyStartupDecisionSettings(); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := a.DecisionStatus().Limits.MaxConcurrency; got != a.cfg.Decision.MaxConcurrency {
		t.Fatalf("max_concurrency in force = %d, want elida.yaml's %d", got, a.cfg.Decision.MaxConcurrency)
	}
}

// D9: settings.yaml enforce with a mismatched threshold set fails startup,
// as it does in elida.yaml.
func TestDecisionStartupSettings_EnforceMismatchFailsStartup(t *testing.T) {
	a := settingsApp(t, config.DecisionModeAudit, "decision:\n  mode: enforce\n", func(c *config.Config) { c.Decision.ThresholdSet = "v999" })
	if err := a.applyStartupDecisionSettings(); err == nil || !strings.Contains(err.Error(), "enforce") {
		t.Fatalf("err = %v, want an enforce refusal", err)
	}
}

// /control/decision reports the restart-only limits and the async results
// dropped while disabled.
func TestDecisionStatus_ReportsLimitsAndAsyncDroppedDisabled(t *testing.T) {
	a := settingsApp(t, config.DecisionModeAudit, "", nil)
	raw, err := json.Marshal(a.DecisionStatus())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["async_dropped_disabled"]; !ok {
		t.Fatalf("status lacks async_dropped_disabled: %s", raw)
	}
	lim, ok := m["limits"].(map[string]any)
	if !ok {
		t.Fatalf("status lacks limits: %s", raw)
	}
	for _, k := range []string{"threshold_set", "elevated_threshold", "inline_timeout_ms", "max_concurrency", "max_inline_tokens", "max_inline_windows", "max_async_windows", "async_queue_size"} {
		if _, ok := lim[k]; !ok {
			t.Fatalf("limits lacks %s: %s", k, raw)
		}
	}
	if got, ok := lim["inline_timeout_ms"].(float64); !ok || int(got) != int(a.cfg.Decision.InlineTimeout/time.Millisecond) {
		t.Fatalf("inline_timeout_ms = %v", lim["inline_timeout_ms"])
	}
}
