package unit

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/decision/runner"
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

func TestSemanticDecisions_GettersReturnCopies(t *testing.T) {
	getters := []struct {
		name string
		get  func(e *policy.Engine) *policy.FlaggedSession
	}{
		{"GetFlaggedSession", func(e *policy.Engine) *policy.FlaggedSession { return e.GetFlaggedSession("sess-sd-copy") }},
		{"GetFlaggedSessions", func(e *policy.Engine) *policy.FlaggedSession { return e.GetFlaggedSessions()[0] }},
		{"GetFlaggedSessionsBySeverity", func(e *policy.Engine) *policy.FlaggedSession {
			return e.GetFlaggedSessionsBySeverity(policy.SeverityInfo)[0]
		}},
	}
	for _, g := range getters {
		t.Run(g.name, func(t *testing.T) {
			e := newRiskLadderEngine(baselineRules(), baselineThresholds())
			e.RecordSemanticViolation("sess-sd-copy", decisionViolation("semantic_injection", policy.SeverityWarning, false, 0.6, "dec_a", 1, "a"))
			fs := g.get(e)
			fs.SemanticDecisions[0].Evidence.DecisionID = "tampered"
			fs.SemanticDecisions = append(fs.SemanticDecisions, policy.SemanticDecision{EventID: "extra"})
			fs.SemanticDecisionsDropped = 99
			got := e.GetFlaggedSession("sess-sd-copy")
			if len(got.SemanticDecisions) != 1 || got.SemanticDecisions[0].Evidence.DecisionID != "dec_a" || got.SemanticDecisionsDropped != 0 {
				t.Fatalf("the engine's records were changed through a copy from %s: %+v", g.name, got.SemanticDecisions)
			}
		})
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

// Review M1: the representative is ranked on source-weighted severity, so a
// critical from a trusted role (effective info) never displaces a user
// warning and MaxSeverity cannot fall.
func TestSemanticDecisions_RankingUsesEffectiveSeverity(t *testing.T) {
	systemCritical := func(id string, idx int) policy.Violation {
		v := decisionViolation("semantic_injection", policy.SeverityCritical, false, 0.95, id, idx, "system critical")
		v.SourceRole = "system"
		v.EffectiveSeverity = "" // computed: critical x system = info
		return v
	}

	e := newRiskLadderEngine(baselineRules(), baselineThresholds())
	e.RecordSemanticViolation("sess-sd-eff", decisionViolation("semantic_injection", policy.SeverityWarning, false, 0.6, "dec_user", 1, "user warning"))
	e.RecordSemanticViolation("sess-sd-eff", systemCritical("dec_sys", 2))
	fs := e.GetFlaggedSession("sess-sd-eff")
	if v := fs.Violations[0]; v.Semantic.DecisionID != "dec_user" || v.SourceRole != "user" {
		t.Fatalf("a critical/system decision (effective info) must not displace a user warning: %+v / %+v", v, v.Semantic)
	}
	if fs.MaxSeverity != policy.SeverityWarning {
		t.Fatalf("MaxSeverity = %q, want warning (it must not fall)", fs.MaxSeverity)
	}

	e.RecordSemanticViolation("sess-sd-eff2", systemCritical("dec_sys", 1))
	e.RecordSemanticViolation("sess-sd-eff2", decisionViolation("semantic_injection", policy.SeverityWarning, false, 0.6, "dec_user", 2, "user warning"))
	fs = e.GetFlaggedSession("sess-sd-eff2")
	if v := fs.Violations[0]; v.Semantic.DecisionID != "dec_user" {
		t.Fatalf("a user warning outranks a stored critical/system decision: %+v / %+v", v, v.Semantic)
	}
	if fs.MaxSeverity != policy.SeverityWarning {
		t.Fatalf("MaxSeverity = %q, want warning", fs.MaxSeverity)
	}
}

// Review M4: the old mixing path let an evidence-only critical leak into
// MaxSeverity when a contributing warning followed; the representative is
// now the contributing warning.
func TestSemanticDecisions_EvidenceCriticalThenContributingWarning(t *testing.T) {
	e := newRiskLadderEngine(baselineRules(), baselineThresholds())
	e.RecordSemanticViolation("sess-sd-m4", decisionViolation("semantic_injection", policy.SeverityCritical, true, 0.95, "dec_ev", 1, "ev critical"))
	e.RecordSemanticViolation("sess-sd-m4", decisionViolation("semantic_injection", policy.SeverityWarning, false, 0.6, "dec_c", 2, "warning"))
	fs := e.GetFlaggedSession("sess-sd-m4")
	v := fs.Violations[0]
	if v.Severity != policy.SeverityWarning || v.Description != "warning" || v.EvidenceOnly {
		t.Fatalf("entry must be the contributing warning: %+v", v)
	}
	if fs.MaxSeverity != policy.SeverityWarning {
		t.Fatalf("MaxSeverity = %q, want warning", fs.MaxSeverity)
	}
}

// collidingRules gives the engine a regex rule named like a semantic rule.
// Config validation rejects this name; the engine must still never mix the
// two kinds in one display entry.
func collidingRules() []policy.Rule {
	r := baselineRules()
	r[0].Name = "semantic_injection"
	return r
}

// Review M2, semantic onto regex: the regex entry is kept.
func TestSemanticDecisions_SemanticNeverReplacesARegexEntry(t *testing.T) {
	e := newRiskLadderEngine(collidingRules(), baselineThresholds())
	e.EvaluateMessages("sess-sd-kind", []policy.MessageToScan{{Role: "user", Index: 0, Content: "zzmarkerzz"}})
	before := e.GetFlaggedSession("sess-sd-kind").Violations[0]
	if before.Semantic != nil || before.MatchedPattern == "" {
		t.Fatalf("fixture: want a regex entry: %+v", before)
	}

	sem := decisionViolation("semantic_injection", policy.SeverityCritical, false, 0.95, "dec_a", 3, "semantic")
	sem.Timestamp = before.Timestamp.Add(time.Second)
	e.RecordSemanticViolation("sess-sd-kind", sem)

	fs := e.GetFlaggedSession("sess-sd-kind")
	if len(fs.Violations) != 1 {
		t.Fatalf("violations = %d, want 1", len(fs.Violations))
	}
	v := fs.Violations[0]
	if v.Semantic != nil || v.MatchedPattern != before.MatchedPattern || v.Description != before.Description ||
		v.Severity != before.Severity || v.EventID != before.EventID {
		t.Fatalf("a semantic decision replaced or mixed into a regex entry: %+v", v)
	}
	if !v.Timestamp.Equal(sem.Timestamp) {
		t.Fatalf("Timestamp = %v, want the last firing %v", v.Timestamp, sem.Timestamp)
	}
	// The decision itself is still recorded and scored.
	if !hasDecision(fs, "dec_a") || len(fs.ViolationEvents) != 2 {
		t.Fatalf("the decision must still get its record and event: records=%+v events=%d", fs.SemanticDecisions, len(fs.ViolationEvents))
	}
}

// Review M2, regex onto semantic: the semantic entry is kept, and an
// evidence-only critical does not leak into MaxSeverity through a mix.
func TestSemanticDecisions_RegexNeverReplacesASemanticEntry(t *testing.T) {
	e := newRiskLadderEngine(collidingRules(), baselineThresholds())
	sem := decisionViolation("semantic_injection", policy.SeverityCritical, true, 0.95, "dec_ev", 1, "ev critical")
	sem.Timestamp = time.Now().Add(-time.Minute)
	e.RecordSemanticViolation("sess-sd-kind2", sem)
	before := e.GetFlaggedSession("sess-sd-kind2").Violations[0]

	e.EvaluateMessages("sess-sd-kind2", []policy.MessageToScan{{Role: "user", Index: 0, Content: "zzmarkerzz"}})

	fs := e.GetFlaggedSession("sess-sd-kind2")
	if len(fs.Violations) != 1 {
		t.Fatalf("violations = %d, want 1", len(fs.Violations))
	}
	v := fs.Violations[0]
	if v.Semantic == nil || v.Semantic.DecisionID != "dec_ev" || !v.EvidenceOnly || v.Severity != policy.SeverityCritical ||
		v.EventID != before.EventID || v.MatchedPattern != "" {
		t.Fatalf("a regex firing replaced or mixed into a semantic entry: %+v / %+v", v, v.Semantic)
	}
	if !v.Timestamp.After(before.Timestamp) {
		t.Fatalf("Timestamp must advance to the regex firing: %v <= %v", v.Timestamp, before.Timestamp)
	}
	if fs.MaxSeverity == policy.SeverityCritical {
		t.Fatal("an evidence-only critical leaked into MaxSeverity through a regex firing")
	}
	if len(fs.ViolationEvents) != 2 {
		t.Fatalf("events = %d, want 2 (the regex firing still scores)", len(fs.ViolationEvents))
	}
}

// Review M2: config validation reserves the runner's rule names.
func TestSemanticDecisions_ConfigRejectsReservedRuleNames(t *testing.T) {
	for _, name := range []string{runner.RuleSemanticInjection, runner.RuleInjectionElevated} {
		c := config.DefaultConfig()
		c.Listen = ":8080"
		c.Backend = "https://api.example.com"
		c.Policy.Rules = append(c.Policy.Rules, config.PolicyRule{
			Name: name, Type: "content_match", Patterns: []string{"x"}, Severity: "warning",
		})
		res := c.Validate()
		var found bool
		for _, e := range res.Errors {
			if strings.HasPrefix(e.Field, "policy.rules[") && strings.Contains(e.Message, name) {
				found = true
			}
		}
		if res.Valid || !found {
			t.Fatalf("a policy rule named %q must be rejected: %+v", name, res.Errors)
		}
	}

	c := config.DefaultConfig()
	c.Listen = ":8080"
	c.Backend = "https://api.example.com"
	for _, e := range c.Validate().Errors {
		if strings.HasPrefix(e.Field, "policy.rules[") {
			t.Fatalf("the default rules must not use a reserved name: %+v", e)
		}
	}
}
