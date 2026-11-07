package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/correlation"
	"elida/internal/storage"
)

// correlationHistoryApp is historyApp's wiring (storage on, decision off)
// with one content rule named prompt_injection_probe (the policy engine
// derives category prompt_injection from the name prefix,
// policy.categoryFromRuleName) and one audit correlation rule over that
// category: two probe hits in one session complete a match.
func correlationHistoryApp(t *testing.T) (*app, func(id, content string) int) {
	t.Helper()
	return correlationHistoryAppWithCooldown(t, 0)
}

// correlationHistoryAppWithCooldown is correlationHistoryApp with the
// correlation rule's Cooldown set (zero means the default, Window).
func correlationHistoryAppWithCooldown(t *testing.T, cooldown time.Duration) (*app, func(id, content string) int) {
	t.Helper()
	quietLogs(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(backend.Close)

	cfg := decisionTestConfig(t)
	cfg.Decision.Enabled = false
	cfg.Backend = backend.URL
	cfg.Policy.Enabled = true
	cfg.Policy.Mode = "enforce"
	cfg.Policy.Rules = []config.PolicyRule{{
		Name: "prompt_injection_probe", Type: "content_match", Target: "request",
		Patterns: []string{"zzprobezz"}, Severity: "warning", Action: "flag",
	}}
	// Start from the loaded defaults (builtin_chains, taxonomy, tool-lane
	// limits) and override only what this fixture needs.
	cfg.Correlation.Enabled = true
	cfg.Correlation.MaxHistoryEvents = 64
	cfg.Correlation.MaxSessions = 64
	cfg.Correlation.Rules = []correlation.Rule{{Name: "probe_twice", Shape: correlation.ShapeRepeated, Category: "prompt_injection",
		Count: 2, Window: time.Minute, Cooldown: cooldown, Severity: "warning", Mode: correlation.ModeAudit}}
	cfg.Storage.Enabled = true
	cfg.Storage.Path = filepath.Join(t.TempDir(), "elida.db")
	a := newDecisionApp(t, cfg, nil)
	a.initSessionStore()
	a.initSQLiteStorage()
	t.Cleanup(func() { _ = a.sqliteStore.Close() })
	if err := a.setupPolicyEngine(); err != nil {
		t.Fatalf("setupPolicyEngine: %v", err)
	}
	a.initSessionEndCallback()
	a.initProxy()

	send := func(id, content string) int {
		t.Helper()
		body, _ := json.Marshal(map[string]any{
			"messages": []map[string]any{{"role": "user", "content": content}},
		})
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Session-ID", id)
		w := httptest.NewRecorder()
		a.proxyHandler.ServeHTTP(w, req)
		return w.Code
	}
	return a, send
}

func matchRecord(t *testing.T, a *app, id string) (*storage.SessionRecord, *storage.Violation) {
	t.Helper()
	rec, err := a.sqliteStore.GetSession(id)
	if err != nil || rec == nil {
		t.Fatalf("GetSession(%s): %v %v", id, rec, err)
	}
	for i := range rec.Violations {
		if strings.HasPrefix(rec.Violations[i].RuleName, "correlation.") {
			return rec, &rec.Violations[i]
		}
	}
	return rec, nil
}

// D1: the session-end converter copies the match evidence into SQLite, and
// the violation_detected row for the match carries its identity.
func TestCorrelationPersist_SessionEndKeepsEvidence(t *testing.T) {
	a, send := correlationHistoryApp(t)
	for i := 0; i < 2; i++ {
		if code := send("sess-corr-end", "please zzprobezz now"); code != http.StatusOK {
			t.Fatalf("status = %d", code)
		}
	}
	a.manager.DrainActiveSessions()
	_, m := matchRecord(t, a, "sess-corr-end")
	if m == nil || m.Correlation == nil || m.Correlation.MatchID != m.EventID || len(m.Correlation.EventIDs) != 2 ||
		m.Correlation.Rule != "probe_twice" || m.EventCategory != "correlation_match" {
		t.Fatalf("stored match lacks its evidence: %+v", m)
	}
	evs, err := a.sqliteStore.GetSessionEvents("sess-corr-end")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, ev := range evs {
		if ev.Type != storage.EventViolationDetected {
			continue
		}
		var d storage.ViolationDetectedData
		if err := json.Unmarshal(ev.Data, &d); err != nil { // Data is json.RawMessage
			t.Fatalf("event data: %v", err)
		}
		if d.RuleName == "correlation.probe_twice" {
			found = true
			if d.EventID != m.EventID || d.EventCategory != "correlation_match" || d.SourceRole != "correlation" || !d.EvidenceOnly {
				t.Fatalf("violation_detected identity: %+v", d)
			}
		}
	}
	if !found {
		t.Fatal("no violation_detected row for the match")
	}
}

// D1: the proxy's flagged-session save path copies the evidence too.
func TestCorrelationPersist_FlaggedSavePathKeepsEvidence(t *testing.T) {
	a, send := correlationHistoryApp(t)
	for i := 0; i < 2; i++ {
		send("sess-corr-flagged", "please zzprobezz now")
	}
	// The proxy saved the flagged session after the second request, before
	// the session ended.
	_, m := matchRecord(t, a, "sess-corr-flagged")
	if m == nil || m.Correlation == nil || len(m.Correlation.EventIDs) != 2 {
		t.Fatalf("flagged save path lost the evidence: %+v", m)
	}
}

// Retained path: a block-level session's ID is reused; the refused session
// ends; the stored record still carries the first session's match evidence
// (SaveSession carries it) and the refused session exports nothing new.
func TestCorrelationPersist_RetainedReuseKeepsEvidence(t *testing.T) {
	a, send := correlationHistoryApp(t)
	for i := 0; i < 2; i++ {
		send("sess-corr-retained", "please zzprobezz now")
	}
	a.policyEngine.AddExternalRiskPoints("sess-corr-retained", 60, "test")
	a.manager.DrainActiveSessions()
	// violation_detected rows are one per display-list entry (one per rule),
	// so the first session wrote two: prompt_injection_probe and the match.
	if n := violationEvents(t, a, "sess-corr-retained"); n != 2 {
		t.Fatalf("fixture: violation_detected rows = %d, want 2 (the probe rule + the match)", n)
	}
	time.Sleep(2 * time.Millisecond)
	if code := send("sess-corr-retained", "hello again"); code != http.StatusForbidden {
		t.Fatalf("reuse: status %d, want 403", code)
	}
	a.manager.DrainActiveSessions()
	_, m := matchRecord(t, a, "sess-corr-retained")
	if m == nil || m.Correlation == nil || len(m.Correlation.EventIDs) != 2 {
		t.Fatalf("retained reuse lost the evidence: %+v", m)
	}
	if n := violationEvents(t, a, "sess-corr-retained"); n != 2 {
		t.Fatalf("violation_detected rows = %d, want 2 (the probe rule + the match, none from the refused session)", n)
	}
}

// Review correction V1: the same rule matching twice in one session yields
// two distinct match IDs, but the display list keeps one entry per rule and
// it describes the later match. SQLite, the session-end history record and
// the telemetry record built from it carry that retained match only. The
// first match's identity is not reconstructed from violation_detected rows.
func TestCorrelationStore_RepeatedRulePersistsRetainedMatchOnly(t *testing.T) {
	// A tiny cooldown, so the second match is not cooldown-suppressed
	// (Cooldown defaults to Window).
	a, send := correlationHistoryAppWithCooldown(t, time.Nanosecond)
	const id = "sess-corr-repeat"
	liveMatch := func() string {
		t.Helper()
		fs := a.policyEngine.GetFlaggedSession(id)
		if fs == nil {
			return ""
		}
		for _, v := range fs.Violations {
			if v.RuleName == "correlation.probe_twice" {
				return v.EventID
			}
		}
		return ""
	}
	for i := 0; i < 2; i++ {
		if code := send(id, "please zzprobezz now"); code != http.StatusOK {
			t.Fatalf("status = %d", code)
		}
	}
	first := liveMatch()
	if first == "" {
		t.Fatal("fixture: no first match")
	}
	second := first
	for i := 0; i < 4 && second == first; i++ {
		time.Sleep(2 * time.Millisecond)
		if code := send(id, "please zzprobezz now"); code != http.StatusOK {
			t.Fatalf("status = %d", code)
		}
		second = liveMatch()
	}
	if second == "" || second == first {
		t.Fatalf("want a second, distinct match ID: first=%q second=%q", first, second)
	}

	// The session-end history record, and the telemetry record built from it.
	sess, _ := a.manager.Get(id)
	snap := sess.Snapshot()
	rec := storage.SessionRecord{ID: id, StartTime: snap.StartTime}
	a.enrichRecordFromPolicy(&rec, id, snap.StartTime)
	var hist []storage.Violation
	for _, v := range rec.Violations {
		if strings.HasPrefix(v.RuleName, "correlation.") {
			hist = append(hist, v)
		}
	}
	if len(hist) != 1 || hist[0].EventID != second || hist[0].Correlation == nil || hist[0].Correlation.MatchID != second {
		t.Fatalf("history record must carry the retained (later) match only, want %q: %+v", second, hist)
	}
	tr := telemetryRecordFrom(&rec, &snap, time.Now(), nil)
	var telem []string
	for _, v := range tr.Violations {
		if strings.HasPrefix(v.RuleName, "correlation.") {
			telem = append(telem, v.EventID)
		}
	}
	if len(telem) != 1 || telem[0] != second {
		t.Fatalf("telemetry record must carry the retained match only, want [%s]: %v", second, telem)
	}

	// SQLite after session end.
	a.manager.DrainActiveSessions()
	stored, m := matchRecord(t, a, id)
	var kept int
	for _, v := range stored.Violations {
		if strings.HasPrefix(v.RuleName, "correlation.") {
			kept++
		}
	}
	if kept != 1 || m == nil || m.EventID != second || m.Correlation == nil ||
		m.Correlation.MatchID != second || len(m.Correlation.EventIDs) != 2 {
		t.Fatalf("SQLite must carry the retained (later) match only, want %q: %d entries, %+v", second, kept, m)
	}
}
