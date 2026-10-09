package policy

import (
	"fmt"
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
	a := eventIDFor("sess-1", 0, v)
	if !regexp.MustCompile(`^ev_[0-9a-f]{32}$`).MatchString(a) {
		t.Fatalf("eventIDFor = %q, want ev_ + 32 hex chars", a)
	}
	// Stable.
	if b := eventIDFor("sess-1", 0, v); b != a {
		t.Fatalf("eventIDFor is not stable: %q != %q", a, b)
	}
	// A semantic violation's identity is its decision ID: the same decision
	// arriving twice is one event.
	v2 := v
	v2.Timestamp = v.Timestamp.Add(time.Hour)
	if b := eventIDFor("sess-1", 0, v2); b != a {
		t.Fatalf("a different timestamp must not change a semantic event's ID: %q != %q", b, a)
	}
	// Different session, different event.
	if b := eventIDFor("sess-2", 0, v); b == a {
		t.Fatal("a different session must produce a different event ID")
	}
	// Different decision, different event.
	v3 := v
	v3.Semantic = &SemanticEvidence{DecisionID: "dec_xyz"}
	if b := eventIDFor("sess-1", 0, v3); b == a {
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
	a := eventIDFor("sess-1", 0, base)
	later := base
	later.Timestamp = base.Timestamp.Add(time.Second)
	if b := eventIDFor("sess-1", 0, later); b == a {
		t.Fatal("two firings at different times must be two events")
	}
	same := base
	if b := eventIDFor("sess-1", 0, same); b != a {
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

func TestEventIDFor_SequenceSeparatesSameMicrosecondFirings(t *testing.T) {
	// Review M1: on darwin, wall-clock time has ~1µs resolution, so two
	// firings of one rule from one role can share a timestamp. The per-rule
	// ordinal tells them apart.
	v := Violation{
		RuleName:   "prompt_injection",
		SourceRole: "user",
		Timestamp:  time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC),
	}
	if eventIDFor("sess-1", 0, v) == eventIDFor("sess-1", 1, v) {
		t.Fatal("two firings with the same timestamp but different ordinals must be two events")
	}
	// A semantic event's identity is its DecisionID alone: the ordinal
	// must not change it, or a replay would not be recognized.
	v.Semantic = &SemanticEvidence{DecisionID: "dec_abc"}
	if eventIDFor("sess-1", 0, v) != eventIDFor("sess-1", 7, v) {
		t.Fatal("the ordinal must not change a semantic event's ID")
	}
}

func TestRecordViolations_TenThousandSameTimestampEventsGetDistinctIDs(t *testing.T) {
	e := NewEngine(Config{Enabled: true, Mode: "enforce", RiskLadder: RiskLadderConfig{Enabled: true}})
	ts := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	batch := make([]Violation, 10000)
	for i := range batch {
		batch[i] = Violation{RuleName: "prompt_injection", Severity: SeverityWarning, SourceRole: "user", Timestamp: ts}
	}
	e.recordViolations("sess-10k", batch)

	fs := e.GetFlaggedSession("sess-10k")
	if len(fs.ViolationEvents) != 10000 {
		t.Fatalf("expected 10000 events, got %d", len(fs.ViolationEvents))
	}
	ids := make(map[string]struct{}, 10000)
	for _, ev := range fs.ViolationEvents {
		ids[ev.EventID] = struct{}{}
	}
	if len(ids) != 10000 {
		t.Fatalf("expected 10000 distinct event IDs, got %d", len(ids))
	}
}

func TestSeenDecisionIDs_CapEvictsOldest(t *testing.T) {
	e := NewEngine(Config{Enabled: true, Mode: "enforce", RiskLadder: RiskLadderConfig{Enabled: true}})
	sem := func(id string) Violation {
		return Violation{
			RuleName: "injection_elevated", Severity: SeverityInfo, SourceRole: "user",
			EvidenceOnly: true, Semantic: &SemanticEvidence{DecisionID: id},
		}
	}
	// One more than the cap: dec_0 is evicted.
	for i := 0; i <= maxSeenDecisionIDsPerSession; i++ {
		e.RecordSemanticViolation("sess-cap", sem(fmt.Sprintf("dec_%d", i)))
	}
	e.mu.RLock()
	size := len(e.seenSemanticEvents["sess-cap"].ids)
	e.mu.RUnlock()
	if size != maxSeenDecisionIDsPerSession {
		t.Fatalf("seen set holds %d IDs, want the cap %d", size, maxSeenDecisionIDsPerSession)
	}
	want := maxSeenDecisionIDsPerSession + 1

	// The newest is still refused.
	e.RecordSemanticViolation("sess-cap", sem(fmt.Sprintf("dec_%d", maxSeenDecisionIDsPerSession)))
	if got := len(e.EvidenceEvents("sess-cap")); got != want {
		t.Fatalf("the newest DecisionID must still be refused: %d events, want %d", got, want)
	}
	// The oldest was evicted, so it is accepted again.
	e.RecordSemanticViolation("sess-cap", sem("dec_0"))
	if got := len(e.EvidenceEvents("sess-cap")); got != want+1 {
		t.Fatalf("the evicted oldest DecisionID should be accepted: %d events, want %d", got, want+1)
	}
}
