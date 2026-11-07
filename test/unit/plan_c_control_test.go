package unit

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"elida/internal/control"
	"elida/internal/correlation"
	"elida/internal/session"
	"elida/internal/storage"
)

// D4: a violation turn names its event, category, role, evidence flag and,
// for a match, the match ID and contributing count. Message and tool turns
// gain nothing.
func TestPlanCControl_ViolationTurnsCarryIdentity(t *testing.T) {
	e, m := matchEngine(t)
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)
	sess := manager.GetOrCreate("sess-c-match", "https://backend", "127.0.0.1:1")
	sess.RecordMessage("user", "hello", "anthropic")
	h := control.New(store, manager, control.WithPolicy(e))
	w := doJSON(t, h, http.MethodGet, "/control/sessions/sess-c-match/turns?full=true", nil)
	var resp struct {
		Turns []json.RawMessage `json:"turns"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("turns: %d %s", w.Code, w.Body.String())
	}
	var matchTurns, semanticTurns, messageTurns int
	for _, raw := range resp.Turns {
		var turn control.TurnEntry
		if err := json.Unmarshal(raw, &turn); err != nil {
			t.Fatal(err)
		}
		keys := jsonKeys(t, raw)
		switch {
		case turn.Type == "message":
			messageTurns++
			if keys["event_id"] || keys["correlation"] || keys["evidence_only"] {
				t.Fatalf("a message turn gains nothing: %s", raw)
			}
		case turn.RuleName == m.RuleName:
			matchTurns++
			if turn.EventID != m.EventID || turn.EventCategory != correlation.CategoryMatch || turn.SourceRole != "correlation" || !turn.EvidenceOnly ||
				turn.Correlation == nil || turn.Correlation.MatchID != m.EventID || turn.Correlation.EventCount != 2 {
				t.Fatalf("match turn: %s", raw)
			}
		case turn.Type == "violation":
			semanticTurns++
			if turn.EventID == "" || turn.EventCategory != "semantic_injection" || turn.SourceRole != "user" || !turn.EvidenceOnly || keys["correlation"] {
				t.Fatalf("semantic violation turn: %s", raw)
			}
		}
	}
	if matchTurns != 1 || semanticTurns != 1 || messageTurns != 1 {
		t.Fatalf("turns: match=%d semantic=%d message=%d", matchTurns, semanticTurns, messageTurns)
	}
}

// D4: the unauthenticated health block gains only an enum and two booleans.
func TestPlanCControl_HealthCarriesThreeContentFreeFacts(t *testing.T) {
	h, _ := newTestHandler()
	h.SetDecisionProvider(fixedDecision{control.DecisionStatus{
		Capability: "inline", EffectiveMode: "audit", ThresholdSetMatches: true, BreakerOpen: false,
		Model: "minilm-multihead", ModelChecksum: "deadbeef", Reason: "/etc/elida/models path", ThresholdSet: "v1",
	}})
	w := doJSON(t, h, http.MethodGet, "/control/health", nil)
	var body struct {
		Decision map[string]any `json:"decision"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Decision) != 5 {
		t.Fatalf("decision keys = %v, want capability, reason, effective_mode, threshold_set_matches, breaker_open", body.Decision)
	}
	if body.Decision["effective_mode"] != "audit" || body.Decision["threshold_set_matches"] != true || body.Decision["breaker_open"] != false {
		t.Fatalf("facts: %v", body.Decision)
	}
	if s := w.Body.String(); contains(s, "minilm") || contains(s, "deadbeef") || contains(s, "/etc/elida") || contains(s, `"v1"`) {
		t.Fatalf("health leaked model identity or the provider reason: %s", s)
	}
	// Without a provider the block is absent, as today.
	h2, _ := newTestHandler()
	w2 := doJSON(t, h2, http.MethodGet, "/control/health", nil)
	if jsonKeys(t, w2.Body.Bytes())["decision"] {
		t.Fatal("no provider, no decision block")
	}
}

// D4: shadow_correlation is a correlation.Stats object when set and absent
// when nil.
func TestPlanCControl_DecisionStatusShadowCorrelationShape(t *testing.T) {
	h, _ := newTestHandler()
	h.SetDecisionProvider(fixedDecision{control.DecisionStatus{Capability: "inline", ShadowCorrelation: &correlation.Stats{Rules: 1, Shards: 4, Matches: 2}}})
	w := doJSON(t, h, http.MethodGet, "/control/decision", nil)
	var st struct {
		Shadow *correlation.Stats `json:"shadow_correlation"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || st.Shadow == nil || st.Shadow.Rules != 1 || st.Shadow.Matches != 2 {
		t.Fatalf("shadow_correlation: %v %s", err, w.Body.String())
	}
	h2, _ := newTestHandler()
	h2.SetDecisionProvider(fixedDecision{control.DecisionStatus{Capability: "disabled"}})
	w2 := doJSON(t, h2, http.MethodGet, "/control/decision", nil)
	if jsonKeys(t, w2.Body.Bytes())["shadow_correlation"] {
		t.Fatalf("nil stats must be omitted: %s", w2.Body.String())
	}
}

// D1/D2 through the API: history detail carries violations[].correlation
// and correlation_shadow; the history list carries the former (inside
// violations, D9) and never the latter.
func TestPlanCControl_HistoryDetailCarriesCorrelationEvidence(t *testing.T) {
	s := openStore(t, "ctl.db")
	now := time.Now().UTC().Truncate(time.Millisecond)
	rec := baseRecord("sess-c-hist", now.Add(-time.Minute))
	rec.Violations = []storage.Violation{storedMatch("cm_hist", now, "ev_a", "ev_b")}
	rec.CorrelationShadow = []storage.CorrelationShadow{shadowMatch(1, now)}
	if err := s.SaveSession(rec); err != nil {
		t.Fatal(err)
	}
	store := session.NewMemoryStore()
	h := control.New(store, session.NewManager(store, 5*time.Minute), control.WithHistory(s))
	w := doJSON(t, h, http.MethodGet, "/control/history/sess-c-hist", nil)
	var got storage.SessionRecord
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusOK {
		t.Fatalf("history detail: %d %v %s", w.Code, err, w.Body.String())
	}
	if len(got.Violations) != 1 || got.Violations[0].Correlation == nil || got.Violations[0].Correlation.MatchID != "cm_hist" ||
		len(got.CorrelationShadow) != 1 || got.CorrelationShadow[0].MatchID != shadowMatch(1, now).MatchID {
		t.Fatalf("history detail lost evidence: %s", w.Body.String())
	}
	wl := doJSON(t, h, http.MethodGet, "/control/history", nil)
	if !contains(wl.Body.String(), `"match_id":"cm_hist"`) {
		t.Fatalf("the list carries violations[].correlation (D9): %s", wl.Body.String())
	}
	if contains(wl.Body.String(), "correlation_shadow") {
		t.Fatalf("the list must not carry correlation_shadow: %s", wl.Body.String())
	}
}
