package instruction

import (
	"reflect"
	"sort"
	"testing"
	"time"
)

// newPersistRegistry is newProvenanceRegistry with a store. The caller reads
// the store through drained, which stops the registry so the worker has
// written every queued job.
func newPersistRegistry(t *testing.T, store *mockStore) *Registry {
	t.Helper()
	scanner, err := NewScanner([]Rule{
		{Name: "instruction_permission_escalation", Patterns: []string{`always\s+approve`}, Severity: "critical", Action: "flag"},
		{Name: "instruction_shell_exec", Patterns: []string{`\beval\s*\(`}, Severity: "warning", Action: "flag"},
	})
	if err != nil {
		t.Fatalf("NewScanner: %v", err)
	}
	reg := NewRegistry(scanner, store, 100)
	t.Cleanup(reg.Stop)
	return reg
}

// drained stops reg (draining its worker) and returns the store.
func drained(reg *Registry, store *mockStore) *mockStore {
	reg.Stop()
	return store
}

func (m *mockStore) pathVersions(path string) []PathVersion {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []PathVersion
	for _, v := range m.versions {
		if v.SourcePath == path {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FirstSeen.Before(out[j].FirstSeen) })
	return out
}

func (m *mockStore) version(path, hash string) (PathVersion, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.versions[path+"\x00"+hash]
	return v, ok
}

func (m *mockStore) eventsOfType(typ string) []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Event
	for _, e := range m.events {
		if e.EventType == typ {
			out = append(out, e)
		}
	}
	return out
}

func TestSinceLastSeenOffByDefault(t *testing.T) {
	store := newMockStore()
	reg := newPersistRegistry(t, store)
	observe(reg, "s1", true, provPath, provA)
	o := observe(reg, "s2", true, provPath, provB)
	if o.Change != ChangeNone {
		t.Fatalf("s2: Change = %q, want none with the toggle off", o.Change)
	}
	if n := len(drained(reg, store).pathVersions(provPath)); n != 2 {
		t.Fatalf("stored versions = %d, want 2", n)
	}
}

func TestSinceLastSeen(t *testing.T) {
	reg := newProvenanceRegistry(t)
	reg.SetChangedSinceLastSeen(true)
	observe(reg, "s1", true, provPath, provA)
	o := observe(reg, "s2", true, provPath, provB)
	if o.Change != ChangeSinceLastSeen {
		t.Fatalf("Change = %q, want %q", o.Change, ChangeSinceLastSeen)
	}
	if o.PrevHash != hashContent(provA) {
		t.Errorf("PrevHash = %q, want hash(A)", o.PrevHash)
	}
	if !o.DiffKnown || o.LinesAdded != 1 {
		t.Errorf("diff known=%v +%d, want known +1", o.DiffKnown, o.LinesAdded)
	}
	if o.Caused {
		t.Error("Caused = true, want false")
	}
	if o.Severity != "info" {
		t.Errorf("Severity = %q, want info", o.Severity)
	}
}

func TestSinceLastSeenEscalation(t *testing.T) {
	reg := newProvenanceRegistry(t)
	reg.SetChangedSinceLastSeen(true)
	observe(reg, "s1", true, provPath, provA)
	o := observe(reg, "s2", true, provPath, provX)
	if o.Severity != "critical" {
		t.Errorf("Severity = %q, want critical", o.Severity)
	}
	if want := []string{"instruction_permission_escalation"}; !reflect.DeepEqual(o.AddedRuleHits, want) {
		t.Errorf("AddedRuleHits = %v, want %v", o.AddedRuleHits, want)
	}
}

func TestSinceLastSeenOncePerVersion(t *testing.T) {
	reg := newProvenanceRegistry(t)
	reg.SetChangedSinceLastSeen(true)
	observe(reg, "s1", true, provPath, provA)
	observe(reg, "s2", true, provPath, provB)
	if o := observe(reg, "s3", true, provPath, provB); o.Change != ChangeNone {
		t.Fatalf("s3: Change = %q, want none (the version is known now)", o.Change)
	}
}

func TestSinceLastSeenNeedsIdentity(t *testing.T) {
	reg := newProvenanceRegistry(t)
	reg.SetChangedSinceLastSeen(true)
	observe(reg, "s1", true, provPath, provA)
	o := observe(reg, "s2", false, provPath, provB)
	if !o.Suppressed || o.Severity != "" {
		t.Fatalf("got suppressed=%v severity=%q, want suppressed and no severity", o.Suppressed, o.Severity)
	}
}

// preloadAB stores versions A (older) then B of provPath, as a previous
// process would have left them.
func preloadAB(store *mockStore) {
	t0 := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	_ = store.SavePathVersion(PathVersion{SourcePath: provPath, Hash: hashContent(provA),
		FirstSeen: t0, LastSeen: t0.Add(time.Minute), ChangeType: "first_seen"})
	_ = store.SavePathVersion(PathVersion{SourcePath: provPath, Hash: hashContent(provB),
		PrevHash: hashContent(provA), FirstSeen: t0.Add(2 * time.Minute), LastSeen: t0.Add(3 * time.Minute),
		ChangeType: "in_session", DiffKnown: true, LinesAdded: 1})
}

func restartedRegistry(t *testing.T, store *mockStore) *Registry {
	t.Helper()
	reg := newPersistRegistry(t, store)
	if err := reg.LoadPathVersions(); err != nil {
		t.Fatalf("LoadPathVersions: %v", err)
	}
	reg.SetChangedSinceLastSeen(true)
	return reg
}

func TestRestart(t *testing.T) {
	store := newMockStore()
	preloadAB(store)
	reg := restartedRegistry(t, store)

	if o := observe(reg, "s1", true, provPath, provA); o.Change != ChangeNone {
		t.Fatalf("s1 A: Change = %q, want none (a known version)", o.Change)
	}
	const provC = "# Other rules\nUse tabs.\n"
	o := observe(reg, "s2", true, provPath, provC)
	if o.Change != ChangeSinceLastSeen {
		t.Fatalf("s2 C: Change = %q, want %q", o.Change, ChangeSinceLastSeen)
	}
	if o.PrevHash != hashContent(provB) {
		t.Errorf("PrevHash = %q, want hash(B), the newest version", o.PrevHash)
	}
	if o.DiffKnown || o.LinesAdded != 0 || o.Severity != "info" {
		t.Errorf("got known=%v +%d severity=%q, want unknown, 0, info", o.DiffKnown, o.LinesAdded, o.Severity)
	}
	if o := observe(reg, "s3", true, provPath, provC); o.Change != ChangeNone {
		t.Fatalf("s3 C: Change = %q, want none", o.Change)
	}
}

func TestRestartRelearnsLines(t *testing.T) {
	store := newMockStore()
	preloadAB(store)
	reg := restartedRegistry(t, store)

	observe(reg, "s1", true, provPath, provB) // the newest: its lines are filled in
	o := observe(reg, "s2", true, provPath, provB+"Keep it short.\n")
	if o.Change != ChangeSinceLastSeen || !o.DiffKnown || o.LinesAdded != 1 {
		t.Fatalf("got change=%q known=%v +%d, want since_last_seen, known, +1", o.Change, o.DiffKnown, o.LinesAdded)
	}
}

func TestVersionPersistence(t *testing.T) {
	store := newMockStore()
	reg := newPersistRegistry(t, store)
	observe(reg, "s1", true, provPath, provA)
	markEdit(reg, "s1", "toolu_1")
	observe(reg, "s1", true, provPath, provB)
	drained(reg, store)

	v, ok := store.version(provPath, hashContent(provB))
	if !ok {
		t.Fatal("version B not stored")
	}
	if v.ChangeType != "in_session" || v.PrevHash != hashContent(provA) || !v.Caused || v.CauseTool != "Edit" {
		t.Errorf("B row = %+v, want in_session from hash(A), caused by Edit", v)
	}

	evts := store.eventsOfType("instruction_file_changed")
	if len(evts) != 1 {
		t.Fatalf("instruction_file_changed events = %d, want 1", len(evts))
	}
	var keys []string
	for k := range evts[0].Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"added_rule_hits", "cause_tool", "caused", "change_type", "diff_known", "hash",
		"lines_added", "lines_removed", "prev_hash", "source_path", "suppressed"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("event Data keys = %v, want %v", keys, want)
	}
}

func TestCausedFlipIsPersisted(t *testing.T) {
	store := newMockStore()
	reg := newPersistRegistry(t, store)
	observe(reg, "s1", true, provPath, provA)
	observe(reg, "s2", true, provPath, provA)
	observe(reg, "s2", true, provPath, provB) // uncaused: the row is written uncaused
	markEdit(reg, "s1", "toolu_1")
	observe(reg, "s1", true, provPath, provB)
	drained(reg, store)

	v, ok := store.version(provPath, hashContent(provB))
	if !ok || !v.Caused {
		t.Fatalf("B row = %+v (found %v), want caused", v, ok)
	}
}

// A version seen again is rewritten only once its last_seen has moved by
// more than PathVersionTouchInterval since the last write.
func TestLastSeenTouch(t *testing.T) {
	store := newMockStore()
	reg := newPersistRegistry(t, store)
	clock := time.Now().UTC().Truncate(time.Second)
	reg.tracker.now = func() time.Time { return clock }
	observe(reg, "s1", true, provPath, provA)
	first := clock

	clock = clock.Add(PathVersionTouchInterval / 2)
	observe(reg, "s2", true, provPath, provA)
	clock = clock.Add(PathVersionTouchInterval)
	observe(reg, "s3", true, provPath, provA)
	drained(reg, store)

	if store.saves != 2 {
		t.Errorf("SavePathVersion calls = %d, want 2 (created, then one touch)", store.saves)
	}
	v, _ := store.version(provPath, hashContent(provA))
	if !v.LastSeen.Equal(clock) || !v.FirstSeen.Equal(first) {
		t.Errorf("A row first=%v last=%v, want first=%v last=%v", v.FirstSeen, v.LastSeen, first, clock)
	}
}
