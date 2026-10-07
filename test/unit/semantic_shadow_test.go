package unit

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"elida/internal/control"
	"elida/internal/session"
	"elida/internal/storage"

	_ "modernc.org/sqlite"
)

func shadowEntry(i int) session.SemanticShadow {
	return session.SemanticShadow{
		Timestamp:        time.Now().Add(time.Duration(i) * time.Millisecond),
		DecisionID:       "dec_" + string(rune('a'+i%26)),
		Signal:           "injection",
		Probability:      0.5 + float64(i)/1000,
		AuxProbability:   0.1,
		SourceRole:       "user",
		MessageIndex:     i,
		WindowStartByte:  0,
		WindowEndByte:    100,
		Model:            "minilm-multihead",
		ModelVersion:     "v5-fp32",
		ThresholdSet:     "v1",
		ExecutionMode:    "inline",
		ProtectionScope:  "current_request",
		CoverageComplete: true,
		LatencyMs:        7,
	}
}

func TestSemanticShadow_NewestFirstAndCapped(t *testing.T) {
	s := session.NewSession("sess-shadow", "https://backend", "127.0.0.1:1234")
	for i := 0; i < session.MaxSemanticShadow+20; i++ {
		s.RecordSemanticShadow(shadowEntry(i))
	}
	got := s.GetSemanticShadow()
	if len(got) != session.MaxSemanticShadow {
		t.Fatalf("len = %d, want the cap %d", len(got), session.MaxSemanticShadow)
	}
	// Newest first: the last entry recorded is at index 0.
	if got[0].MessageIndex != session.MaxSemanticShadow+19 {
		t.Fatalf("got[0].MessageIndex = %d, want the newest (%d)", got[0].MessageIndex, session.MaxSemanticShadow+19)
	}
	for i := 1; i < len(got); i++ {
		if got[i].MessageIndex >= got[i-1].MessageIndex {
			t.Fatalf("entries are not newest-first at %d: %d then %d", i, got[i-1].MessageIndex, got[i].MessageIndex)
		}
	}
}

func TestSemanticShadow_GetReturnsACopy(t *testing.T) {
	s := session.NewSession("sess-copy", "https://backend", "127.0.0.1:1")
	s.RecordSemanticShadow(shadowEntry(1))
	got := s.GetSemanticShadow()
	got[0].Probability = 999
	if again := s.GetSemanticShadow(); again[0].Probability == 999 {
		t.Fatal("GetSemanticShadow must return a copy, not the live slice")
	}
}

func TestSemanticShadow_SnapshotCopiesTheList(t *testing.T) {
	s := session.NewSession("sess-snap", "https://backend", "127.0.0.1:1")
	s.RecordSemanticShadow(shadowEntry(1))
	s.RecordSemanticShadow(shadowEntry(2))

	snap := s.Snapshot()
	if len(snap.SemanticShadow) != 2 {
		t.Fatalf("snapshot carries %d entries, want 2", len(snap.SemanticShadow))
	}
	snap.SemanticShadow[0].Probability = 999
	if s.GetSemanticShadow()[0].Probability == 999 {
		t.Fatal("Snapshot must deep-copy the shadow list")
	}
}

func TestSemanticShadow_ConcurrentRecording(t *testing.T) {
	s := session.NewSession("sess-conc", "https://backend", "127.0.0.1:1")
	done := make(chan struct{})
	for g := 0; g < 8; g++ {
		go func(g int) {
			for i := 0; i < 50; i++ {
				s.RecordSemanticShadow(shadowEntry(g*50 + i))
				_ = s.GetSemanticShadow()
			}
			done <- struct{}{}
		}(g)
	}
	for g := 0; g < 8; g++ {
		<-done
	}
	if n := len(s.GetSemanticShadow()); n != session.MaxSemanticShadow {
		t.Fatalf("len = %d, want the cap %d", n, session.MaxSemanticShadow)
	}
}

func TestSemanticShadow_PersistsAndLoadsFromSQLite(t *testing.T) {
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	rec := storage.SessionRecord{
		ID:         "sess-persist",
		State:      "ended",
		StartTime:  time.Now().Add(-time.Minute),
		EndTime:    time.Now(),
		DurationMs: 60000,
		Backend:    "https://backend",
		ClientAddr: "127.0.0.1:1",
		SemanticShadow: []storage.SemanticShadow{
			{
				Timestamp: time.Now(), DecisionID: "dec_abc", Signal: "injection",
				Probability: 0.81, AuxProbability: 0.04, SourceRole: "tool", MessageIndex: 3,
				Transform: "base64_decode", TransformDepth: 1,
				WindowStartByte: 10, WindowEndByte: 210,
				Model: "minilm-multihead", ModelVersion: "v5-fp32", ModelChecksum: "abc123",
				ThresholdSet: "v1", ExecutionMode: "async", ProtectionScope: "future_activity",
				CoverageComplete: false, LatencyMs: 22,
			},
		},
	}
	if err = store.SaveSession(rec); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	got, err := store.GetSession("sess-persist")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got == nil {
		t.Fatal("GetSession returned nil")
	}
	if len(got.SemanticShadow) != 1 {
		t.Fatalf("loaded %d shadow entries, want 1", len(got.SemanticShadow))
	}
	sh := got.SemanticShadow[0]
	if sh.DecisionID != "dec_abc" || sh.Probability != 0.81 || sh.Transform != "base64_decode" {
		t.Fatalf("round-trip lost fields: %+v", sh)
	}
	if sh.ProtectionScope != "future_activity" || sh.ExecutionMode != "async" {
		t.Fatalf("round-trip lost scope/mode: %+v", sh)
	}
	if sh.CoverageComplete {
		t.Fatal("round-trip flipped CoverageComplete")
	}
}

func TestSemanticShadow_OldRowsWithoutTheColumnStillLoad(t *testing.T) {
	// The column is added by an idempotent ALTER, so a database written
	// before this task must still read back cleanly with an empty list.
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	rec := storage.SessionRecord{
		ID: "sess-legacy", State: "ended",
		StartTime: time.Now().Add(-time.Minute), EndTime: time.Now(),
		Backend: "https://backend", ClientAddr: "127.0.0.1:1",
	}
	if err = store.SaveSession(rec); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	got, err := store.GetSession("sess-legacy")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if len(got.SemanticShadow) != 0 {
		t.Fatalf("a record with no shadow entries must load an empty list, got %d", len(got.SemanticShadow))
	}
}

func TestSemanticShadow_ExposedOnTheSessionDetailAPI(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)
	sess := manager.GetOrCreate("sess-api", "https://backend", "127.0.0.1:1")
	sess.RecordSemanticShadow(shadowEntry(1))
	sess.RecordSemanticShadow(shadowEntry(2))

	h := control.New(store, manager)

	// Detail path carries the entries.
	req := httptest.NewRequest(http.MethodGet, "/control/sessions/sess-api", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body = %s", w.Code, w.Body.String())
	}
	var detail struct {
		SemanticShadow []session.SemanticShadow `json:"semantic_shadow"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatalf("unmarshal detail: %v", err)
	}
	if len(detail.SemanticShadow) != 2 {
		t.Fatalf("detail carries %d shadow entries, want 2: %s", len(detail.SemanticShadow), w.Body.String())
	}
	if detail.SemanticShadow[0].Signal != "injection" {
		t.Fatalf("shadow entry lost its signal: %+v", detail.SemanticShadow[0])
	}

	// List path does NOT: fifty entries per session would bloat every list
	// response, and calibration review happens one session at a time.
	reqList := httptest.NewRequest(http.MethodGet, "/control/sessions", nil)
	wList := httptest.NewRecorder()
	h.ServeHTTP(wList, reqList)
	if wList.Code != http.StatusOK {
		t.Fatalf("list status = %d", wList.Code)
	}
	var list struct {
		Sessions []struct {
			SemanticShadow []session.SemanticShadow `json:"semantic_shadow"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(wList.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	for _, s := range list.Sessions {
		if len(s.SemanticShadow) != 0 {
			t.Fatal("the session list must not carry shadow entries")
		}
	}
}

// allowedShadowStringFields enumerates every string field a shadow entry may
// carry. Each is an identifier, a label or model metadata; none can hold
// request content. A new string (or free-form) field must be justified here.
var allowedShadowStringFields = map[string]bool{
	"DecisionID": true, "Signal": true, "SourceRole": true, "Transform": true,
	"Model": true, "ModelVersion": true, "ModelChecksum": true,
	"ThresholdSet": true, "ExecutionMode": true, "ProtectionScope": true,
}

func assertNoContentFields(t *testing.T, typ reflect.Type) {
	t.Helper()
	timeType := reflect.TypeOf(time.Time{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		switch f.Type.Kind() {
		case reflect.String:
			if !allowedShadowStringFields[f.Name] {
				t.Errorf("%s.%s is a string field not in the allowlist; shadow entries must not carry content", typ, f.Name)
			}
		case reflect.Float64, reflect.Int, reflect.Int64, reflect.Bool:
		case reflect.Struct:
			if f.Type != timeType {
				t.Errorf("%s.%s has unexpected struct type %s", typ, f.Name, f.Type)
			}
		default:
			t.Errorf("%s.%s has kind %s, which could carry content", typ, f.Name, f.Type.Kind())
		}
	}
}

func TestSemanticShadow_CarriesNoContentFields(t *testing.T) {
	assertNoContentFields(t, reflect.TypeOf(session.SemanticShadow{}))
	assertNoContentFields(t, reflect.TypeOf(storage.SemanticShadow{}))

	// The storage mirror must stay field-for-field identical to the session type.
	a, b := reflect.TypeOf(session.SemanticShadow{}), reflect.TypeOf(storage.SemanticShadow{})
	if a.NumField() != b.NumField() {
		t.Fatalf("session has %d fields, storage mirror has %d", a.NumField(), b.NumField())
	}
	for i := 0; i < a.NumField(); i++ {
		fa, fb := a.Field(i), b.Field(i)
		if fa.Name != fb.Name || fa.Type != fb.Type || fa.Tag != fb.Tag {
			t.Errorf("field %d differs: session %s %s %q, storage %s %s %q", i, fa.Name, fa.Type, fa.Tag, fb.Name, fb.Type, fb.Tag)
		}
	}
}

func TestSemanticShadow_MigrationIsIdempotentAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reopen.db")
	for round := 0; round < 2; round++ {
		store, err := storage.NewSQLiteStore(path)
		if err != nil {
			t.Fatalf("round %d: NewSQLiteStore: %v", round, err)
		}
		rec := storage.SessionRecord{
			ID: "sess-reopen", State: "ended",
			StartTime: time.Now().Add(-time.Minute), EndTime: time.Now(),
			Backend: "https://backend", ClientAddr: "127.0.0.1:1",
			SemanticShadow: []storage.SemanticShadow{{DecisionID: "dec_r", Signal: "injection", Probability: 0.7}},
		}
		if err = store.SaveSession(rec); err != nil {
			t.Fatalf("round %d: SaveSession: %v", round, err)
		}
		if err = store.SaveSession(rec); err != nil {
			t.Fatalf("round %d: second SaveSession: %v", round, err)
		}
		got, err := store.GetSession("sess-reopen")
		if err != nil || got == nil {
			t.Fatalf("round %d: GetSession: %v, %v", round, got, err)
		}
		if len(got.SemanticShadow) != 1 || got.SemanticShadow[0].DecisionID != "dec_r" {
			t.Fatalf("round %d: shadow = %+v", round, got.SemanticShadow)
		}
		_ = store.Close()
	}
}

func TestSemanticShadow_StorageListOmitsShadow(t *testing.T) {
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "list.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	rec := storage.SessionRecord{
		ID: "sess-list", State: "ended",
		StartTime: time.Now().Add(-time.Minute), EndTime: time.Now(),
		Backend: "https://backend", ClientAddr: "127.0.0.1:1",
		SemanticShadow: []storage.SemanticShadow{{DecisionID: "dec_l", Signal: "injection"}},
	}
	if err = store.SaveSession(rec); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	list, err := store.ListSessions(storage.ListSessionsOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("listed %d sessions, want 1", len(list))
	}
	if len(list[0].SemanticShadow) != 0 {
		t.Fatal("the list query must not load shadow entries; only GetSession does")
	}
}

func TestSemanticShadow_PreExistingDatabaseAndNullColumnLoadEmpty(t *testing.T) {
	// Build a database with the sessions table as it was before the
	// semantic_shadow column existed, then let NewSQLiteStore migrate it.
	path := filepath.Join(t.TempDir(), "pre.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err = raw.Exec(`CREATE TABLE sessions (
		id TEXT PRIMARY KEY, state TEXT NOT NULL, start_time DATETIME NOT NULL,
		end_time DATETIME NOT NULL, duration_ms INTEGER NOT NULL,
		request_count INTEGER NOT NULL DEFAULT 0, bytes_in INTEGER NOT NULL DEFAULT 0,
		bytes_out INTEGER NOT NULL DEFAULT 0, backend TEXT NOT NULL, client_addr TEXT NOT NULL,
		metadata TEXT, captured_content TEXT, violations TEXT,
		fingerprint_distance REAL DEFAULT 0, fingerprint_bucket TEXT DEFAULT '',
		fingerprint_class TEXT DEFAULT '', created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	now := time.Now()
	if _, err = raw.Exec(`INSERT INTO sessions (id, state, start_time, end_time, duration_ms, backend, client_addr)
		VALUES ('sess-pre', 'ended', ?, ?, 0, 'https://backend', '127.0.0.1:1')`, now.Add(-time.Minute), now); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	_ = raw.Close()

	store, err := storage.NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore on legacy db: %v", err)
	}
	defer func() { _ = store.Close() }()

	got, err := store.GetSession("sess-pre")
	if err != nil || got == nil {
		t.Fatalf("GetSession legacy row: %v, %v", got, err)
	}
	if len(got.SemanticShadow) != 0 {
		t.Fatalf("legacy row loaded %d shadow entries, want 0", len(got.SemanticShadow))
	}

	// An explicit NULL in the migrated column must also load as empty.
	raw, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = raw.Close() }()
	if _, err = raw.Exec(`INSERT INTO sessions (id, state, start_time, end_time, duration_ms, backend, client_addr, semantic_shadow)
		VALUES ('sess-null', 'ended', ?, ?, 0, 'https://backend', '127.0.0.1:1', NULL)`, now.Add(-time.Minute), now); err != nil {
		t.Fatalf("insert NULL row: %v", err)
	}
	got, err = store.GetSession("sess-null")
	if err != nil || got == nil {
		t.Fatalf("GetSession NULL row: %v, %v", got, err)
	}
	if len(got.SemanticShadow) != 0 {
		t.Fatalf("NULL column loaded %d shadow entries, want 0", len(got.SemanticShadow))
	}
}
