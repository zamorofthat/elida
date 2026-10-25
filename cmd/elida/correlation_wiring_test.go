package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/correlation"
)

func correlationTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.Policy.Enabled = true
	cfg.Correlation.Enabled = true
	return cfg
}

// Wiring review M-1: an enabled correlation block that cannot be built as
// configured fails startup instead of running with correlation off. An
// explicit max_history_events: 0 used to validate (0 meant "default") and
// then disable correlation with an ERROR.
func TestCorrelationWiring_InvalidConfigFailsStartup(t *testing.T) {
	quietLogs(t)
	for name, mutate := range map[string]func(*config.Config){
		"zero history":  func(c *config.Config) { c.Correlation.MaxHistoryEvents = 0 },
		"zero sessions": func(c *config.Config) { c.Correlation.MaxSessions = 0 },
		"one-step sequence rule": func(c *config.Config) {
			c.Correlation.Rules = []correlation.Rule{{
				Name: "ordered", Shape: correlation.ShapeSequence,
				Sequence: []string{"a_b"}, Window: time.Minute,
			}}
		},
	} {
		cfg := correlationTestConfig(t)
		mutate(cfg)
		a := &app{cfg: cfg}
		if err := a.setupPolicyEngine(); err == nil || !strings.Contains(err.Error(), "correlation") {
			t.Errorf("%s: want a startup error naming correlation, got %v", name, err)
		}
		if a.policyEngine != nil {
			t.Errorf("%s: a failed startup must not leave a policy engine", name)
		}
	}
}

// Plan B Task 3: the engine implements ordered sequences, so a valid
// sequence rule starts (configuration used to refuse shape: sequence while
// the engine was a stub).
func TestCorrelationWiring_SequenceRuleStarts(t *testing.T) {
	quietLogs(t)
	cfg := correlationTestConfig(t)
	cfg.Correlation.Rules = []correlation.Rule{{
		Name: "ordered", Shape: correlation.ShapeSequence,
		Sequence: []string{"a_b", "c_d"}, Window: time.Minute,
	}}
	a := &app{cfg: cfg}
	if err := a.setupPolicyEngine(); err != nil {
		t.Fatalf("a valid sequence rule must start: %v", err)
	}
	if a.policyEngine == nil {
		t.Fatal("want a policy engine")
	}
}

// The same value in elida.yaml is refused when the file is loaded:
// validation checks the limits exactly as they will be built.
func TestCorrelationWiring_ZeroHistoryInYAMLIsInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "elida.yaml")
	body := "correlation:\n  enabled: true\n  max_history_events: 0\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil || !strings.Contains(err.Error(), "max_history_events") {
		t.Fatalf("max_history_events: 0 must fail config loading, got %v", err)
	}
}

func TestCorrelationWiring_ValidConfigStarts(t *testing.T) {
	quietLogs(t)
	a := &app{cfg: correlationTestConfig(t)}
	if err := a.setupPolicyEngine(); err != nil {
		t.Fatalf("a valid correlation block must start: %v", err)
	}
	if !a.policyEngine.GetConfig().Correlation.Enabled {
		t.Fatal("correlation must be enabled")
	}
}

// Wiring review M-8: correlation enabled with the policy engine disabled is
// inert; startup says so once, at WARN.
func TestCorrelationWiring_WarnsWhenPolicyDisabled(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		var buf strings.Builder
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
		cfg := correlationTestConfig(t)
		cfg.Policy.Enabled = false
		cfg.Correlation.Enabled = enabled
		a := &app{cfg: cfg}
		err := a.setupPolicyEngine()
		slog.SetDefault(prev)
		if err != nil {
			t.Fatalf("setupPolicyEngine: %v", err)
		}
		want := 0
		if enabled {
			want = 1
		}
		if got := strings.Count(buf.String(), "policy.enabled is false"); got != want {
			t.Errorf("correlation enabled=%v: %d warnings, want %d\n%s", enabled, got, want, buf.String())
		}
	}
}
