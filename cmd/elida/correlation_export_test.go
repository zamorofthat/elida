package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/policy"
	"elida/internal/storage"
	"elida/internal/telemetry"
)

// TestCorrelationExport_MatchPersistsAndExports drives a slow-burn match
// through the production wiring to session end: the SQLite record carries
// the correlation.-prefixed violation as evidence-only with the match ID,
// a violation_detected event is stored for it, and the OCSF finding is
// informational evidence with no content (adapted from the wiring
// re-review's end-to-end probe).
func TestCorrelationExport_MatchPersistsAndExports(t *testing.T) {
	quietLogs(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(backend.Close)
	cfg := decisionTestConfig(t)
	cfg.Decision.Enabled = false
	cfg.Backend = backend.URL
	cfg.Policy.Enabled = true
	cfg.Correlation.Enabled = true
	cfg.Correlation.Rules = config.DefaultCorrelationRules()
	cfg.Storage.Enabled = true
	cfg.Storage.Path = filepath.Join(t.TempDir(), "elida.db")
	a := newDecisionApp(t, cfg, nil)
	a.initSessionStore()
	a.initSQLiteStorage()
	t.Cleanup(func() { _ = a.sqliteStore.Close() })
	a.initPolicyEngine()
	a.initSessionEndCallback()
	a.initProxy()

	const id = "sess-corr-e2e"
	const sentinel = "SECRETCONTENT"
	body, _ := json.Marshal(map[string]any{"messages": []map[string]any{{"role": "user", "content": "hi"}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Session-ID", id)
	w := httptest.NewRecorder()
	a.proxyHandler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	for i, idx := range []int{1, 3, 5} {
		a.policyEngine.RecordSemanticViolation(id, policy.Violation{
			RuleName: "injection_elevated", Description: sentinel, Severity: policy.SeverityInfo,
			Action: "flag", Timestamp: time.Now().Add(time.Duration(i) * time.Millisecond),
			SourceRole: "tool", MessageIndex: idx, EventCategory: "injection_elevated", EvidenceOnly: true,
			Semantic: &policy.SemanticEvidence{DecisionID: fmt.Sprintf("dec-%d", idx), Signal: "injection", Probability: 0.35,
				Model: "m", ModelVersion: "v", ThresholdSet: "v1", ExecutionMode: "async", ProtectionScope: "current_request"},
		})
	}
	var live *policy.Violation
	fs := a.policyEngine.GetFlaggedSession(id)
	for i := range fs.Violations {
		if fs.Violations[i].RuleName == "correlation.slow_burn_injection" {
			live = &fs.Violations[i]
		}
	}
	if live == nil || !strings.HasPrefix(live.EventID, "cm_") {
		t.Fatalf("want a live slow_burn_injection match, got %+v", live)
	}

	// The OTEL/OCSF path, as exportToTelemetry builds it.
	sess, _ := a.manager.Get(id)
	snap := sess.Snapshot()
	rec := storage.SessionRecord{ID: id, StartTime: snap.StartTime}
	a.enrichRecordFromPolicy(&rec, id, snap.StartTime)
	tr := telemetryRecordFrom(&rec, &snap, time.Now(), nil)
	var tv *telemetry.Violation
	for i := range tr.Violations {
		if tr.Violations[i].RuleName == "correlation.slow_burn_injection" {
			tv = &tr.Violations[i]
		}
	}
	if tv == nil {
		t.Fatal("the match is missing from the telemetry record")
	}
	if !tv.EvidenceOnly || tv.Semantic != nil {
		t.Fatalf("telemetry violation: evidence=%v semantic=%v", tv.EvidenceOnly, tv.Semantic != nil)
	}
	f := telemetry.BuildPolicyDetection(id, *tv, tr)
	if f.SeverityID != telemetry.OCSFSeverityInfo || !f.Unmapped.EvidenceOnly ||
		!strings.HasPrefix(f.Message, "Policy evidence (no risk contributed)") {
		t.Fatalf("OCSF: sev=%d evidence=%v msg=%q", f.SeverityID, f.Unmapped.EvidenceOnly, f.Message)
	}
	raw, _ := json.Marshal(f)
	if strings.Contains(string(raw), sentinel) {
		t.Fatalf("content in the OCSF finding: %s", raw)
	}

	// Session end persists the record.
	a.manager.DrainActiveSessions()
	stored, err := a.sqliteStore.GetSession(id)
	if err != nil || stored == nil {
		t.Fatalf("GetSession: %v", err)
	}
	var sv *storage.Violation
	for i := range stored.Violations {
		if stored.Violations[i].RuleName == "correlation.slow_burn_injection" {
			sv = &stored.Violations[i]
		}
	}
	if sv == nil {
		t.Fatalf("no stored correlation violation: %+v", stored.Violations)
	}
	if !sv.EvidenceOnly || sv.EventID != live.EventID || strings.Contains(sv.Description, sentinel) {
		t.Fatalf("stored violation: evidence=%v event_id=%q (want %q) desc=%q", sv.EvidenceOnly, sv.EventID, live.EventID, sv.Description)
	}
	evs, _ := a.sqliteStore.GetSessionEvents(id)
	found := false
	for _, e := range evs {
		if e.Type == storage.EventViolationDetected && strings.Contains(string(e.Data), "correlation.slow_burn_injection") {
			found = true
			if !strings.Contains(string(e.Data), `"evidence_only":true`) || strings.Contains(string(e.Data), sentinel) {
				t.Fatalf("violation_detected for the match: %s", e.Data)
			}
		}
	}
	if !found {
		t.Fatal("no violation_detected event for the match")
	}
}
