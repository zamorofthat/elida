package unit

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"elida/internal/control"
	"elida/internal/policy"
	"elida/internal/session"
	"elida/internal/storage"
)

func storedDecision(eventID, decisionID string, ts time.Time) storage.SemanticDecision {
	return storage.SemanticDecision{
		EventID: eventID, RuleName: "semantic_injection", EventCategory: "semantic_injection",
		Severity: "warning", EffectiveSeverity: "warning", SourceRole: "tool", MessageIndex: 3,
		Timestamp: ts,
		Evidence: storage.SemanticEvidence{
			Signal: "injection", Probability: 0.81, AuxProbability: 0.04, Model: "minilm-multihead",
			ModelVersion: "v5-fp32", ModelChecksum: "abc123", ThresholdSet: "v1", DecisionID: decisionID,
			Transform: "base64_decode", TransformDepth: 1, WindowStartByte: 10, WindowEndByte: 210,
			CoverageComplete: true, ExecutionMode: "inline", ProtectionScope: "current_request",
		},
	}
}

func openStore(t *testing.T, name string) *storage.SQLiteStore {
	t.Helper()
	s, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func baseRecord(id string, start time.Time) storage.SessionRecord {
	return storage.SessionRecord{ID: id, State: "ended", StartTime: start, EndTime: start.Add(time.Minute),
		Backend: "https://backend", ClientAddr: "127.0.0.1:1"}
}

func TestSemanticDecisionsStore_PersistsAndLoads(t *testing.T) {
	s := openStore(t, "sd.db")
	now := time.Now().UTC().Truncate(time.Millisecond)
	rec := baseRecord("sess-sd-store", now.Add(-time.Minute))
	rec.SemanticDecisions = []storage.SemanticDecision{storedDecision("evt_1", "dec_1", now), storedDecision("evt_2", "dec_2", now)}
	rec.SemanticDecisionsDropped = 7
	ev := storedDecision("evt_2", "dec_2", now).Evidence
	rec.Violations = []storage.Violation{{RuleName: "semantic_injection", Severity: "warning", Action: "flag", EventID: "evt_2", Semantic: &ev}}
	if err := s.SaveSession(rec); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	got, err := s.GetSession("sess-sd-store")
	if err != nil || got == nil {
		t.Fatalf("GetSession: %v %v", got, err)
	}
	if len(got.SemanticDecisions) != 2 || got.SemanticDecisionsDropped != 7 {
		t.Fatalf("decisions=%d dropped=%d", len(got.SemanticDecisions), got.SemanticDecisionsDropped)
	}
	if !reflect.DeepEqual(got.SemanticDecisions[0].Evidence, rec.SemanticDecisions[0].Evidence) || got.SemanticDecisions[0].EventID != "evt_1" {
		t.Fatalf("round trip lost fields: %+v", got.SemanticDecisions[0])
	}
	if got.Violations[0].Semantic == nil || *got.Violations[0].Semantic != ev {
		t.Fatalf("the representative metadata on the violation was lost: %+v", got.Violations[0].Semantic)
	}
}

func TestSemanticDecisionsStore_ListOmitsDecisions(t *testing.T) {
	s := openStore(t, "list.db")
	rec := baseRecord("sess-sd-list", time.Now().Add(-time.Minute))
	rec.SemanticDecisions = []storage.SemanticDecision{storedDecision("evt_1", "dec_1", time.Now())}
	if err := s.SaveSession(rec); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	list, err := s.ListSessions(storage.ListSessionsOptions{Limit: 10})
	if err != nil || len(list) != 1 {
		t.Fatalf("ListSessions: %v %v", list, err)
	}
	if len(list[0].SemanticDecisions) != 0 {
		t.Fatal("the list query must not load decisions; only GetSession does")
	}
}

// Review Focus 3: a reused session ID carries the earlier session's
// decisions, deduplicated by EventID, prior first.
func TestSemanticDecisionsStore_ReusedIDCarriesAndDeduplicates(t *testing.T) {
	s := openStore(t, "reuse.db")
	t0 := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	first := baseRecord("sess-sd-reuse", t0)
	first.SemanticDecisions = []storage.SemanticDecision{storedDecision("evt_1", "dec_1", t0), storedDecision("evt_2", "dec_2", t0)}
	if err := s.SaveSession(first); err != nil {
		t.Fatalf("first save: %v", err)
	}
	t1 := t0.Add(30 * time.Minute)
	second := baseRecord("sess-sd-reuse", t1)
	second.SemanticDecisions = []storage.SemanticDecision{storedDecision("evt_2", "dec_2", t1), storedDecision("evt_3", "dec_3", t1)}
	if err := s.SaveSession(second); err != nil {
		t.Fatalf("second save: %v", err)
	}
	// Saving the same session again must not duplicate the carried part.
	if err := s.SaveSession(second); err != nil {
		t.Fatalf("second save again: %v", err)
	}
	got, err := s.GetSession("sess-sd-reuse")
	if err != nil || got == nil {
		t.Fatalf("GetSession: %v %v", got, err)
	}
	var ids []string
	for _, d := range got.SemanticDecisions {
		ids = append(ids, d.EventID)
	}
	if !reflect.DeepEqual(ids, []string{"evt_1", "evt_2", "evt_3"}) {
		t.Fatalf("event IDs = %v, want [evt_1 evt_2 evt_3]", ids)
	}
}

// The conversion must carry every field. JSON tags are identical on both
// sides, so equal JSON means nothing was dropped or renamed.
func TestSemanticDecisionsStore_ConversionKeepsEveryField(t *testing.T) {
	in := policy.SemanticDecision{
		EventID: "evt", RuleName: "semantic_injection", EventCategory: "semantic_injection",
		Severity: policy.SeverityCritical, EffectiveSeverity: policy.SeverityWarning, SourceRole: "tool",
		MessageIndex: 4, EvidenceOnly: true, WouldContributePoints: 8, Timestamp: time.Now().UTC(),
		Evidence: policy.SemanticEvidence{
			Signal: "injection", Probability: 0.9, AuxProbability: 0.1, Vetoed: true, Model: "m",
			ModelVersion: "v", ModelChecksum: "c", ThresholdSet: "v1", DecisionID: "d", Transform: "rot13",
			TransformDepth: 2, WindowStartByte: 1, WindowEndByte: 2, CoverageComplete: true,
			ExecutionMode: "async", ProtectionScope: "future_activity",
		},
	}
	out := storage.SemanticDecisionsFromPolicy([]policy.SemanticDecision{in}, time.Time{})
	a, _ := json.Marshal(in)
	b, _ := json.Marshal(out[0])
	if string(a) != string(b) {
		t.Fatalf("conversion changed the record:\npolicy:  %s\nstorage: %s", a, b)
	}
	if storage.SemanticEvidenceFromPolicy(nil) != nil {
		t.Fatal("nil evidence must convert to nil")
	}
}

func TestSemanticDecisionsStore_ConversionSkipsDecisionsBeforeStart(t *testing.T) {
	start := time.Now()
	in := []policy.SemanticDecision{
		{EventID: "old", Timestamp: start.Add(-time.Minute)},
		{EventID: "new", Timestamp: start.Add(time.Second)},
	}
	out := storage.SemanticDecisionsFromPolicy(in, start)
	if len(out) != 1 || out[0].EventID != "new" {
		t.Fatalf("got %+v, want only the decision from this session instance", out)
	}
}

func TestSemanticDecisionsStore_TypesCarryNoContentFields(t *testing.T) {
	forbidden := map[string]bool{"MatchedText": true, "SourceContent": true, "Content": true, "Text": true, "Body": true, "RequestBody": true, "ResponseBody": true}
	for _, typ := range []reflect.Type{reflect.TypeOf(storage.SemanticDecision{}), reflect.TypeOf(storage.SemanticEvidence{}), reflect.TypeOf(policy.SemanticDecision{})} {
		for i := 0; i < typ.NumField(); i++ {
			if forbidden[typ.Field(i).Name] {
				t.Fatalf("%s has a content field %s", typ.Name(), typ.Field(i).Name)
			}
		}
	}
}

func TestSemanticDecisions_ExposedOnDetailNotOnFlaggedList(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)
	manager.GetOrCreate("sess-sd-api", "https://backend", "127.0.0.1:1")
	engine := newRiskLadderEngine(baselineRules(), baselineThresholds())
	engine.RecordSemanticViolation("sess-sd-api", decisionViolation("semantic_injection", policy.SeverityWarning, false, 0.6, "dec_api", 1, "api"))
	h := control.New(store, manager, control.WithPolicy(engine))

	var detail struct {
		SemanticDecisions []policy.SemanticDecision `json:"semantic_decisions"`
	}
	w := doJSON(t, h, http.MethodGet, "/control/sessions/sess-sd-api", nil)
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil || len(detail.SemanticDecisions) != 1 {
		t.Fatalf("session detail: %d %s", w.Code, w.Body.String())
	}

	w = doJSON(t, h, http.MethodGet, "/control/flagged", nil)
	if contains(w.Body.String(), "semantic_decisions\"") {
		t.Fatalf("the flagged list must not carry the per-decision list: %s", w.Body.String())
	}

	w = doJSON(t, h, http.MethodGet, "/control/flagged/sess-sd-api", nil)
	if !contains(w.Body.String(), "semantic_decisions\"") {
		t.Fatalf("the flagged detail carries the list: %s", w.Body.String())
	}
}

// A row written before the semantic_decisions column existed, or one whose
// column holds NULL or an empty string, loads as an empty list, not an error.
func TestSemanticDecisionsStore_NullOrEmptyColumnLoadsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "null.db")
	s, err := storage.NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	now := time.Now()
	for id, val := range map[string]any{"sess-sd-null": nil, "sess-sd-empty": ""} {
		if _, err = raw.Exec(`INSERT INTO sessions (id, state, start_time, end_time, duration_ms, backend, client_addr, semantic_decisions)
			VALUES (?, 'ended', ?, ?, 0, 'https://backend', '127.0.0.1:1', ?)`, id, now.Add(-time.Minute), now, val); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
		got, gerr := s.GetSession(id)
		if gerr != nil || got == nil {
			t.Fatalf("GetSession(%s): %v %v", id, got, gerr)
		}
		if len(got.SemanticDecisions) != 0 || got.SemanticDecisionsDropped != 0 {
			t.Fatalf("%s loaded decisions=%d dropped=%d, want none", id, len(got.SemanticDecisions), got.SemanticDecisionsDropped)
		}
	}
	// A later session reusing the NULL row's ID carries nothing from it.
	rec := baseRecord("sess-sd-null", now.Add(time.Hour))
	rec.SemanticDecisions = []storage.SemanticDecision{storedDecision("evt_9", "dec_9", now.Add(time.Hour))}
	if err = s.SaveSession(rec); err != nil {
		t.Fatalf("SaveSession over NULL row: %v", err)
	}
	got, err := s.GetSession("sess-sd-null")
	if err != nil || got == nil || len(got.SemanticDecisions) != 1 {
		t.Fatalf("reuse over NULL row: %+v %v", got, err)
	}
}

// Carried decisions are capped at MaxCarriedSemanticDecisions: the record's
// own decisions are always kept and the newest carried ones fill what is
// left.
func TestSemanticDecisionsStore_CarriedDecisionsAreCapped(t *testing.T) {
	s := openStore(t, "cap.db")
	t0 := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	first := baseRecord("sess-sd-cap", t0)
	for i := 0; i < storage.MaxCarriedSemanticDecisions; i++ {
		first.SemanticDecisions = append(first.SemanticDecisions, storedDecision(fmt.Sprintf("old_%d", i), fmt.Sprintf("d_old_%d", i), t0))
	}
	if err := s.SaveSession(first); err != nil {
		t.Fatalf("first save: %v", err)
	}
	t1 := t0.Add(30 * time.Minute)
	second := baseRecord("sess-sd-cap", t1)
	second.SemanticDecisions = []storage.SemanticDecision{storedDecision("new_0", "d_new_0", t1), storedDecision("new_1", "d_new_1", t1)}
	// Saved twice (flagged save, then session end): the cut is counted once.
	for i := 0; i < 2; i++ {
		if err := s.SaveSession(second); err != nil {
			t.Fatalf("second save %d: %v", i, err)
		}
	}
	got, err := s.GetSession("sess-sd-cap")
	if err != nil || got == nil {
		t.Fatalf("GetSession: %v %v", got, err)
	}
	n := len(got.SemanticDecisions)
	if n != storage.MaxCarriedSemanticDecisions {
		t.Fatalf("len = %d, want %d", n, storage.MaxCarriedSemanticDecisions)
	}
	if got.SemanticDecisions[0].EventID != "old_2" || got.SemanticDecisions[n-2].EventID != "new_0" || got.SemanticDecisions[n-1].EventID != "new_1" {
		t.Fatalf("want oldest carried old_2 then own new_0,new_1; got first=%s last=%s,%s",
			got.SemanticDecisions[0].EventID, got.SemanticDecisions[n-2].EventID, got.SemanticDecisions[n-1].EventID)
	}
	if got.SemanticDecisionsDropped != 2 {
		t.Fatalf("dropped = %d, want 2 (the two carried records the cap cut)", got.SemanticDecisionsDropped)
	}
}
