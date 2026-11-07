package unit

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"elida/internal/session"
	"elida/internal/storage"
)

func shadowMatch(i int, at time.Time) storage.CorrelationShadow {
	return storage.CorrelationShadow{
		Timestamp: at, MatchID: fmt.Sprintf("cm_%032x", i), Rule: "slow_burn_injection", Shape: "repeated",
		Severity: "warning", Mode: "audit", EventIDs: []string{"dec_a", "dec_b", "dec_c"},
		Categories: []string{"injection_elevated", "injection_elevated", "injection_elevated"},
		FirstEvent: at.Add(-time.Minute), LastEvent: at,
	}
}

func TestCorrelationShadowStore_PersistsAndLoadsNewestFirst(t *testing.T) {
	s := openStore(t, "cs.db")
	now := time.Now().UTC().Truncate(time.Millisecond)
	rec := baseRecord("sess-cs-store", now.Add(-time.Minute))
	rec.CorrelationShadow = []storage.CorrelationShadow{shadowMatch(2, now), shadowMatch(1, now.Add(-time.Second))}
	if err := s.SaveSession(rec); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	got, err := s.GetSession("sess-cs-store")
	if err != nil || got == nil {
		t.Fatalf("GetSession: %v %v", got, err)
	}
	if !reflect.DeepEqual(got.CorrelationShadow, rec.CorrelationShadow) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got.CorrelationShadow, rec.CorrelationShadow)
	}
	list, err := s.ListSessions(storage.ListSessionsOptions{Limit: 10})
	if err != nil || len(list) != 1 || len(list[0].CorrelationShadow) != 0 {
		t.Fatalf("ListSessions must omit correlation_shadow: %+v %v", list, err)
	}
}

// Review Focus 1: carried across ID reuse, record's own first, deduplicated
// by match ID, total capped at MaxCarriedCorrelationShadow (newest prior kept).
func TestCorrelationShadowStore_ReusedIDCarriesAndDeduplicates(t *testing.T) {
	s := openStore(t, "cs-reuse.db")
	t0 := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	first := baseRecord("sess-cs-reuse", t0)
	for i := 0; i < storage.MaxCarriedCorrelationShadow; i++ {
		first.CorrelationShadow = append(first.CorrelationShadow, shadowMatch(i, t0.Add(time.Duration(i)*time.Second)))
	}
	if err := s.SaveSession(first); err != nil {
		t.Fatalf("first: %v", err)
	}
	t1 := t0.Add(30 * time.Minute)
	second := baseRecord("sess-cs-reuse", t1)
	second.CorrelationShadow = []storage.CorrelationShadow{shadowMatch(100, t1), shadowMatch(0, t1.Add(-time.Second))} // cm_0 duplicates a prior entry
	for i := 0; i < 2; i++ {
		if err := s.SaveSession(second); err != nil {
			t.Fatalf("second save %d: %v", i, err)
		}
	}
	got, _ := s.GetSession("sess-cs-reuse")
	if len(got.CorrelationShadow) != storage.MaxCarriedCorrelationShadow {
		t.Fatalf("len = %d, want the cap %d", len(got.CorrelationShadow), storage.MaxCarriedCorrelationShadow)
	}
	if got.CorrelationShadow[0].MatchID != shadowMatch(100, t1).MatchID || got.CorrelationShadow[1].MatchID != shadowMatch(0, t1).MatchID {
		t.Fatalf("the record's own entries must come first: %q %q", got.CorrelationShadow[0].MatchID, got.CorrelationShadow[1].MatchID)
	}
	seen := map[string]int{}
	for _, c := range got.CorrelationShadow {
		seen[c.MatchID]++
	}
	if seen[shadowMatch(0, t1).MatchID] != 1 {
		t.Fatal("a carried entry with the record's own match ID must be dropped")
	}
	// Prior entries are kept head-first (newest first), so the oldest prior
	// entries fall off the end.
	if got.CorrelationShadow[2].MatchID != shadowMatch(1, t0).MatchID {
		t.Fatalf("carried order: %q", got.CorrelationShadow[2].MatchID)
	}
}

// Review Focus 2: rows from before the column, NULL and ” load as empty.
func TestCorrelationShadowStore_NullOrEmptyColumnLoadsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cs-null.db")
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
	for id, val := range map[string]any{"sess-cs-null": nil, "sess-cs-empty": ""} {
		if _, err = raw.Exec(`INSERT INTO sessions (id, state, start_time, end_time, duration_ms, backend, client_addr, correlation_shadow)
			VALUES (?, 'ended', ?, ?, 0, 'https://backend', '127.0.0.1:1', ?)`, id, now.Add(-time.Minute), now, val); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
		got, gerr := s.GetSession(id)
		if gerr != nil || got == nil || len(got.CorrelationShadow) != 0 {
			t.Fatalf("%s: %+v %v", id, got, gerr)
		}
	}
	rec := baseRecord("sess-cs-null", now.Add(time.Hour))
	rec.CorrelationShadow = []storage.CorrelationShadow{shadowMatch(9, now.Add(time.Hour))}
	if err = s.SaveSession(rec); err != nil {
		t.Fatalf("SaveSession over NULL row: %v", err)
	}
	if got, _ := s.GetSession("sess-cs-null"); len(got.CorrelationShadow) != 1 {
		t.Fatalf("reuse over NULL row: %+v", got)
	}
}

// Review Focus 2: a database created before the column gets it on reopen,
// and a second reopen is a no-op.
func TestCorrelationShadowStore_MigrationIsIdempotentAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cs-mig.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`CREATE TABLE sessions (id TEXT PRIMARY KEY, state TEXT NOT NULL, start_time DATETIME NOT NULL, end_time DATETIME NOT NULL, duration_ms INTEGER NOT NULL, request_count INTEGER NOT NULL DEFAULT 0, bytes_in INTEGER NOT NULL DEFAULT 0, bytes_out INTEGER NOT NULL DEFAULT 0, backend TEXT NOT NULL, client_addr TEXT NOT NULL, metadata TEXT, captured_content TEXT, violations TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
	for i := 0; i < 2; i++ {
		s, err := storage.NewSQLiteStore(path)
		if err != nil {
			t.Fatalf("reopen %d: %v", i, err)
		}
		rec := baseRecord("sess-cs-mig", time.Now().Add(-time.Minute))
		rec.CorrelationShadow = []storage.CorrelationShadow{shadowMatch(i, time.Now())}
		if err := s.SaveSession(rec); err != nil {
			t.Fatalf("save after reopen %d: %v", i, err)
		}
		if got, _ := s.GetSession("sess-cs-mig"); len(got.CorrelationShadow) == 0 {
			t.Fatalf("reopen %d: column missing", i)
		}
		_ = s.Close()
	}
}

func TestCorrelationShadowStore_ConversionIsFieldForField(t *testing.T) {
	in := []session.CorrelationShadow{{Timestamp: time.Now().UTC(), MatchID: "cm_1", Rule: "r", Shape: "sequence", Severity: "info", Mode: "enforce",
		EventIDs: []string{"a"}, Categories: []string{"c"}, FirstEvent: time.Now().UTC(), LastEvent: time.Now().UTC()}}
	out := storage.CorrelationShadowFromSession(in)
	a, _ := json.Marshal(in[0])
	b, _ := json.Marshal(out[0])
	if string(a) != string(b) {
		t.Fatalf("conversion changed the entry:\nsession: %s\nstorage: %s", a, b)
	}
	if storage.CorrelationShadowFromSession(nil) != nil {
		t.Fatal("empty converts to nil")
	}
	if storage.MaxCarriedCorrelationShadow != session.MaxCorrelationShadow {
		t.Fatalf("cap = %d, want session.MaxCorrelationShadow", storage.MaxCarriedCorrelationShadow)
	}
	forbidden := map[string]bool{"MatchedText": true, "SourceContent": true, "Content": true, "Text": true, "Body": true, "Arguments": true}
	typ := reflect.TypeOf(storage.CorrelationShadow{})
	for i := 0; i < typ.NumField(); i++ {
		if forbidden[typ.Field(i).Name] {
			t.Fatalf("content field %s", typ.Field(i).Name)
		}
	}
}

// The plan's merge-stress gate: many generations over a few reused session
// IDs, with a repeated re-save of some generations, must never grow a row
// past its bounds, duplicate a match, or let prior_history run away.
func TestCorrelationShadowStore_MergeStressAcrossManyGenerations(t *testing.T) {
	const (
		saves        = 200
		ids          = 6
		violsPerSave = 40 // 34 generations per ID overrun MaxCarriedViolations
		matchesPer   = 3
	)
	path := filepath.Join(t.TempDir(), "cs-stress.db")
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

	t0 := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Millisecond)
	check := func(n int, id string) {
		t.Helper()
		got, gerr := s.GetSession(id)
		if gerr != nil || got == nil {
			t.Fatalf("save %d: GetSession(%s): %v %v", n, id, got, gerr)
		}
		if len(got.Violations) > storage.MaxCarriedViolations {
			t.Fatalf("save %d: %s has %d violations, cap %d", n, id, len(got.Violations), storage.MaxCarriedViolations)
		}
		if len(got.CorrelationShadow) > storage.MaxCarriedCorrelationShadow {
			t.Fatalf("save %d: %s has %d correlation_shadow entries, cap %d", n, id, len(got.CorrelationShadow), storage.MaxCarriedCorrelationShadow)
		}
		seen := map[string]bool{}
		for _, c := range got.CorrelationShadow {
			if seen[c.MatchID] {
				t.Fatalf("save %d: %s repeats match_id %s", n, id, c.MatchID)
			}
			seen[c.MatchID] = true
		}
		var prior sql.NullString
		if qerr := raw.QueryRow(`SELECT prior_history FROM sessions WHERE id = ?`, id).Scan(&prior); qerr != nil {
			t.Fatalf("save %d: read prior_history: %v", n, qerr)
		}
		var p struct {
			Violations        []json.RawMessage `json:"violations"`
			CorrelationShadow []struct {
				MatchID string `json:"match_id"`
			} `json:"correlation_shadow"`
		}
		if prior.Valid && prior.String != "" {
			if uerr := json.Unmarshal([]byte(prior.String), &p); uerr != nil {
				t.Fatalf("save %d: prior_history is not JSON: %v", n, uerr)
			}
		}
		if len(p.Violations) > storage.MaxCarriedViolations || len(p.CorrelationShadow) > storage.MaxCarriedCorrelationShadow {
			t.Fatalf("save %d: %s prior_history unbounded: %d violations, %d matches", n, id, len(p.Violations), len(p.CorrelationShadow))
		}
		pseen := map[string]bool{}
		for _, c := range p.CorrelationShadow {
			if pseen[c.MatchID] {
				t.Fatalf("save %d: %s prior_history repeats match_id %s", n, id, c.MatchID)
			}
			pseen[c.MatchID] = true
		}
	}

	for n := 0; n < saves; n++ {
		id := fmt.Sprintf("sess-cs-stress-%d", n%ids)
		gen := n / ids
		start := t0.Add(time.Duration(gen) * time.Minute)
		rec := baseRecord(id, start)
		for v := 0; v < violsPerSave; v++ {
			rec.Violations = append(rec.Violations, storage.Violation{
				RuleName: "r", Severity: "warning", Action: "flag",
				EventID: fmt.Sprintf("ev_%d_%d_%d", n%ids, gen, v),
			})
		}
		for m := 0; m < matchesPer; m++ {
			rec.CorrelationShadow = append(rec.CorrelationShadow, shadowMatch(1000*(n%ids)+gen*matchesPer+m, start.Add(time.Duration(m)*time.Second)))
		}
		// A match ID that recurs in every generation must still appear once.
		rec.CorrelationShadow = append(rec.CorrelationShadow, shadowMatch(999999+n%ids, start))
		if err := s.SaveSession(rec); err != nil {
			t.Fatalf("save %d: %v", n, err)
		}
		check(n, id)
		if n%4 == 0 { // the same generation saved again must not grow the row
			if err := s.SaveSession(rec); err != nil {
				t.Fatalf("re-save %d: %v", n, err)
			}
			check(n, id)
		}
	}

	// The stress must actually have hit both caps.
	got, _ := s.GetSession("sess-cs-stress-0")
	if len(got.Violations) != storage.MaxCarriedViolations || len(got.CorrelationShadow) != storage.MaxCarriedCorrelationShadow {
		t.Fatalf("caps not exercised: %d violations, %d matches", len(got.Violations), len(got.CorrelationShadow))
	}
}
