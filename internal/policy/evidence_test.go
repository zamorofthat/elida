package policy

import (
	"regexp"
	"testing"
	"time"
)

func TestEventIDFor(t *testing.T) {
	v := Violation{
		RuleName:   "semantic_injection",
		SourceRole: "user",
		Timestamp:  time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC),
		Semantic:   &SemanticEvidence{DecisionID: "dec_abc"},
	}
	a := eventIDFor("sess-1", v)
	if !regexp.MustCompile(`^ev_[0-9a-f]{32}$`).MatchString(a) {
		t.Fatalf("eventIDFor = %q, want ev_ + 32 hex chars", a)
	}
	// Stable.
	if b := eventIDFor("sess-1", v); b != a {
		t.Fatalf("eventIDFor is not stable: %q != %q", a, b)
	}
	// A semantic violation's identity is its decision ID: the same decision
	// arriving twice is one event.
	v2 := v
	v2.Timestamp = v.Timestamp.Add(time.Hour)
	if b := eventIDFor("sess-1", v2); b != a {
		t.Fatalf("a different timestamp must not change a semantic event's ID: %q != %q", b, a)
	}
	// Different session, different event.
	if b := eventIDFor("sess-2", v); b == a {
		t.Fatal("a different session must produce a different event ID")
	}
	// Different decision, different event.
	v3 := v
	v3.Semantic = &SemanticEvidence{DecisionID: "dec_xyz"}
	if b := eventIDFor("sess-1", v3); b == a {
		t.Fatal("a different decision must produce a different event ID")
	}
}

func TestEventIDFor_NonSemanticUsesRuleAndTime(t *testing.T) {
	// A regex violation has no decision ID, so its identity is rule, role
	// and timestamp: two firings of the same rule at different moments are
	// two events, which is what cumulative risk depends on.
	base := Violation{
		RuleName:   "prompt_injection",
		SourceRole: "user",
		Timestamp:  time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC),
	}
	a := eventIDFor("sess-1", base)
	later := base
	later.Timestamp = base.Timestamp.Add(time.Second)
	if b := eventIDFor("sess-1", later); b == a {
		t.Fatal("two firings at different times must be two events")
	}
	same := base
	if b := eventIDFor("sess-1", same); b != a {
		t.Fatal("the same firing must produce the same ID")
	}
}

func TestCalculateRiskScore_SkipsEvidenceOnly(t *testing.T) {
	e := NewEngine(Config{Enabled: true, Mode: "enforce", RiskLadder: RiskLadderConfig{Enabled: true}})
	now := time.Now()
	fs := &FlaggedSession{
		SessionID: "s",
		ViolationEvents: []ViolationEvent{
			{RuleName: "a", Severity: SeverityWarning, SourceRole: "user", Timestamp: now},
			{RuleName: "b", Severity: SeverityCritical, SourceRole: "user", Timestamp: now, EvidenceOnly: true},
			{RuleName: "c", Severity: SeverityCritical, SourceRole: "user", Timestamp: now, EvidenceOnly: true},
		},
	}
	got := e.calculateRiskScore(fs)
	// Only the warning/user event counts: 3.0 x 1.0 x ~1.0.
	if got < 2.9 || got > 3.1 {
		t.Fatalf("calculateRiskScore = %v, want ~3.0 (evidence-only events must be skipped)", got)
	}
}

func TestScoreAt_SkipsEvidenceOnly(t *testing.T) {
	e := NewEngine(Config{Enabled: true, Mode: "enforce", RiskLadder: RiskLadderConfig{Enabled: true}})
	now := time.Now()
	events := []ViolationEvent{
		{RuleName: "a", Severity: SeverityWarning, SourceRole: "user", Timestamp: now},
		{RuleName: "b", Severity: SeverityCritical, SourceRole: "user", Timestamp: now, EvidenceOnly: true},
	}
	got := e.scoreAt(events, now)
	if got < 2.9 || got > 3.1 {
		t.Fatalf("scoreAt = %v, want ~3.0 (evidence-only events must be skipped here too)", got)
	}
}
