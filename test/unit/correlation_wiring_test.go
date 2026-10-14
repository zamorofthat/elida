package unit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
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

// Wiring review I-5 (D5): under policy.mode audit an enforce rule's match is
// evidence-only, with the would-contribute diagnostic, exactly like the
// semantic cap. The cap is read at record time: switching policy.mode to
// enforce lifts it for later matches.
func TestCorrelationWiring_PolicyAuditCapsEnforceRule(t *testing.T) {
	e := wiringEngine(t, "audit", correlation.ModeEnforce)
	now := time.Now()
	recordCorrelationInput(e, "sess-cap", "ev_a", now)
	recordCorrelationInput(e, "sess-cap", "ev_b", now.Add(time.Second))
	fs := e.GetFlaggedSession("sess-cap")
	m := correlationMatch(fs)
	if m == nil || !m.EvidenceOnly {
		t.Fatalf("policy.mode audit must cap an enforce rule to evidence-only: %+v", m)
	}
	if m.WouldContributePoints != 10 {
		t.Fatalf("would-contribute = critical x 1.0 = 10, got %v", m.WouldContributePoints)
	}
	if fs.RiskScore != 0 || fs.MaxSeverity == policy.SeverityCritical {
		t.Fatalf("a capped match must not score or raise severity: score=%v max=%s", fs.RiskScore, fs.MaxSeverity)
	}

	cfg := e.GetConfig()
	cfg.Mode = "enforce"
	e.ReloadConfig(cfg)
	recordCorrelationInput(e, "sess-cap2", "ev_c", now)
	recordCorrelationInput(e, "sess-cap2", "ev_d", now.Add(time.Second))
	fs = e.GetFlaggedSession("sess-cap2")
	if m := correlationMatch(fs); m == nil || m.EvidenceOnly || m.WouldContributePoints != 0 || fs.RiskScore < 9 {
		t.Fatalf("under policy.mode enforce the rule contributes: %+v score=%v", m, fs.RiskScore)
	}
}

// D5: one startup WARN per enforce-mode rule, naming it; none for audit.
func TestCorrelationWiring_EnforceRuleWarnsAtStartup(t *testing.T) {
	for _, tc := range []struct {
		policyMode string
		ruleMode   correlation.Mode
		want       string
	}{
		{"enforce", correlation.ModeEnforce, "contribute risk"},
		{"audit", correlation.ModeEnforce, "capped to evidence-only"},
		{"enforce", correlation.ModeAudit, ""},
	} {
		buf := captureLogs(t, slog.LevelWarn)
		wiringEngine(t, tc.policyMode, tc.ruleMode)
		out := buf.String()
		got := strings.Count(out, "rule=repeated_injection")
		if tc.want == "" {
			if got != 0 {
				t.Errorf("audit rule must not WARN:\n%s", out)
			}
			continue
		}
		if got != 1 || !contains(out, "level=WARN") || !contains(out, tc.want) {
			t.Errorf("policy %s, rule %s: want one WARN naming the rule (%q):\n%s", tc.policyMode, tc.ruleMode, tc.want, out)
		}
	}
}

// Wiring review I-4 refused shape: sequence while the engine was a stub.
// Plan B Task 3 implements sequences, so a valid sequence rule validates
// and an invalid one is still refused with the engine's own field error.
func TestCorrelationWiring_ConfigAcceptsSequenceRules(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Correlation.Enabled = true
	cfg.Correlation.Rules = []correlation.Rule{sequenceRule("ordered", time.Minute, "a_b", "c_d")}
	if res := cfg.Validate(); !res.Valid {
		t.Fatalf("a valid sequence rule must validate: %+v", res.Errors)
	}
	if err := cfg.Correlation.Validate(); err != nil {
		t.Fatalf("CorrelationConfig.Validate: %v", err)
	}

	cfg.Correlation.Rules = []correlation.Rule{sequenceRule("ordered", time.Minute, "a_b")}
	res := cfg.Validate()
	found := false
	for _, e := range res.Errors {
		if e.Field == "correlation.rules[0].sequence" {
			found = true
		}
	}
	if res.Valid || !found {
		t.Fatalf("a one-step sequence must be refused on rules[0].sequence: %+v", res.Errors)
	}
}

// A sequence match reaches the policy engine as a correlation violation
// whose evidence reports shape sequence and the steps in time order, even
// when the later step was recorded first.
func TestCorrelationWiring_SequenceMatchViolation(t *testing.T) {
	e := policy.NewEngine(policy.Config{
		Enabled: true, Mode: "enforce",
		RiskLadder: policy.RiskLadderConfig{Enabled: true, Thresholds: baselineThresholds()},
		Correlation: policy.CorrelationConfig{
			Enabled: true,
			Limits:  okLimits(),
			Rules:   []correlation.Rule{sequenceRule("ordered_injection", time.Minute, "semantic_injection", "semantic_injection")},
		},
	})
	now := time.Now()
	recordCorrelationInput(e, "sess-seq", "ev_late", now.Add(time.Second))
	recordCorrelationInput(e, "sess-seq", "ev_early", now)
	fs := e.GetFlaggedSession("sess-seq")
	var m *policy.Violation
	if fs != nil {
		for i := range fs.Violations {
			if fs.Violations[i].RuleName == "correlation.ordered_injection" {
				m = &fs.Violations[i]
			}
		}
	}
	if m == nil || m.Correlation == nil {
		t.Fatalf("want a sequence match with evidence: %+v", fs)
	}
	ev := m.Correlation
	if ev.Shape != "sequence" || ev.Rule != "ordered_injection" || ev.MatchID != m.EventID {
		t.Fatalf("evidence metadata: %+v", ev)
	}
	if len(ev.EventIDs) != 2 || ev.EventIDs[0] != "ev_early" || ev.EventIDs[1] != "ev_late" {
		t.Fatalf("steps must be in time order: %+v", ev.EventIDs)
	}
	if !m.EvidenceOnly {
		t.Fatal("an audit rule's match is evidence-only")
	}
}

// Wiring review M-3/M-2: a match carries structured evidence in
// Violation.Correlation, FrameworkRef stays empty, the description holds
// no event IDs, and the "correlation" source role has an explicit 1.0
// weight.
func TestCorrelationWiring_MatchViolationShape(t *testing.T) {
	if w, ok := policy.SourceRoleWeights["correlation"]; !ok || w != 1.0 {
		t.Fatalf("SourceRoleWeights[correlation] = %v, %v; want an explicit 1.0", w, ok)
	}
	e := wiringEngine(t, "enforce", correlation.ModeAudit)
	now := time.Now()
	recordCorrelationInput(e, "sess-shape", "ev_a", now)
	recordCorrelationInput(e, "sess-shape", "ev_b", now.Add(time.Second))
	m := correlationMatch(e.GetFlaggedSession("sess-shape"))
	if m == nil || m.Correlation == nil {
		t.Fatalf("want a match with correlation evidence: %+v", m)
	}
	ev := m.Correlation
	if ev.MatchID != m.EventID || ev.Rule != "repeated_injection" || ev.Shape != "repeated" || ev.Mode != "audit" {
		t.Fatalf("evidence metadata: %+v", ev)
	}
	if len(ev.EventIDs) != 2 || len(ev.Categories) != 2 || ev.Categories[0] != "semantic_injection" {
		t.Fatalf("contributing events: %+v", ev)
	}
	if !ev.FirstEvent.Equal(now) || !ev.LastEvent.Equal(now.Add(time.Second)) {
		t.Fatalf("window: %v..%v", ev.FirstEvent, ev.LastEvent)
	}
	if m.FrameworkRef != "" || contains(m.Description, "ev_") || m.SourceRole != "correlation" {
		t.Fatalf("FrameworkRef must stay empty and the description ID-free: %+v", m)
	}
	if m.WouldContributePoints != 10 {
		t.Fatalf("would-contribute = critical x 1.0 = 10, got %v", m.WouldContributePoints)
	}

	// GetFlaggedSession deep-copies the evidence.
	m.Correlation.EventIDs[0] = "tampered"
	if again := correlationMatch(e.GetFlaggedSession("sess-shape")); again.Correlation.EventIDs[0] == "tampered" {
		t.Fatal("GetFlaggedSession must deep-copy correlation evidence")
	}
}

// slowBurnEngine runs the shipped slow_burn_injection rule (D8) plus a
// count_by: message rule on policy_violation for role-less events.
func slowBurnEngine() *policy.Engine {
	rules := config.DefaultCorrelationRules()
	rules = append(rules, correlation.Rule{
		Name: "roleless", Shape: correlation.ShapeRepeated, Category: "policy_violation",
		Count: 3, CountBy: correlation.CountByMessage, Window: time.Minute,
	})
	return policy.NewEngine(policy.Config{
		Enabled: true, Mode: "enforce",
		Correlation: policy.CorrelationConfig{Enabled: true, Limits: okLimits(), Rules: rules},
	})
}

// recordElevated records one injection_elevated window of message idx, as
// the semantic runner does (one decision per window).
func recordElevated(e *policy.Engine, sess, decisionID string, role string, idx int, at time.Time) {
	e.RecordSemanticViolation(sess, policy.Violation{
		RuleName: "injection_elevated", EventCategory: "injection_elevated",
		Severity: policy.SeverityInfo, Action: "flag", SourceRole: role, MessageIndex: idx,
		Timestamp: at, EvidenceOnly: true,
		Semantic: &policy.SemanticEvidence{DecisionID: decisionID, Signal: "injection", Probability: 0.35},
	})
}

func hasRule(fs *policy.FlaggedSession, name string) bool {
	if fs == nil {
		return false
	}
	for _, v := range fs.Violations {
		if v.RuleName == name {
			return true
		}
	}
	return false
}

// Re-review N-1: count_by: message keys on role:index for any violation
// attributed to a message, index 0 included. One long first message with
// three elevated windows is one message, not a slow burn.
func TestCorrelationWiring_OneLongIndexZeroMessageIsNotASlowBurn(t *testing.T) {
	e := slowBurnEngine()
	now := time.Now()
	for i := 0; i < 3; i++ {
		recordElevated(e, "sess-long", fmt.Sprintf("dec_w%d", i), "user", 0, now.Add(time.Duration(i)*time.Second))
	}
	if hasRule(e.GetFlaggedSession("sess-long"), "correlation.slow_burn_injection") {
		t.Fatal("three windows of one index-0 message must not satisfy slow_burn_injection")
	}
}

func TestCorrelationWiring_ThreeMessagesFromIndexZeroAreASlowBurn(t *testing.T) {
	e := slowBurnEngine()
	now := time.Now()
	for i := 0; i < 3; i++ {
		recordElevated(e, "sess-three", fmt.Sprintf("dec_m%d", i), "user", i, now.Add(time.Duration(i)*time.Second))
	}
	if !hasRule(e.GetFlaggedSession("sess-three"), "correlation.slow_burn_injection") {
		t.Fatal("three distinct messages (indices 0, 1, 2) must satisfy slow_burn_injection")
	}
}

// A violation with no message at all (no source role, no semantic
// evidence: metric and anomaly violations) counts per event.
func TestCorrelationWiring_RolelessViolationCountsPerEvent(t *testing.T) {
	e := slowBurnEngine()
	now := time.Now()
	for i := 0; i < 3; i++ {
		e.RecordSemanticViolation("sess-roleless", policy.Violation{
			RuleName: "metric_rule", EventID: fmt.Sprintf("ev_m%d", i), EventCategory: "policy_violation",
			Severity: policy.SeverityWarning, Action: "flag", Timestamp: now.Add(time.Duration(i) * time.Second),
			EvidenceOnly: true,
		})
	}
	if !hasRule(e.GetFlaggedSession("sess-roleless"), "correlation.roleless") {
		t.Fatal("role-less violations have no message: each counts on its own")
	}
}

// Wiring review M-5 (D13): /control/flagged/stats carries correlation
// counters when a correlator is installed.
func TestCorrelationWiring_StatsKey(t *testing.T) {
	e := wiringEngine(t, "enforce", correlation.ModeAudit)
	now := time.Now()
	recordCorrelationInput(e, "sess-st", "ev_a", now)
	recordCorrelationInput(e, "sess-st", "ev_b", now.Add(time.Second))
	st, ok := e.Stats()["correlation"].(policy.CorrelationStats)
	if !ok {
		t.Fatalf("Stats must include correlation when a correlator is set: %+v", e.Stats())
	}
	if st.EventsQueued != 2 || st.EventsObserved != 2 || st.Matches != 1 || st.MatchesRecorded != 1 ||
		st.Shards == 0 || st.Rules != 1 || st.EventsDropped != 0 {
		t.Fatalf("correlation stats: %+v", st)
	}
	out, err := json.Marshal(e.Stats())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"events_queued"`, `"events_observed"`, `"matches"`, `"events_dropped"`, `"shards"`} {
		if !contains(string(out), key) {
			t.Errorf("stats JSON lacks %s: %s", key, out)
		}
	}
}

// D8: the shipped default rule set is one audit-only slow_burn_injection
// rule, and correlation stays disabled by default.
func TestCorrelationWiring_DefaultRuleSet(t *testing.T) {
	c := config.DefaultConfig().Correlation
	if c.Enabled {
		t.Fatal("correlation must stay disabled by default")
	}
	want := correlation.Rule{
		Name: "slow_burn_injection", Shape: correlation.ShapeRepeated,
		Category: "injection_elevated", Count: 3, CountBy: correlation.CountByMessage,
		Window: 15 * time.Minute, Cooldown: 15 * time.Minute,
		Severity: "warning", Mode: correlation.ModeAudit,
	}
	if len(c.Rules) != 1 || !reflect.DeepEqual(c.Rules[0], want) {
		t.Fatalf("default rules = %+v, want [%+v]", c.Rules, want)
	}
	c.Enabled = true
	if err := c.Validate(); err != nil {
		t.Fatalf("the default block must validate when enabled: %v", err)
	}
}

// configs/elida.yaml documents the correlation block and is what the
// Docker image runs: it must equal DefaultConfig.
func TestSampleConfig_CorrelationMatchesDefaults(t *testing.T) {
	cfg, err := config.Load("../../configs/elida.yaml")
	if err != nil {
		t.Fatalf("load configs/elida.yaml: %v", err)
	}
	if want := config.DefaultConfig().Correlation; !reflect.DeepEqual(cfg.Correlation, want) {
		t.Fatalf("configs/elida.yaml correlation block drifted from DefaultConfig:\n got %+v\nwant %+v", cfg.Correlation, want)
	}
}

func TestCorrelationWiring_EnvOverride(t *testing.T) {
	t.Setenv("ELIDA_CORRELATION_ENABLED", "true")
	path := writeTempConfig(t, "correlation:\n  enabled: false\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Correlation.Enabled {
		t.Fatal("ELIDA_CORRELATION_ENABLED=true must enable correlation")
	}
	t.Setenv("ELIDA_CORRELATION_ENABLED", "false")
	path = writeTempConfig(t, "correlation:\n  enabled: true\n")
	if cfg, err = config.Load(path); err != nil || cfg.Correlation.Enabled {
		t.Fatalf("ELIDA_CORRELATION_ENABLED=false must disable correlation (err %v)", err)
	}
}
