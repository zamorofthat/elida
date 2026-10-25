package unit

import (
	"fmt"
	"testing"
	"time"

	"elida/internal/storage"
)

func historyRecord(id string, start time.Time) storage.SessionRecord {
	return storage.SessionRecord{
		ID: id, State: "completed", StartTime: start, EndTime: start.Add(time.Second),
		Backend: "default", ClientAddr: "127.0.0.1:1",
	}
}

func mustSave(t *testing.T, s *storage.SQLiteStore, r storage.SessionRecord) {
	t.Helper()
	if err := s.SaveSession(r); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
}

func mustGet(t *testing.T, s *storage.SQLiteStore, id string) *storage.SessionRecord {
	t.Helper()
	rec, err := s.GetSession(id)
	if err != nil || rec == nil {
		t.Fatalf("GetSession: rec=%v err=%v", rec, err)
	}
	return rec
}

func TestStorageHistory_SameSessionResaveReplaces(t *testing.T) {
	s := newTestStore(t)
	start := time.Now().UTC().Truncate(time.Microsecond)
	r := historyRecord("sess-same", start)
	r.Violations = []storage.Violation{{RuleName: "a", EventID: "ev1"}}
	mustSave(t, s, r)
	// The proxy's flagged save, then the session-end save of the SAME
	// session: a replace, not an accumulation.
	r.Violations = []storage.Violation{{RuleName: "a", EventID: "ev2"}}
	mustSave(t, s, r)
	if got := mustGet(t, s, "sess-same").Violations; len(got) != 1 || got[0].EventID != "ev2" {
		t.Fatalf("a resave of one session must replace its own entries, got %+v", got)
	}
}

func TestStorageHistory_ReusedIDMergesAndKeepsPriorAcrossResaves(t *testing.T) {
	s := newTestStore(t)
	t1 := time.Now().UTC().Truncate(time.Microsecond)
	first := historyRecord("sess-merge", t1)
	first.Violations = []storage.Violation{{RuleName: "probe_rule", EventID: "ev-old", Severity: "warning"}}
	first.CapturedContent = []storage.CapturedRequest{{Timestamp: t1, Method: "POST", Path: "/v1", RequestBody: "old"}}
	first.SemanticShadow = []storage.SemanticShadow{{DecisionID: "d-old", Timestamp: t1}}
	mustSave(t, s, first)

	t2 := t1.Add(time.Minute)
	second := historyRecord("sess-merge", t2)
	second.Violations = []storage.Violation{
		{RuleName: "probe_rule", EventID: "ev-old", Severity: "warning"}, // a retained entry re-sent
		{RuleName: "other", EventID: "ev-new"},
	}
	second.CapturedContent = []storage.CapturedRequest{{Timestamp: t2, Method: "POST", Path: "/v1", RequestBody: "new"}}
	second.SemanticShadow = []storage.SemanticShadow{{DecisionID: "d-new", Timestamp: t2}, {DecisionID: "d-old", Timestamp: t1}}
	// Saved twice, as the proxy and the session-end callback both do.
	mustSave(t, s, second)
	mustSave(t, s, second)

	rec := mustGet(t, s, "sess-merge")
	if !rec.StartTime.Equal(t2) {
		t.Fatalf("the row describes the newest session, start=%v", rec.StartTime)
	}
	var ids []string
	for _, v := range rec.Violations {
		ids = append(ids, v.EventID)
	}
	if fmt.Sprint(ids) != "[ev-old ev-new]" {
		t.Fatalf("violations = %v, want the old one once (deduplicated by EventID) then the new", ids)
	}
	if len(rec.CapturedContent) != 2 || rec.CapturedContent[0].RequestBody != "old" || rec.CapturedContent[1].RequestBody != "new" {
		t.Fatalf("captures = %+v", rec.CapturedContent)
	}
	if len(rec.SemanticShadow) != 2 || rec.SemanticShadow[0].DecisionID != "d-new" || rec.SemanticShadow[1].DecisionID != "d-old" {
		t.Fatalf("shadow = %+v, want newest first, deduplicated by DecisionID", rec.SemanticShadow)
	}

	// A third session carries both earlier ones.
	third := historyRecord("sess-merge", t2.Add(time.Minute))
	third.Violations = []storage.Violation{{RuleName: "probe_rule", EventID: "ev-third"}}
	mustSave(t, s, third)
	ids = nil
	for _, v := range mustGet(t, s, "sess-merge").Violations {
		ids = append(ids, v.EventID)
	}
	if fmt.Sprint(ids) != "[ev-old ev-new ev-third]" {
		t.Fatalf("violations = %v", ids)
	}
}

func TestStorageHistory_LegacyViolationsWithoutEventIDDeduplicate(t *testing.T) {
	s := newTestStore(t)
	t1 := time.Now().UTC().Truncate(time.Microsecond)
	v := storage.Violation{RuleName: "legacy", Severity: "info", Description: "d", SourceRole: "user"}
	first := historyRecord("sess-legacy", t1)
	first.Violations = []storage.Violation{v}
	mustSave(t, s, first)
	second := historyRecord("sess-legacy", t1.Add(time.Minute))
	second.Violations = []storage.Violation{v}
	mustSave(t, s, second)
	if got := mustGet(t, s, "sess-legacy").Violations; len(got) != 1 {
		t.Fatalf("identical violations without EventID must merge to one, got %d", len(got))
	}
}

func TestStorageHistory_CarriedHistoryIsBounded(t *testing.T) {
	s := newTestStore(t)
	t0 := time.Now().UTC().Truncate(time.Microsecond)
	const sessions = 3
	per := storage.MaxCarriedCaptures // each session alone fills the cap
	for i := 0; i < sessions; i++ {
		start := t0.Add(time.Duration(i) * time.Hour)
		r := historyRecord("sess-cap", start)
		for j := 0; j < per; j++ {
			ts := start.Add(time.Duration(j) * time.Millisecond)
			r.CapturedContent = append(r.CapturedContent, storage.CapturedRequest{Timestamp: ts, Method: "POST", Path: fmt.Sprintf("/%d/%d", i, j)})
			r.SemanticShadow = append(r.SemanticShadow, storage.SemanticShadow{DecisionID: fmt.Sprintf("d-%d-%d", i, j)})
		}
		r.SemanticShadow = r.SemanticShadow[:storage.MaxCarriedShadow]
		mustSave(t, s, r)
	}
	rec := mustGet(t, s, "sess-cap")
	if len(rec.CapturedContent) != storage.MaxCarriedCaptures {
		t.Fatalf("captures = %d, want the cap %d", len(rec.CapturedContent), storage.MaxCarriedCaptures)
	}
	if rec.CapturedContent[0].Path != "/2/0" {
		t.Fatalf("a full own list leaves no room for carried entries; first = %s", rec.CapturedContent[0].Path)
	}
	if len(rec.SemanticShadow) != storage.MaxCarriedShadow {
		t.Fatalf("shadow = %d, want the cap %d", len(rec.SemanticShadow), storage.MaxCarriedShadow)
	}

	// A small session after them carries the newest earlier captures, up to
	// the cap.
	last := historyRecord("sess-cap", t0.Add(10*time.Hour))
	last.CapturedContent = []storage.CapturedRequest{{Timestamp: t0.Add(10 * time.Hour), Method: "POST", Path: "/last"}}
	mustSave(t, s, last)
	rec = mustGet(t, s, "sess-cap")
	n := len(rec.CapturedContent)
	if n != storage.MaxCarriedCaptures || rec.CapturedContent[n-1].Path != "/last" || rec.CapturedContent[n-2].Path != fmt.Sprintf("/2/%d", per-1) {
		t.Fatalf("captures = %d, last two %s %s", n, rec.CapturedContent[n-2].Path, rec.CapturedContent[n-1].Path)
	}
}
