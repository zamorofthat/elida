package unit

import (
	"bytes"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"elida/internal/config"
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

// Wiring review I-2: the "correlation." prefix is reserved on the control
// API path too, not only in elida.yaml validation.
func TestCorrelationWiring_ReservedPrefix(t *testing.T) {
	for _, name := range []string{"correlation.repeated_injection", "correlation.x"} {
		if !config.ReservedRuleName(name) {
			t.Errorf("%q must be reserved", name)
		}
	}
	for _, name := range []string{"correlation_rule", "my_correlation.rule", "correlation"} {
		if config.ReservedRuleName(name) {
			t.Errorf("%q must not be reserved", name)
		}
	}
}

func TestCorrelationWiring_ControlAPIRefusesNewCorrelationRule(t *testing.T) {
	e := wiringEngine(t, "enforce", correlation.ModeAudit)
	h, ss := settingsHandler(t, e)
	w := doJSON(t, h, http.MethodPut, "/control/settings", map[string]any{"policy": map[string]any{
		"custom_rules": []map[string]any{
			{"name": "correlation.repeated_injection", "type": "content_match", "patterns": []string{"x"}, "severity": "warning"},
		},
	}})
	if w.Code != http.StatusBadRequest || !contains(w.Body.String(), "correlation.repeated_injection") {
		t.Fatalf("a new correlation.* custom rule: got %d %s, want 400 naming it", w.Code, w.Body.String())
	}
	if len(ss.GetMerged().Policy.CustomRules) != 0 {
		t.Fatal("a refused save must not be stored")
	}
	for _, r := range e.GetConfig().Rules {
		if r.Name == "correlation.repeated_injection" {
			t.Fatal("a refused rule must not reach the engine")
		}
	}
}

func TestCorrelationWiring_SavedCorrelationRuleWarnsAndIsSkipped(t *testing.T) {
	buf := captureLogs(t, slog.LevelInfo)
	const yamlBody = "policy:\n  custom_rules:\n    - name: correlation.saved\n      type: content_match\n      patterns: [\"x\"]\n      severity: warning\n"
	h, ss := staleSettingsHandler(t, yamlBody)
	got := doJSON(t, h, http.MethodGet, "/control/settings", nil)
	if w := doJSON(t, h, http.MethodPut, "/control/settings", got.Body.String()); w.Code != http.StatusOK {
		t.Fatalf("round trip with a saved correlation.* rule: %d %s", w.Code, w.Body.String())
	}
	if out := buf.String(); !contains(out, "level=WARN") || !contains(out, "rule=correlation.saved") {
		t.Fatalf("a saved correlation.* rule must WARN: %s", out)
	}
	if rules := ss.GetMerged().Policy.CustomRules; len(rules) != 1 || rules[0].Name != "correlation.saved" {
		t.Fatalf("the saved body must not be rewritten: %+v", rules)
	}
}

// kindGateEngine has a config rule sharing the correlation match's name:
// the state a reserved-name bypass would produce (an embedder constructing
// rules directly).
func kindGateEngine(t *testing.T, mode correlation.Mode) *policy.Engine {
	t.Helper()
	return policy.NewEngine(policy.Config{
		Enabled: true, Mode: "enforce",
		Rules: []policy.Rule{{
			Name: "correlation.repeated_injection", Type: policy.RuleTypeContentMatch,
			Target: policy.RuleTargetRequest, Patterns: []string{"zzmarkerzz"},
			Severity: policy.SeverityWarning, Action: "flag",
		}},
		RiskLadder: policy.RiskLadderConfig{Enabled: true, Thresholds: baselineThresholds()},
		Correlation: policy.CorrelationConfig{
			Enabled: true, Limits: okLimits(),
			Rules: []correlation.Rule{{
				Name: "repeated_injection", Shape: correlation.ShapeRepeated,
				Category: "semantic_injection", Count: 2, Window: time.Minute,
				Severity: "critical", Mode: mode,
			}},
		},
	})
}

// A correlation match never merges into a regex display entry, and a regex
// firing never merges into a correlation display entry.
func TestCorrelationWiring_KindGate(t *testing.T) {
	t.Run("regex first", func(t *testing.T) {
		// An enforce match: the plain merge would relabel the regex entry.
		e := kindGateEngine(t, correlation.ModeEnforce)
		e.EvaluateMessages("sess-kg1", []policy.MessageToScan{{Role: "user", Index: 0, Content: "zzmarkerzz"}})
		before := *correlationMatch(e.GetFlaggedSession("sess-kg1"))
		now := time.Now()
		recordCorrelationInput(e, "sess-kg1", "ev_a", now)
		recordCorrelationInput(e, "sess-kg1", "ev_b", now.Add(time.Second))
		got := correlationMatch(e.GetFlaggedSession("sess-kg1"))
		if got.EventCategory == correlation.CategoryMatch || got.EvidenceOnly || got.EventID != before.EventID {
			t.Fatalf("the regex entry absorbed the correlation match: %+v", got)
		}
	})
	t.Run("correlation first", func(t *testing.T) {
		e := kindGateEngine(t, correlation.ModeAudit)
		now := time.Now()
		recordCorrelationInput(e, "sess-kg2", "ev_a", now)
		recordCorrelationInput(e, "sess-kg2", "ev_b", now.Add(time.Second))
		before := *correlationMatch(e.GetFlaggedSession("sess-kg2"))
		if before.EventCategory != correlation.CategoryMatch {
			t.Fatalf("precondition: a correlation entry, got %+v", before)
		}
		e.EvaluateMessages("sess-kg2", []policy.MessageToScan{{Role: "user", Index: 0, Content: "zzmarkerzz"}})
		got := correlationMatch(e.GetFlaggedSession("sess-kg2"))
		if got.EventCategory != correlation.CategoryMatch || !got.EvidenceOnly || got.EventID != before.EventID {
			t.Fatalf("the correlation entry absorbed the regex firing: %+v", got)
		}
	})
}
