package main

import (
	"context"
	"strings"
	"testing"

	"elida/internal/config"
	"elida/internal/correlation"
	"elida/internal/policy"
)

// Plan B Task 9 (D14): the runner's shadow correlator is built from the
// same correlation block as the policy engine's correlator, but it is a
// separate engine, and it exists only when correlation is enabled and the
// policy engine runs.
func TestShadowCorrelator_FollowsCorrelationConfig(t *testing.T) {
	quietLogs(t)

	on := &app{cfg: correlationTestConfig(t)}
	if err := on.setupPolicyEngine(); err != nil {
		t.Fatalf("setupPolicyEngine: %v", err)
	}
	sc, err := on.shadowCorrelator()
	if err != nil || sc == nil {
		t.Fatalf("an enabled correlation block must give the runner a shadow correlator (D14), got (%v, %v)", sc, err)
	}
	if got, want := len(sc.Rules()), len(on.cfg.Correlation.Rules); got != want {
		t.Fatalf("the shadow correlator has %d rules, the correlation block %d", got, want)
	}
	// A separate engine: shadow evidence never reaches the policy
	// correlator, so a shadow match can never become a violation.
	sc.Observe("s", []correlation.Event{{ID: "dec_1", Category: "injection_elevated", Group: "tool:0"}})
	if sc.Stats().EventsObserved != 1 {
		t.Fatalf("fixture: the shadow correlator must observe the event: %+v", sc.Stats())
	}
	st, ok := on.policyEngine.Stats()["correlation"].(policy.CorrelationStats)
	if !ok {
		t.Fatal("fixture: the policy engine must run a correlator")
	}
	if st.EventsObserved != 0 {
		t.Fatal("the shadow correlator must not share state with the policy correlator")
	}

	offCfg := correlationTestConfig(t)
	offCfg.Correlation.Enabled = false
	off := &app{cfg: offCfg}
	if err := off.setupPolicyEngine(); err != nil {
		t.Fatalf("setupPolicyEngine: %v", err)
	}
	if c, err := off.shadowCorrelator(); c != nil || err != nil {
		t.Fatalf("correlation disabled: want no correlator, got (%v, %v)", c, err)
	}

	// Correlation needs the policy engine (setupPolicyEngine warns that it
	// is inactive), and the shadow correlator follows that.
	noPolicyCfg := correlationTestConfig(t)
	noPolicyCfg.Policy.Enabled = false
	noPolicy := &app{cfg: noPolicyCfg}
	if err := noPolicy.setupPolicyEngine(); err != nil {
		t.Fatalf("setupPolicyEngine: %v", err)
	}
	if c, err := noPolicy.shadowCorrelator(); c != nil || err != nil {
		t.Fatalf("policy disabled: want no correlator, got (%v, %v)", c, err)
	}
}

// setupDecision builds the shadow correlator only when correlation is
// enabled (the end-to-end test proves the runner uses it).
func TestShadowCorrelator_BuiltBySetupDecisionOnlyWhenEnabled(t *testing.T) {
	quietLogs(t)
	for _, enabled := range []bool{true, false} {
		cfg := decisionTestConfig(t)
		cfg.Policy.Enabled = true
		cfg.Correlation.Enabled = enabled
		a := newDecisionApp(t, cfg, &gate{})
		a.initSessionStore()
		if err := a.setupPolicyEngine(); err != nil {
			t.Fatalf("setupPolicyEngine: %v", err)
		}
		if err := a.setupDecision(context.Background()); err != nil {
			t.Fatalf("setupDecision: %v", err)
		}
		if got := a.shadowCorr != nil; got != enabled {
			t.Errorf("correlation enabled=%v: shadow correlator built=%v", enabled, got)
		}
		a.shutdownDecision(context.Background())
	}
}

// One startup summary line: rule count, shapes, mode per rule, the shadow
// correlator, the taxonomy and the tool-lane bounds, with no content and
// no patterns.
func TestCorrelationWiring_StartupSummary(t *testing.T) {
	const pattern = `zzsecretpatternzz`
	logs := captureLogs(t)
	cfg := decisionTestConfig(t)
	cfg.Policy.Enabled = true
	cfg.Correlation.Enabled = true
	cfg.Correlation.Rules = append(config.DefaultCorrelationRules(), correlation.Rule{
		Name: "cred_then_send", Shape: correlation.ShapeSequence,
		Sequence: []string{"credential_discovery", "external_transmission"},
		Window:   config.DefaultCorrelationRules()[0].Window, Mode: correlation.ModeEnforce,
	})
	cfg.Correlation.ToolCategories = []correlation.ToolCategoryRule{
		{Category: "credential_discovery", Tools: []string{"read_file"}, ArgumentPatterns: []string{pattern}},
		{Category: "external_transmission", Tools: []string{"http_*"}},
	}
	a := newDecisionApp(t, cfg, &gate{})
	a.initSessionStore()
	if err := a.setupPolicyEngine(); err != nil {
		t.Fatalf("setupPolicyEngine: %v", err)
	}
	if err := a.setupDecision(context.Background()); err != nil {
		t.Fatalf("setupDecision: %v", err)
	}
	t.Cleanup(func() { a.shutdownDecision(context.Background()) })
	a.logCorrelationSummary()

	var line string
	for _, l := range strings.Split(logs.String(), "\n") {
		if strings.Contains(l, `msg="temporal correlation enabled"`) {
			if line != "" {
				t.Fatalf("want one summary line, got several:\n%s", logs.String())
			}
			line = l
		}
	}
	if line == "" {
		t.Fatalf("no summary line:\n%s", logs.String())
	}
	for _, want := range []string{
		"rules=2", "repeated_rules=1", "sequence_rules=1",
		"rule_modes=slow_burn_injection:audit,cred_then_send:enforce",
		"max_history_events=64", "max_sessions=4096",
		"shadow_correlator=true",
		"tool_categories=2", "taxonomy_instructions=", "max_argument_bytes=16384", "taxonomy_warnings=0",
		"tool_lane_workers=", "tool_queue_per_worker=", "tool_batches_per_session=",
		"tool_queue_bytes=", "tool_calls_per_batch=", "tool_call_ids_per_session=",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("summary lacks %q:\n%s", want, line)
		}
	}
	if strings.Contains(logs.String(), pattern) {
		t.Errorf("startup logs must not echo argument patterns:\n%s", logs.String())
	}

	// Disabled: one line saying so, and no summary.
	logs = captureLogs(t)
	off := &app{cfg: correlationTestConfig(t)}
	off.cfg.Correlation.Enabled = false
	off.logCorrelationSummary()
	if out := logs.String(); !strings.Contains(out, `msg="temporal correlation disabled"`) || strings.Contains(out, "rules=") {
		t.Fatalf("disabled correlation: %s", out)
	}
}
