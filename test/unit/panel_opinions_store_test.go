package unit

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"elida/internal/storage"
)

func toolChainOpinion(pct float64) storage.PanelOpinion {
	return storage.PanelOpinion{
		Member: "tool-chain", Shadow: true, Weight: 1, Anomaly: pct,
		Perplexity: 3.25, Percentile: pct, ClassUsed: "global",
	}
}

func m3LiteOpinion(bucket string, dist float64) storage.PanelOpinion {
	return storage.PanelOpinion{
		Member: "m3-lite", Weight: 1, Anomaly: 0.25,
		Bucket: bucket, Distance: dist,
	}
}

func TestPanelOpinionsStore_PersistsAndLoads(t *testing.T) {
	s := openStore(t, "po.db")
	now := time.Now().UTC().Truncate(time.Millisecond)
	rec := baseRecord("sess-po-store", now.Add(-time.Minute))
	rec.PanelOpinions = []storage.PanelOpinion{m3LiteOpinion("notable", 4.5), toolChainOpinion(0.97)}
	if err := s.SaveSession(rec); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	got, err := s.GetSession("sess-po-store")
	if err != nil || got == nil {
		t.Fatalf("GetSession: %v %v", got, err)
	}
	if !reflect.DeepEqual(got.PanelOpinions, rec.PanelOpinions) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got.PanelOpinions, rec.PanelOpinions)
	}
	list, err := s.ListSessions(storage.ListSessionsOptions{Limit: 10})
	if err != nil || len(list) != 1 || list[0].PanelOpinions != nil {
		t.Fatalf("ListSessions must omit panel_opinions: %+v %v", list, err)
	}
}

func TestPanelOpinionsStore_NoOpinionsLoadsNil(t *testing.T) {
	s := openStore(t, "po-none.db")
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.SaveSession(baseRecord("sess-po-none", now.Add(-time.Minute))); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	got, err := s.GetSession("sess-po-none")
	if err != nil || got == nil {
		t.Fatalf("GetSession: %v %v", got, err)
	}
	if got.PanelOpinions != nil {
		t.Fatalf("a record with no opinions must load as nil: %+v", got.PanelOpinions)
	}
	// An empty list keeps the column empty rather than storing "null"/"[]".
	var col sql.NullString
	if err := s.DB().QueryRow(`SELECT panel_opinions FROM sessions WHERE id = ?`, "sess-po-none").Scan(&col); err != nil {
		t.Fatalf("read panel_opinions: %v", err)
	}
	if col.Valid && col.String != "" {
		t.Fatalf("empty opinions stored as %q, want the empty string", col.String)
	}
}

// Ruling R3: on session-ID reuse the latest instance's opinions replace the
// earlier instance's. They are this session instance's assessment, so they are
// never merged or carried the way violations and shadow matches are.
func TestPanelOpinionsStore_ReusedIDReplacesEarlierOpinions(t *testing.T) {
	s := openStore(t, "po-reuse.db")
	t0 := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	first := baseRecord("sess-po-reuse", t0)
	first.PanelOpinions = []storage.PanelOpinion{m3LiteOpinion("severe", 9.5), toolChainOpinion(0.99)}
	if err := s.SaveSession(first); err != nil {
		t.Fatalf("first: %v", err)
	}
	second := baseRecord("sess-po-reuse", t0.Add(30*time.Minute))
	second.PanelOpinions = []storage.PanelOpinion{m3LiteOpinion("normal", 0.5)}
	if err := s.SaveSession(second); err != nil {
		t.Fatalf("second: %v", err)
	}
	got, _ := s.GetSession("sess-po-reuse")
	if !reflect.DeepEqual(got.PanelOpinions, second.PanelOpinions) {
		t.Fatalf("the latest instance's opinions must replace the earlier ones, not merge:\n got %+v\nwant %+v", got.PanelOpinions, second.PanelOpinions)
	}
	// The same instance re-saved (the flagged-session save then the
	// session-end save) replaces too, never appends.
	if err := s.SaveSession(second); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	if got, _ = s.GetSession("sess-po-reuse"); len(got.PanelOpinions) != 1 {
		t.Fatalf("re-save grew the list: %+v", got.PanelOpinions)
	}
	// Carried history keeps the record's violations, so the replace is
	// specific to panel_opinions rather than a row-wide overwrite.
	third := baseRecord("sess-po-reuse", t0.Add(time.Hour))
	third.Violations = []storage.Violation{{RuleName: "r", Severity: "warning", Action: "flag", EventID: "ev_1"}}
	if err := s.SaveSession(third); err != nil {
		t.Fatalf("third: %v", err)
	}
	if got, _ = s.GetSession("sess-po-reuse"); got.PanelOpinions != nil {
		t.Fatalf("an instance with no opinions must clear the stored ones: %+v", got.PanelOpinions)
	}
}

// Rows written before the column exists (NULL) and rows with ” load as no
// opinions, and a save over such a row works.
func TestPanelOpinionsStore_NullOrEmptyColumnLoadsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "po-null.db")
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
	for id, val := range map[string]any{"sess-po-null": nil, "sess-po-empty": ""} {
		if _, err = raw.Exec(`INSERT INTO sessions (id, state, start_time, end_time, duration_ms, backend, client_addr, panel_opinions)
			VALUES (?, 'ended', ?, ?, 0, 'https://backend', '127.0.0.1:1', ?)`, id, now.Add(-time.Minute), now, val); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
		got, gerr := s.GetSession(id)
		if gerr != nil || got == nil || len(got.PanelOpinions) != 0 {
			t.Fatalf("%s: %+v %v", id, got, gerr)
		}
	}
	rec := baseRecord("sess-po-null", now.Add(time.Hour))
	rec.PanelOpinions = []storage.PanelOpinion{m3LiteOpinion("minor", 2)}
	if err = s.SaveSession(rec); err != nil {
		t.Fatalf("SaveSession over NULL row: %v", err)
	}
	if got, _ := s.GetSession("sess-po-null"); len(got.PanelOpinions) != 1 {
		t.Fatalf("reuse over NULL row: %+v", got)
	}
}

// A database created before the column gets it on reopen, and a second
// reopen is a no-op.
func TestPanelOpinionsStore_MigrationIsIdempotentAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "po-mig.db")
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
		rec := baseRecord("sess-po-mig", time.Now().Add(-time.Minute))
		rec.PanelOpinions = []storage.PanelOpinion{m3LiteOpinion("notable", float64(i))}
		if err := s.SaveSession(rec); err != nil {
			t.Fatalf("save after reopen %d: %v", i, err)
		}
		if got, _ := s.GetSession("sess-po-mig"); len(got.PanelOpinions) == 0 {
			t.Fatalf("reopen %d: column missing", i)
		}
		_ = s.Close()
	}
}

// The persisted opinion is a fixed allowlist of numbers, flags and short
// enums: no map, interface or slice field can carry request content, and the
// only string fields are the member name and the three short enums.
func TestPanelOpinion_IsAFixedContentFreeStruct(t *testing.T) {
	allowedStrings := map[string]bool{"Member": true, "Bucket": true, "ClassUsed": true, "Reason": true}
	typ := reflect.TypeOf(storage.PanelOpinion{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		switch f.Type.Kind() {
		case reflect.Map, reflect.Interface, reflect.Slice, reflect.Pointer, reflect.Struct, reflect.Array:
			t.Fatalf("field %s is %s: an open-ended field could carry content", f.Name, f.Type.Kind())
		case reflect.String:
			if !allowedStrings[f.Name] {
				t.Fatalf("field %s is a string outside the allowlist %v", f.Name, allowedStrings)
			}
		case reflect.Float64, reflect.Bool:
		default:
			t.Fatalf("field %s has unexpected kind %s", f.Name, f.Type.Kind())
		}
	}
	// The JSON form is just as narrow: marshaling a fully-populated opinion
	// emits exactly the allowlisted keys.
	raw, err := json.Marshal(storage.PanelOpinion{
		Member: "tool-chain", Shadow: true, Weight: 1, Anomaly: 1, Veto: true,
		Bucket: "severe", Distance: 9, Perplexity: 3, Percentile: 0.99,
		ClassUsed: "global", Reason: "no-chain",
	})
	if err != nil {
		t.Fatal(err)
	}
	var keyed map[string]any
	if err := json.Unmarshal(raw, &keyed); err != nil {
		t.Fatal(err)
	}
	want := []string{"member", "shadow", "weight", "anomaly", "veto", "bucket", "distance", "perplexity", "percentile", "class_used", "reason"}
	if len(keyed) != len(want) {
		t.Fatalf("JSON keys = %v, want exactly %v", keyed, want)
	}
	for _, k := range want {
		if _, ok := keyed[k]; !ok {
			t.Fatalf("JSON key %q missing from %s", k, raw)
		}
	}
	if strings.Contains(string(raw), "detail") {
		t.Fatalf("the opinion must not carry a detail blob: %s", raw)
	}
}
