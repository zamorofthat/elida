package runner

import (
	"testing"

	"elida/internal/decision"
)

func testThresholds() Thresholds {
	return Thresholds{Main: 0.5, Aux: 0.64, Elevated: 0.3, Warning: 0.5, Critical: 0.8}
}

func TestSeverityFor(t *testing.T) {
	th := testThresholds()
	cases := []struct {
		p    float64
		want string
	}{
		{0.00, ""},
		{0.29, ""},
		{0.30, "info"},
		{0.49, "info"},
		{0.50, "warning"},
		{0.79, "warning"},
		{0.80, "critical"},
		{1.00, "critical"},
	}
	for _, tc := range cases {
		if got := severityFor(tc.p, th); got != tc.want {
			t.Errorf("severityFor(%v) = %q, want %q", tc.p, got, tc.want)
		}
	}
}

func TestApplyVeto(t *testing.T) {
	th := testThresholds()
	cases := []struct {
		name     string
		main     float64
		aux      float64
		auxOK    bool
		wantVeto bool
	}{
		{"high main, low aux: stands", 0.9, 0.1, true, false},
		{"high main, high aux: vetoed", 0.9, 0.7, true, true},
		{"aux exactly at threshold: vetoed", 0.9, 0.64, true, true},
		{"aux just below: stands", 0.9, 0.6399, true, false},
		{"low main, high aux: nothing to veto", 0.1, 0.9, true, true},
		{"aux unanswered: no veto, main stands", 0.9, 0, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := applyVeto(tc.main, tc.aux, tc.auxOK, th); got != tc.wantVeto {
				t.Fatalf("applyVeto(%v, %v, %v) = %v, want %v", tc.main, tc.aux, tc.auxOK, got, tc.wantVeto)
			}
		})
	}
}

func TestEffectiveModeCap(t *testing.T) {
	cases := []struct {
		mode, policyMode, want string
	}{
		{"enforce", "enforce", "enforce"},
		{"enforce", "audit", "audit"},
		{"audit", "audit", "audit"},
		{"audit", "enforce", "audit"},
		{"shadow", "audit", "shadow"},
		{"shadow", "enforce", "shadow"},
		{"disabled", "enforce", "disabled"},
		{"disabled", "audit", "disabled"},
	}
	for _, tc := range cases {
		if got := capMode(tc.mode, tc.policyMode); got != tc.want {
			t.Errorf("capMode(%q, %q) = %q, want %q", tc.mode, tc.policyMode, got, tc.want)
		}
	}
}

func TestEligibleRole(t *testing.T) {
	// Phase 1 scope: request-side user content and untrusted tool results.
	// System and assistant content is trusted and not analyzed.
	for role, want := range map[string]bool{
		"user": true, "tool": true,
		"system": false, "assistant": false, "": false,
	} {
		if got := eligibleRole(role); got != want {
			t.Errorf("eligibleRole(%q) = %v, want %v", role, got, want)
		}
	}
}

func TestExecutionModeFromScope(t *testing.T) {
	if got := executionMode(decision.ScopeCurrentRequest); got != "inline" {
		t.Errorf("executionMode(current_request) = %q, want inline", got)
	}
	if got := executionMode(decision.ScopeFutureActivity); got != "async" {
		t.Errorf("executionMode(future_activity) = %q, want async", got)
	}
	if got := executionMode(decision.ScopeRemainingStream); got != "async" {
		t.Errorf("executionMode(remaining_stream) = %q, want async", got)
	}
}

func TestDescribeMentionsTheTransform(t *testing.T) {
	r := &Runner{cfg: Config{
		Thresholds: testThresholds(),
		Model:      ModelIdentity{ThresholdSet: "v1"},
	}}
	plain := r.describe(Verdict{Probability: 0.91}, RuleSemanticInjection)
	if !containsStr(plain, "original content") {
		t.Errorf("describe = %q, want it to mention the original content", plain)
	}
	derived := r.describe(Verdict{
		Probability: 0.91,
		Window:      decision.Window{Transform: "base64_decode"},
	}, RuleSemanticInjection)
	if !containsStr(derived, "base64_decode") {
		t.Errorf("describe = %q, want it to name the transform", derived)
	}
	elevated := r.describe(Verdict{Probability: 0.35}, RuleInjectionElevated)
	if !containsStr(elevated, "evidence only") {
		t.Errorf("describe = %q, want it to say the event contributes no risk", elevated)
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
