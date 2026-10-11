package unit

import (
	"fmt"
	"sync"
	"testing"

	"elida/internal/policy"
)

func recordFor(t *testing.T, fs *policy.FlaggedSession, eventID string) policy.SemanticDecision {
	t.Helper()
	for _, d := range fs.SemanticDecisions {
		if d.EventID == eventID {
			return d
		}
	}
	t.Fatalf("no SemanticDecision with EventID %q in %+v", eventID, fs.SemanticDecisions)
	return policy.SemanticDecision{}
}

func hasDecision(fs *policy.FlaggedSession, decisionID string) bool {
	for _, d := range fs.SemanticDecisions {
		if d.Evidence.DecisionID == decisionID {
			return true
		}
	}
	return false
}

func TestSemanticDecisions_OneRecordPerAcceptedDecision(t *testing.T) {
	e := newRiskLadderEngine(baselineRules(), baselineThresholds())
	e.RecordSemanticViolation("sess-sd", decisionViolation("semantic_injection", policy.SeverityWarning, false, 0.6, "dec_a", 1, "first"))
	e.RecordSemanticViolation("sess-sd", decisionViolation("semantic_injection", policy.SeverityCritical, true, 0.95, "dec_b", 2, "second"))
	// A redelivered decision is refused and must add no record.
	e.RecordSemanticViolation("sess-sd", decisionViolation("semantic_injection", policy.SeverityWarning, false, 0.6, "dec_a", 1, "first"))

	fs := e.GetFlaggedSession("sess-sd")
	if len(fs.SemanticDecisions) != 2 {
		t.Fatalf("records = %d, want 2", len(fs.SemanticDecisions))
	}
	if len(fs.ViolationEvents) != 2 {
		t.Fatalf("events = %d, want 2", len(fs.ViolationEvents))
	}
	for i := range fs.ViolationEvents {
		if fs.SemanticDecisions[i].EventID != fs.ViolationEvents[i].EventID {
			t.Fatalf("record %d EventID %q != event EventID %q", i, fs.SemanticDecisions[i].EventID, fs.ViolationEvents[i].EventID)
		}
	}
	a, b := fs.SemanticDecisions[0], fs.SemanticDecisions[1]
	if a.Evidence.DecisionID != "dec_a" || a.Severity != policy.SeverityWarning || a.MessageIndex != 1 || a.EvidenceOnly || a.WouldContributePoints != 0 {
		t.Fatalf("first record = %+v", a)
	}
	if b.Evidence.DecisionID != "dec_b" || !b.EvidenceOnly || !approxEqual(b.WouldContributePoints, 10.0, 0.001) {
		t.Fatalf("second record = %+v (critical x user = 10 would-contribute points)", b)
	}
	if a.RuleName != "semantic_injection" || a.EventCategory != "semantic_injection" || a.SourceRole != "user" {
		t.Fatalf("identity fields lost: %+v", a)
	}
	if a.EffectiveSeverity == "" || a.Timestamp.IsZero() {
		t.Fatalf("effective severity and timestamp must be set: %+v", a)
	}
	if a.Evidence.Probability != 0.6 || a.Evidence.Model != "minilm-multihead" || a.Evidence.ThresholdSet != "v1" {
		t.Fatalf("evidence lost: %+v", a.Evidence)
	}
	if fs.SemanticDecisionsDropped != 0 {
		t.Fatalf("dropped = %d, want 0", fs.SemanticDecisionsDropped)
	}
}

func TestSemanticDecisions_RegexViolationsRecordNone(t *testing.T) {
	e := newRiskLadderEngine(baselineRules(), baselineThresholds())
	e.EvaluateMessages("sess-sd-regex", []policy.MessageToScan{{Role: "user", Index: 0, Content: "zzmarkerzz"}})
	if fs := e.GetFlaggedSession("sess-sd-regex"); len(fs.SemanticDecisions) != 0 {
		t.Fatalf("a regex violation must not create a semantic record: %+v", fs.SemanticDecisions)
	}
}

func TestSemanticDecisions_BoundKeepsContributingDecisions(t *testing.T) {
	e := newRiskLadderEngine(baselineRules(), baselineThresholds())
	e.RecordSemanticViolation("sess-sd-bound", decisionViolation("semantic_injection", policy.SeverityInfo, false, 0.55, "dec_keep", 0, "kept"))
	extra := 44
	total := policy.MaxSemanticDecisionsPerSession + extra
	for i := 0; i < total; i++ {
		e.RecordSemanticViolation("sess-sd-bound", decisionViolation("injection_elevated", policy.SeverityInfo, true, 0.35, fmt.Sprintf("dec_ev_%d", i), i+1, "elevated"))
	}

	fs := e.GetFlaggedSession("sess-sd-bound")
	if len(fs.SemanticDecisions) != policy.MaxSemanticDecisionsPerSession {
		t.Fatalf("records = %d, want the cap %d", len(fs.SemanticDecisions), policy.MaxSemanticDecisionsPerSession)
	}
	if !hasDecision(fs, "dec_keep") {
		t.Fatal("the contributing decision must survive eviction: it is what enforcement must explain")
	}
	if want := extra + 1; fs.SemanticDecisionsDropped != want {
		t.Fatalf("dropped = %d, want %d", fs.SemanticDecisionsDropped, want)
	}
	if hasDecision(fs, "dec_ev_0") {
		t.Fatal("the oldest evidence-only record should have been evicted first")
	}
	if !hasDecision(fs, fmt.Sprintf("dec_ev_%d", total-1)) {
		t.Fatal("the newest record must be kept")
	}
	// The event list is not bounded by this task: scoring reads it.
	if len(fs.ViolationEvents) != total+1 {
		t.Fatalf("events = %d, want %d (the record bound must not touch events)", len(fs.ViolationEvents), total+1)
	}
}

func TestSemanticDecisions_EvidenceDroppedWhenFullOfContributing(t *testing.T) {
	e := newRiskLadderEngine(baselineRules(), baselineThresholds())
	for i := 0; i < policy.MaxSemanticDecisionsPerSession; i++ {
		e.RecordSemanticViolation("sess-sd-full", decisionViolation("semantic_injection", policy.SeverityInfo, false, 0.55, fmt.Sprintf("dec_c_%d", i), i, "c"))
	}
	e.RecordSemanticViolation("sess-sd-full", decisionViolation("injection_elevated", policy.SeverityInfo, true, 0.35, "dec_late_ev", 999, "ev"))

	fs := e.GetFlaggedSession("sess-sd-full")
	if hasDecision(fs, "dec_late_ev") {
		t.Fatal("an evidence-only record must not evict a contributing one")
	}
	if fs.SemanticDecisionsDropped != 1 || len(fs.SemanticDecisions) != policy.MaxSemanticDecisionsPerSession {
		t.Fatalf("dropped=%d len=%d, want 1 and the cap", fs.SemanticDecisionsDropped, len(fs.SemanticDecisions))
	}

	e.RecordSemanticViolation("sess-sd-full", decisionViolation("semantic_injection", policy.SeverityInfo, false, 0.55, "dec_c_new", 1000, "c"))
	fs = e.GetFlaggedSession("sess-sd-full")
	if hasDecision(fs, "dec_c_0") || !hasDecision(fs, "dec_c_new") {
		t.Fatal("with only contributing records, the oldest one makes room for a new contributing record")
	}
	if fs.SemanticDecisionsDropped != 2 {
		t.Fatalf("dropped = %d, want 2", fs.SemanticDecisionsDropped)
	}
}

// Replaces TestBaselineC1_MergedSemanticViolation_MixesFirstAndLatest.
func TestSemanticDecisions_MergedViolationTakesTheMoreSevereDecision(t *testing.T) {
	e := newRiskLadderEngine(baselineRules(), baselineThresholds())
	e.RecordSemanticViolation("sess-sd-sev", decisionViolation("semantic_injection", policy.SeverityWarning, false, 0.6, "dec_a", 1, "first"))
	e.RecordSemanticViolation("sess-sd-sev", decisionViolation("semantic_injection", policy.SeverityCritical, false, 0.9, "dec_b", 2, "second"))

	fs := e.GetFlaggedSession("sess-sd-sev")
	v := fs.Violations[0]
	if v.Severity != policy.SeverityCritical || v.Description != "second" || v.MessageIndex != 2 || v.Semantic.DecisionID != "dec_b" {
		t.Fatalf("entry must describe the critical decision wholesale: %+v / %+v", v, v.Semantic)
	}
	if v.EventID != fs.ViolationEvents[1].EventID {
		t.Fatalf("entry EventID %q, want the critical decision's %q", v.EventID, fs.ViolationEvents[1].EventID)
	}
	if fs.MaxSeverity != policy.SeverityCritical {
		t.Fatalf("MaxSeverity = %q, want critical (D1 side effect)", fs.MaxSeverity)
	}
}

func TestSemanticDecisions_MergedViolationKeepsAMoreSevereEarlierDecision(t *testing.T) {
	e := newRiskLadderEngine(baselineRules(), baselineThresholds())
	e.RecordSemanticViolation("sess-sd-keep", decisionViolation("semantic_injection", policy.SeverityCritical, false, 0.9, "dec_a", 1, "first"))
	e.RecordSemanticViolation("sess-sd-keep", decisionViolation("semantic_injection", policy.SeverityWarning, false, 0.6, "dec_b", 2, "second"))

	fs := e.GetFlaggedSession("sess-sd-keep")
	v := fs.Violations[0]
	if v.Severity != policy.SeverityCritical || v.Description != "first" || v.Semantic.DecisionID != "dec_a" || v.EventID != fs.ViolationEvents[0].EventID {
		t.Fatalf("entry must keep the critical decision: %+v / %+v", v, v.Semantic)
	}
	if v.Timestamp.Before(fs.ViolationEvents[1].Timestamp) {
		t.Fatalf("Timestamp must still be the last firing: %v < %v", v.Timestamp, fs.ViolationEvents[1].Timestamp)
	}
}

func TestSemanticDecisions_ContributingOutranksEvidence(t *testing.T) {
	e := newRiskLadderEngine(baselineRules(), baselineThresholds())
	e.RecordSemanticViolation("sess-sd-ev", decisionViolation("semantic_injection", policy.SeverityCritical, true, 0.95, "dec_a", 1, "first"))
	e.RecordSemanticViolation("sess-sd-ev", decisionViolation("semantic_injection", policy.SeverityWarning, false, 0.6, "dec_b", 2, "second"))

	v := e.GetFlaggedSession("sess-sd-ev").Violations[0]
	if v.EvidenceOnly || v.Severity != policy.SeverityWarning || v.Description != "second" || v.Semantic.DecisionID != "dec_b" || v.WouldContributePoints != 0 {
		t.Fatalf("a contributing decision outranks a more severe evidence-only one: %+v", v)
	}
}

func TestSemanticDecisions_ProbabilityBreaksTiesAndTheEarlierWinsAnExactTie(t *testing.T) {
	e := newRiskLadderEngine(baselineRules(), baselineThresholds())
	e.RecordSemanticViolation("sess-sd-tie", decisionViolation("semantic_injection", policy.SeverityWarning, false, 0.6, "dec_a", 1, "a"))
	e.RecordSemanticViolation("sess-sd-tie", decisionViolation("semantic_injection", policy.SeverityWarning, false, 0.7, "dec_b", 2, "b"))
	e.RecordSemanticViolation("sess-sd-tie", decisionViolation("semantic_injection", policy.SeverityWarning, false, 0.7, "dec_c", 3, "c"))

	if got := e.GetFlaggedSession("sess-sd-tie").Violations[0].Semantic.DecisionID; got != "dec_b" {
		t.Fatalf("representative = %s, want dec_b (higher probability, then earlier)", got)
	}
}

// The property behind D1: whatever the sequence, the display entry's
// decision fields all come from the one stored decision its EventID names.
func TestSemanticDecisions_EveryDisplayFieldComesFromOneDecision(t *testing.T) {
	e := newRiskLadderEngine(baselineRules(), baselineThresholds())
	seq := []struct {
		sev  policy.Severity
		ev   bool
		p    float64
		role string
	}{
		{policy.SeverityInfo, true, 0.4, "tool"},
		{policy.SeverityWarning, false, 0.55, "user"},
		{policy.SeverityCritical, true, 0.97, "tool"},
		{policy.SeverityWarning, false, 0.61, "tool"},
		{policy.SeverityInfo, false, 0.52, "user"},
	}
	for i, s := range seq {
		v := decisionViolation("semantic_injection", s.sev, s.ev, s.p, fmt.Sprintf("dec_%d", i), i, fmt.Sprintf("desc_%d", i))
		v.SourceRole = s.role
		v.EffectiveSeverity = ""
		e.RecordSemanticViolation("sess-sd-prop", v)

		fs := e.GetFlaggedSession("sess-sd-prop")
		got := fs.Violations[0]
		d := recordFor(t, fs, got.EventID)
		if got.Severity != d.Severity || got.EffectiveSeverity != d.EffectiveSeverity || got.SourceRole != d.SourceRole ||
			got.MessageIndex != d.MessageIndex || got.EvidenceOnly != d.EvidenceOnly ||
			got.WouldContributePoints != d.WouldContributePoints || got.Semantic == nil || *got.Semantic != d.Evidence {
			t.Fatalf("step %d: display entry %+v / %+v does not match its decision %+v", i, got, got.Semantic, d)
		}
		if got.Description != fmt.Sprintf("desc_%d", d.MessageIndex) {
			t.Fatalf("step %d: description %q is not the representative decision's", i, got.Description)
		}
	}
}

func TestSemanticDecisions_GetFlaggedSessionReturnsACopy(t *testing.T) {
	e := newRiskLadderEngine(baselineRules(), baselineThresholds())
	e.RecordSemanticViolation("sess-sd-copy", decisionViolation("semantic_injection", policy.SeverityWarning, false, 0.6, "dec_a", 1, "a"))
	fs := e.GetFlaggedSession("sess-sd-copy")
	fs.SemanticDecisions[0].Evidence.DecisionID = "tampered"
	if got := e.GetFlaggedSession("sess-sd-copy").SemanticDecisions[0].Evidence.DecisionID; got != "dec_a" {
		t.Fatalf("the engine's record was changed through a returned copy: %q", got)
	}
}

func TestSemanticDecisions_ReleaseDropsThemAndTheRetainedEntryIsSlim(t *testing.T) {
	e := newRiskLadderEngine(baselineRules(), baselineThresholds())
	// 4 criticals from user content: ~40 points, past block (30).
	for i := 0; i < 4; i++ {
		e.RecordSemanticViolation("sess-sd-ret", decisionViolation("semantic_injection", policy.SeverityCritical, false, 0.9, fmt.Sprintf("dec_%d", i), i, "c"))
	}
	if _, action, _ := e.GetSessionRiskScore("sess-sd-ret"); action != string(policy.ActionBlock) {
		t.Fatalf("fixture: action = %q, want block", action)
	}
	if !e.ReleaseFlaggedSession("sess-sd-ret") {
		t.Fatal("fixture: a block session is retained")
	}
	fs := e.GetFlaggedSession("sess-sd-ret")
	if fs == nil || len(fs.SemanticDecisions) != 0 || fs.SemanticDecisionsDropped != 0 {
		t.Fatalf("a retained slim entry must drop the decision list: %+v", fs)
	}
	if len(fs.Violations) != 1 || fs.Violations[0].Semantic == nil {
		t.Fatalf("the slim entry keeps its content-free display violation: %+v", fs.Violations)
	}
}

func TestSemanticDecisions_ConcurrentRecordAndRead(t *testing.T) {
	e := newRiskLadderEngine(baselineRules(), baselineThresholds())
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(2)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				e.RecordSemanticViolation("sess-sd-race", decisionViolation("injection_elevated", policy.SeverityInfo, true, 0.35, fmt.Sprintf("dec_%d_%d", w, i), i, "ev"))
			}
		}(w)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if fs := e.GetFlaggedSession("sess-sd-race"); fs != nil {
					_ = len(fs.SemanticDecisions)
				}
			}
		}()
	}
	wg.Wait()
	fs := e.GetFlaggedSession("sess-sd-race")
	if len(fs.SemanticDecisions) != policy.MaxSemanticDecisionsPerSession || fs.SemanticDecisionsDropped != 400-policy.MaxSemanticDecisionsPerSession {
		t.Fatalf("len=%d dropped=%d", len(fs.SemanticDecisions), fs.SemanticDecisionsDropped)
	}
}
