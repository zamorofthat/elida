package unit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"elida/internal/control"
	"elida/internal/instruction"
	"elida/internal/session"
	"elida/internal/storage"
)

func keysOf(t *testing.T, raw []byte) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode: %v\n%s", err, raw)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func newProvenanceControl(t *testing.T, store *storage.SQLiteStore, opts ...control.Option) (*control.Handler, *session.Manager) {
	t.Helper()
	mem := session.NewMemoryStore()
	manager := session.NewManager(mem, 5*time.Minute)
	if store != nil {
		opts = append(opts, control.WithHistory(store))
	}
	return control.New(mem, manager, opts...), manager
}

func getBody(h http.Handler, method, url string) (int, []byte) {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, url, nil))
	return w.Code, w.Body.Bytes()
}

func TestControlStatsKeepsItsKeys(t *testing.T) {
	h, manager := newProvenanceControl(t, nil)
	base, _ := json.Marshal(manager.Stats())
	wantKeys := keysOf(t, base)

	code, body := getBody(h, "GET", "/control/stats")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if got := keysOf(t, body); !reflect.DeepEqual(got, wantKeys) {
		t.Fatalf("stats keys changed without the option:\n got %v\nwant %v", got, wantKeys)
	}

	reg := instruction.NewRegistry(nil, nil, 4)
	t.Cleanup(reg.Stop)
	h2, _ := newProvenanceControl(t, nil, control.WithInstructionRegistry(reg))
	_, body = getBody(h2, "GET", "/control/stats")
	want := append(append([]string{}, wantKeys...), "instruction_integrity")
	sort.Strings(want)
	if got := keysOf(t, body); !reflect.DeepEqual(got, want) {
		t.Fatalf("stats keys with registry:\n got %v\nwant %v", got, want)
	}
	var env map[string]json.RawMessage
	_ = json.Unmarshal(body, &env)
	wantII := []string{"changed_since_last_seen", "changes", "fallback_sessions", "identified_sessions", "suppressed_findings", "tracked_paths"}
	if got := keysOf(t, env["instruction_integrity"]); !reflect.DeepEqual(got, wantII) {
		t.Fatalf("instruction_integrity keys: got %v want %v", got, wantII)
	}
}

func TestControlInstructionPaths(t *testing.T) {
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	t0 := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	rows := []instruction.PathVersion{
		{SourcePath: "/r/CLAUDE.md", Hash: "h2", PrevHash: "h1", FirstSeen: t0.Add(10 * time.Minute), LastSeen: t0.Add(30 * time.Minute),
			ChangeType: instruction.VersionInSession, Caused: true, CauseTool: "Edit", DiffKnown: true, LinesAdded: 2, LinesRemoved: 1},
		{SourcePath: "/r/CLAUDE.md", Hash: "h1", FirstSeen: t0, LastSeen: t0.Add(5 * time.Minute), ChangeType: instruction.VersionFirstSeen},
		{SourcePath: "/r/AGENTS.md", Hash: "a1", FirstSeen: t0, LastSeen: t0.Add(time.Minute), ChangeType: instruction.VersionFirstSeen},
	}
	for _, r := range rows {
		if err := store.SaveInstructionPathVersion(r); err != nil {
			t.Fatal(err)
		}
	}
	h, _ := newProvenanceControl(t, store)

	code, body := getBody(h, "GET", "/control/instructions/paths")
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var resp struct {
		Paths []struct {
			SourcePath string `json:"source_path"`
			Versions   []struct {
				Hash string `json:"hash"`
			} `json:"versions"`
		} `json:"paths"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 2 || len(resp.Paths) != 2 {
		t.Fatalf("total %d paths %d, want 2: %s", resp.Total, len(resp.Paths), body)
	}
	for _, p := range resp.Paths {
		if p.SourcePath == "/r/CLAUDE.md" {
			if len(p.Versions) != 2 || p.Versions[0].Hash != "h1" || p.Versions[1].Hash != "h2" {
				t.Fatalf("versions not oldest first: %+v", p.Versions)
			}
		}
	}
	for _, banned := range []string{"content", "pattern", "matched"} {
		if strings.Contains(string(body), banned) {
			t.Errorf("body contains %q: %s", banned, body)
		}
	}

	_, body = getBody(h, "GET", "/control/instructions/paths?limit=1")
	_ = json.Unmarshal(body, &resp)
	n := 0
	for _, p := range resp.Paths {
		n += len(p.Versions)
	}
	if n != 1 {
		t.Fatalf("limit=1 returned %d versions: %s", n, body)
	}

	if postCode, _ := getBody(h, "POST", "/control/instructions/paths"); postCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST: %d, want 405", postCode)
	}

	hNone, _ := newProvenanceControl(t, nil)
	code, body = getBody(hNone, "GET", "/control/instructions/paths")
	if code != 200 || strings.TrimSpace(string(body)) != `{"paths":[],"total":0}` {
		t.Fatalf("no store: %d %s", code, body)
	}
}

func TestControlInstructionHashRouteStillWorks(t *testing.T) {
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	if err := store.SaveInstructionFile(instruction.Record{
		Hash: "deadbeef", FileType: "claude_md", Confidence: "high", Content: "x",
		ScanStatus: "clean", FirstSeen: now, LastSeen: now, SessionCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	h, _ := newProvenanceControl(t, store)
	code, body := getBody(h, "GET", "/control/instructions/deadbeef")
	if code != 200 || !strings.Contains(string(body), "deadbeef") {
		t.Fatalf("hash route: %d %s", code, body)
	}
	if code, _ := getBody(h, "GET", "/control/instructions/missing"); code != http.StatusNotFound {
		t.Fatalf("missing hash: %d, want 404", code)
	}
}
