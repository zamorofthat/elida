package unit

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"elida/internal/control"
	"elida/internal/correlation"
	"elida/internal/policy"
	"elida/internal/session"
)

// matchEngine is an enforce-mode policy engine with one audit correlation
// rule, after two inputs that complete one match on session sess-c-match.
// It returns the engine and the match's display violation (a deep copy).
func matchEngine(t *testing.T) (*policy.Engine, policy.Violation) {
	t.Helper()
	e := wiringEngine(t, "enforce", correlation.ModeAudit)
	now := time.Now()
	recordCorrelationInput(e, "sess-c-match", "ev_a", now)
	recordCorrelationInput(e, "sess-c-match", "ev_b", now.Add(time.Second))
	m := correlationMatch(e.GetFlaggedSession("sess-c-match"))
	if m == nil || m.Correlation == nil || len(m.Correlation.EventIDs) != 2 {
		t.Fatalf("fixture: want one match over two events, got %+v", m)
	}
	return e, *m
}

// jsonKeys returns the top-level keys of a JSON object.
func jsonKeys(t *testing.T, raw []byte) map[string]bool {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("not a JSON object: %v: %s", err, raw)
	}
	keys := map[string]bool{}
	for k := range m {
		keys[k] = true
	}
	return keys
}

// fixedDecision is a control.DecisionProvider returning one status.
type fixedDecision struct{ st control.DecisionStatus }

func (f fixedDecision) DecisionStatus() control.DecisionStatus { return f.st }

// KEEP. A plain (non-match) violation serializes with no correlation key
// from the policy engine; storage must keep that after Task 1 (omitempty).
func TestBaselineC_PlainViolationJSONHasNoCorrelationKey(t *testing.T) {
	e := newRiskLadderEngine(baselineRules(), baselineThresholds())
	e.EvaluateMessages("sess-c-plain", []policy.MessageToScan{{Role: "user", Index: 0, Content: "zzmarkerzz"}})
	fs := e.GetFlaggedSession("sess-c-plain")
	if fs == nil || len(fs.Violations) != 1 {
		t.Fatalf("fixture: %+v", fs)
	}
	raw, _ := json.Marshal(fs.Violations[0])
	if jsonKeys(t, raw)["correlation"] {
		t.Fatalf("a plain violation must not carry a correlation key: %s", raw)
	}
}

// KEEP. The match violation's shape in the policy engine: Plan C reads it
// and never changes it.
func TestBaselineC_MatchViolationShape(t *testing.T) {
	_, m := matchEngine(t)
	if m.RuleName != "correlation.repeated_injection" || m.EventCategory != correlation.CategoryMatch ||
		m.SourceRole != "correlation" || !m.EvidenceOnly || m.EventID != m.Correlation.MatchID ||
		m.Correlation.Rule != "repeated_injection" || m.Correlation.Shape != "repeated" || m.Correlation.Mode != "audit" ||
		m.Correlation.FirstEvent.IsZero() || m.Correlation.LastEvent.IsZero() || !m.Timestamp.Equal(m.Correlation.LastEvent) {
		t.Fatalf("match violation shape drifted: %+v %+v", m, m.Correlation)
	}
	if m.MatchedText != "" || m.SourceContent != "" {
		t.Fatal("a match violation carries no content")
	}
}

// FLIP in Task 5. Today a violation turn carries only timestamp, type,
// rule_name, content, severity and action.
func TestBaselineC_ViolationTurnKeysToday(t *testing.T) {
	e, _ := matchEngine(t)
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)
	manager.GetOrCreate("sess-c-match", "https://backend", "127.0.0.1:1")
	h := control.New(store, manager, control.WithPolicy(e))
	w := doJSON(t, h, http.MethodGet, "/control/sessions/sess-c-match/turns?full=true", nil)
	var resp struct {
		Turns []json.RawMessage `json:"turns"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || len(resp.Turns) == 0 {
		t.Fatalf("turns: %d %s", w.Code, w.Body.String())
	}
	for _, raw := range resp.Turns {
		keys := jsonKeys(t, raw)
		if keys["event_id"] || keys["correlation"] || keys["evidence_only"] {
			t.Fatalf("today a violation turn has no identity fields: %s", raw)
		}
	}
}

// FLIP in Task 5. Today /control/health's decision block is exactly
// capability and reason.
func TestBaselineC_HealthDecisionKeysToday(t *testing.T) {
	h, _ := newTestHandler()
	h.SetDecisionProvider(fixedDecision{control.DecisionStatus{Capability: "inline", EffectiveMode: "audit", ThresholdSetMatches: true}})
	w := doJSON(t, h, http.MethodGet, "/control/health", nil)
	var body struct {
		Decision map[string]any `json:"decision"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("health: %v", err)
	}
	if len(body.Decision) != 2 || body.Decision["capability"] != "inline" || body.Decision["reason"] == "" {
		t.Fatalf("today decision = %v", body.Decision)
	}
}

// KEEP. /control/decision without a shadow correlator has no
// shadow_correlation key (Task 5 adds it only when one runs).
func TestBaselineC_DecisionStatusHasNoShadowCorrelationKeyWhenUnwired(t *testing.T) {
	h, _ := newTestHandler()
	w := doJSON(t, h, http.MethodGet, "/control/decision", nil)
	if jsonKeys(t, w.Body.Bytes())["shadow_correlation"] {
		t.Fatalf("unwired status must not carry shadow_correlation: %s", w.Body.String())
	}
}

// KEEP. The session list omits correlation_shadow (Plan B) and must keep
// omitting it after Task 2 persists it.
func TestBaselineC_SessionListOmitsCorrelationShadow(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)
	sess := manager.GetOrCreate("sess-c-list", "https://backend", "127.0.0.1:1")
	sess.RecordCorrelationShadow(session.CorrelationShadow{MatchID: "cm_1", Rule: "r", EventIDs: []string{"dec_a"}})
	h := control.New(store, manager)
	w := doJSON(t, h, http.MethodGet, "/control/sessions", nil)
	if contains(w.Body.String(), "correlation_shadow") {
		t.Fatalf("list must not carry correlation_shadow: %s", w.Body.String())
	}
}
