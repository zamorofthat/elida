package main

import (
	"context"
	"strings"
	"testing"

	"elida/internal/correlation"
)

var builtinCategories = []string{"credential_discovery", "sensitive_access", "external_transmission"}

// summaryLine returns the "temporal correlation enabled" line of out.
func summaryLine(out string) string {
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, `msg="temporal correlation enabled"`) {
			line = l
		}
	}
	return line
}

// Task 10: with builtin_chains the policy engine's correlator wants the
// built-in categories, its taxonomy carries them, and the startup summary
// says so.
func TestBuiltinChains_PolicyEngineWiring(t *testing.T) {
	logs := captureLogs(t)
	cfg := correlationTestConfig(t)
	cfg.Correlation.BuiltinChains = true
	a := &app{cfg: cfg}
	if err := a.setupPolicyEngine(); err != nil {
		t.Fatalf("setupPolicyEngine: %v", err)
	}
	rules, tx := a.policyEngine.CorrelationSetup()
	names := make(map[string]bool, len(rules))
	for _, r := range rules {
		names[r.Name] = true
		if r.Mode != correlation.ModeAudit {
			t.Errorf("shipped rule %q must be audit, got %q", r.Name, r.Mode)
		}
	}
	for _, want := range []string{"slow_burn_injection", "builtin_credential_exfiltration", "builtin_sensitive_exfiltration"} {
		if !names[want] {
			t.Errorf("policy correlator lacks rule %q: %v", want, names)
		}
	}
	if tx == nil {
		t.Fatal("the built-in taxonomy must reach the policy engine")
	}
	for _, cat := range builtinCategories {
		if got := tx.Categories(); !strings.Contains(strings.Join(got, ","), cat) {
			t.Errorf("taxonomy lacks %q: %v", cat, got)
		}
	}
	if strings.Contains(logs.String(), "taxonomy is expensive") {
		t.Errorf("the built-in taxonomy must not warn:\n%s", logs.String())
	}
	a.logCorrelationSummary()
	line := summaryLine(logs.String())
	for _, want := range []string{"builtin_chains=true", "rules=3", "sequence_rules=2", "repeated_rules=1", "tool_categories=4", "policy_correlation=true"} {
		if !strings.Contains(line, want) {
			t.Errorf("summary lacks %q:\n%s", want, line)
		}
	}
	for _, pat := range []string{`\.aws`, "curl", "/etc/shadow"} {
		if strings.Contains(logs.String(), pat) {
			t.Errorf("startup logs must not echo the built-in patterns (%q):\n%s", pat, logs.String())
		}
	}
}

// Default behavior unchanged: the summary says builtin_chains=false and
// no built-in category is wanted.
func TestBuiltinChains_OffByDefaultWiring(t *testing.T) {
	logs := captureLogs(t)
	cfg := correlationTestConfig(t)
	a := &app{cfg: cfg}
	if err := a.setupPolicyEngine(); err != nil {
		t.Fatalf("setupPolicyEngine: %v", err)
	}
	rules, tx := a.policyEngine.CorrelationSetup()
	if len(rules) != 1 || tx != nil {
		t.Fatalf("default: one rule and no taxonomy, got %d rules, taxonomy %v", len(rules), tx)
	}
	a.logCorrelationSummary()
	line := summaryLine(logs.String())
	for _, want := range []string{"builtin_chains=false", "rules=1", "tool_categories=0"} {
		if !strings.Contains(line, want) {
			t.Errorf("summary lacks %q:\n%s", want, line)
		}
	}
}

// The shadow correlator is built from the same effective rules.
func TestBuiltinChains_ShadowCorrelatorWiring(t *testing.T) {
	logs := captureLogs(t)
	cfg := decisionTestConfig(t)
	cfg.Policy.Enabled = false
	cfg.Correlation.Enabled = true
	cfg.Correlation.BuiltinChains = true
	a := newDecisionApp(t, cfg, &gate{})
	a.initSessionStore()
	if err := a.setupPolicyEngine(); err != nil {
		t.Fatalf("setupPolicyEngine: %v", err)
	}
	if err := a.setupDecision(context.Background()); err != nil {
		t.Fatalf("setupDecision: %v", err)
	}
	t.Cleanup(func() { a.shutdownDecision(context.Background()) })
	if a.shadowCorr == nil {
		t.Fatal("setupDecision must build the shadow correlator")
	}
	for _, cat := range builtinCategories {
		if !a.shadowCorr.Wants(cat) {
			t.Errorf("shadow correlator must want %q", cat)
		}
	}
	a.logCorrelationSummary()
	line := summaryLine(logs.String())
	for _, want := range []string{"builtin_chains=true", "rules=3", "policy_correlation=false", "shadow_correlator=true"} {
		if !strings.Contains(line, want) {
			t.Errorf("summary lacks %q:\n%s", want, line)
		}
	}
}
