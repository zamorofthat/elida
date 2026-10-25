package unit

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"elida/internal/policy"
	"elida/internal/storage"
)

func storedMatch(matchID string, at time.Time, ids ...string) storage.Violation {
	cats := make([]string, len(ids))
	for i := range ids {
		cats[i] = "semantic_injection"
	}
	return storage.Violation{
		RuleName: "correlation.repeated_injection", Description: "Temporal correlation rule repeated_injection matched 2 events",
		Severity: "critical", Action: "flag", EventCategory: "correlation_match", SourceRole: "correlation",
		EvidenceOnly: true, EventID: matchID, Timestamp: at,
		Correlation: &storage.CorrelationEvidence{
			MatchID: matchID, Rule: "repeated_injection", Shape: "repeated", Mode: "audit",
			EventIDs: ids, Categories: cats, FirstEvent: at.Add(-time.Second), LastEvent: at,
		},
	}
}

func TestCorrelationStore_PersistsAndLoadsEvidence(t *testing.T) {
	s := openStore(t, "corr.db")
	now := time.Now().UTC().Truncate(time.Millisecond)
	rec := baseRecord("sess-corr-store", now.Add(-time.Minute))
	rec.Violations = []storage.Violation{storedMatch("cm_aaaa", now, "ev_a", "ev_b")}
	if err := s.SaveSession(rec); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	got, err := s.GetSession("sess-corr-store")
	if err != nil || got == nil || len(got.Violations) != 1 {
		t.Fatalf("GetSession: %+v %v", got, err)
	}
	if !reflect.DeepEqual(got.Violations[0].Correlation, rec.Violations[0].Correlation) {
		t.Fatalf("round trip lost evidence:\n got %+v\nwant %+v", got.Violations[0].Correlation, rec.Violations[0].Correlation)
	}
	// The list endpoint's query returns the violations column too (D9).
	list, err := s.ListSessions(storage.ListSessionsOptions{Limit: 10})
	if err != nil || len(list) != 1 || list[0].Violations[0].Correlation == nil {
		t.Fatalf("ListSessions must carry the bounded evidence inside violations: %+v %v", list, err)
	}
}

// Review Focus 1: a reused ID keeps the earlier session's match and the new
// session's match, each with its own evidence; a re-save of the same
// session does not duplicate either.
func TestCorrelationStore_ReusedIDKeepsBothMatches(t *testing.T) {
	s := openStore(t, "corr-reuse.db")
	t0 := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	first := baseRecord("sess-corr-reuse", t0)
	first.Violations = []storage.Violation{storedMatch("cm_first", t0.Add(time.Minute), "ev_a", "ev_b")}
	if err := s.SaveSession(first); err != nil {
		t.Fatalf("first: %v", err)
	}
	t1 := t0.Add(30 * time.Minute)
	second := baseRecord("sess-corr-reuse", t1)
	second.Violations = []storage.Violation{storedMatch("cm_second", t1.Add(time.Minute), "ev_c", "ev_d")}
	for i := 0; i < 2; i++ {
		if err := s.SaveSession(second); err != nil {
			t.Fatalf("second save %d: %v", i, err)
		}
	}
	got, _ := s.GetSession("sess-corr-reuse")
	if len(got.Violations) != 2 {
		t.Fatalf("want the carried match and the new one, got %d: %+v", len(got.Violations), got.Violations)
	}
	if got.Violations[0].Correlation == nil || got.Violations[0].Correlation.MatchID != "cm_first" ||
		got.Violations[1].Correlation == nil || got.Violations[1].Correlation.MatchID != "cm_second" {
		t.Fatalf("carried evidence lost or reordered: %+v", got.Violations)
	}
	if got.Violations[0].Correlation.EventIDs[0] != "ev_a" || got.Violations[1].Correlation.EventIDs[0] != "ev_c" {
		t.Fatalf("contributing IDs mixed up: %+v", got.Violations)
	}
}

// The conversion must carry every field: identical JSON tags on both sides.
func TestCorrelationStore_ConversionKeepsEveryFieldAndCopies(t *testing.T) {
	in := &policy.CorrelationEvidence{
		MatchID: "cm_x", Rule: "r", Shape: "sequence", Mode: "enforce",
		EventIDs: []string{"a", "b"}, Categories: []string{"c1", "c2"},
		FirstEvent: time.Now().UTC().Add(-time.Second), LastEvent: time.Now().UTC(),
	}
	out := storage.CorrelationEvidenceFromPolicy(in)
	a, _ := json.Marshal(in)
	b, _ := json.Marshal(out)
	if string(a) != string(b) {
		t.Fatalf("conversion changed the evidence:\npolicy:  %s\nstorage: %s", a, b)
	}
	out.EventIDs[0] = "tampered"
	if in.EventIDs[0] != "a" {
		t.Fatal("the conversion must copy the slices")
	}
	if storage.CorrelationEvidenceFromPolicy(nil) != nil {
		t.Fatal("nil evidence must convert to nil")
	}
}

// KEEP-style guard: no new content field anywhere in the persisted types.
func TestCorrelationStore_TypesCarryNoContentFields(t *testing.T) {
	forbidden := map[string]bool{"MatchedText": true, "SourceContent": true, "Content": true, "Text": true, "Body": true, "Arguments": true, "Pattern": true}
	for _, typ := range []reflect.Type{reflect.TypeOf(storage.CorrelationEvidence{}), reflect.TypeOf(policy.CorrelationEvidence{})} {
		for i := 0; i < typ.NumField(); i++ {
			if forbidden[typ.Field(i).Name] {
				t.Fatalf("%s has a content field %s", typ.Name(), typ.Field(i).Name)
			}
		}
	}
	// A plain violation still serializes without the key.
	raw, _ := json.Marshal(storage.Violation{RuleName: "r", Severity: "info", Action: "flag"})
	if jsonKeys(t, raw)["correlation"] {
		t.Fatalf("omitempty lost: %s", raw)
	}
}

func TestCorrelationStore_ViolationDetectedDataCarriesIdentity(t *testing.T) {
	raw, _ := json.Marshal(storage.ViolationDetectedData{
		RuleName: "correlation.r", Severity: "warning", Action: "flag", EvidenceOnly: true,
		EventID: "cm_1", EventCategory: "correlation_match", SourceRole: "correlation",
	})
	keys := jsonKeys(t, raw)
	for _, k := range []string{"event_id", "event_category", "source_role"} {
		if !keys[k] {
			t.Fatalf("violation_detected data lacks %s: %s", k, raw)
		}
	}
	raw, _ = json.Marshal(storage.ViolationDetectedData{RuleName: "r", Severity: "info", Action: "flag"})
	keys = jsonKeys(t, raw)
	if keys["event_id"] || keys["event_category"] || keys["source_role"] {
		t.Fatalf("the identity fields must be omitempty: %s", raw)
	}
}
