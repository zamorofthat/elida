package unit

import (
	"bytes"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"elida/internal/correlation"
	"elida/internal/policy"
)

// wiringEngine is a policy engine with one correlation rule: two
// semantic_injection events within a minute, critical, in ruleMode.
func wiringEngine(t *testing.T, policyMode string, ruleMode correlation.Mode) *policy.Engine {
	t.Helper()
	return policy.NewEngine(policy.Config{
		Enabled: true, Mode: policyMode,
		RiskLadder: policy.RiskLadderConfig{Enabled: true, Thresholds: baselineThresholds()},
		Correlation: policy.CorrelationConfig{
			Enabled: true,
			Limits:  okLimits(),
			Rules: []correlation.Rule{{
				Name: "repeated_injection", Shape: correlation.ShapeRepeated,
				Category: "semantic_injection", Count: 2, Window: time.Minute,
				Severity: "critical", Mode: ruleMode,
			}},
		},
	})
}

func correlationMatch(fs *policy.FlaggedSession) *policy.Violation {
	if fs == nil {
		return nil
	}
	for i := range fs.Violations {
		if fs.Violations[i].RuleName == "correlation.repeated_injection" {
			return &fs.Violations[i]
		}
	}
	return nil
}

func captureLogs(t *testing.T, level slog.Level) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: level})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// Wiring review I-1: a settings PUT or DELETE reloads the policy engine
// from Settings, which hold no correlation block. The reload must carry the
// startup block, so the correlator and its per-session state survive: one
// input before the save and one after still complete the match.
func TestCorrelationWiring_SettingsSaveKeepsCorrelationAndState(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			e := wiringEngine(t, "enforce", correlation.ModeAudit)
			h, _ := settingsHandler(t, e)
			now := time.Now()
			recordCorrelationInput(e, "sess-reload", "ev_a", now)

			var body any
			if method == http.MethodPut {
				body = map[string]any{"policy": map[string]any{"enabled": true, "mode": "enforce"}}
			}
			if w := doJSON(t, h, method, "/control/settings", body); w.Code != http.StatusOK {
				t.Fatalf("%s /control/settings: %d %s", method, w.Code, w.Body.String())
			}
			if !e.GetConfig().Correlation.Enabled {
				t.Fatal("a settings save must not disable correlation (restart-only, D4)")
			}

			recordCorrelationInput(e, "sess-reload", "ev_b", now.Add(time.Second))
			if correlationMatch(e.GetFlaggedSession("sess-reload")) == nil {
				t.Fatal("the match must still fire across a settings save: the engine or its state was replaced")
			}
		})
	}
}

// A reload with the same correlation block keeps the same engine, so a
// half-built match survives it. A changed block replaces the engine and
// logs that at INFO.
func TestCorrelationWiring_NoOpReloadKeepsStateChangedReloadLogs(t *testing.T) {
	buf := captureLogs(t, slog.LevelInfo)

	e := wiringEngine(t, "enforce", correlation.ModeAudit)
	now := time.Now()
	recordCorrelationInput(e, "sess-noop", "ev_a", now)
	e.ReloadConfig(e.GetConfig())
	recordCorrelationInput(e, "sess-noop", "ev_b", now.Add(time.Second))
	if correlationMatch(e.GetFlaggedSession("sess-noop")) == nil {
		t.Fatal("a reload with an unchanged correlation block must keep the engine and its state")
	}
	if contains(buf.String(), "correlation engine replaced") {
		t.Fatalf("an unchanged block must not replace the engine:\n%s", buf.String())
	}

	cfg := e.GetConfig()
	cfg.Correlation.Rules[0].Count = 3
	e.ReloadConfig(cfg)
	if !contains(buf.String(), "level=INFO") || !contains(buf.String(), "correlation engine replaced") {
		t.Fatalf("replacing the engine must be logged at INFO:\n%s", buf.String())
	}
	if got := e.GetConfig().Correlation.Rules[0].Count; got != 3 {
		t.Fatalf("the changed block must be in force, count = %d", got)
	}
}

// GetConfig returns a copy: editing it never edits the engine's block.
func TestCorrelationWiring_GetConfigCopiesRules(t *testing.T) {
	e := wiringEngine(t, "enforce", correlation.ModeAudit)
	cfg := e.GetConfig()
	cfg.Correlation.Rules[0].Name = "tampered"
	if e.GetConfig().Correlation.Rules[0].Name != "repeated_injection" {
		t.Fatal("GetConfig must deep-copy the correlation rules")
	}
}
