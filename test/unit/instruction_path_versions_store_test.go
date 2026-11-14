package unit

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"elida/internal/instruction"
	"elida/internal/storage"
)

func TestPathVersionStore(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "elida.db")
	store, err := storage.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().UTC().Truncate(time.Second)

	v := instruction.PathVersion{
		SourcePath: "/repo/CLAUDE.md", Hash: "hb", PrevHash: "ha",
		FirstSeen: t0, LastSeen: t0.Add(time.Minute),
		ChangeType: "since_last_seen", Caused: false, CauseTool: "",
		DiffKnown: true, LinesAdded: 2, LinesRemoved: 1,
		AddedRuleHits: []string{"instruction_permission_escalation", "instruction_shell_exec"},
	}
	if err := store.SaveInstructionPathVersion(v); err != nil {
		t.Fatal(err)
	}
	got, err := store.ListInstructionPathVersions(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("rows = %d, want 1", len(got))
	}
	g := got[0]
	if !g.FirstSeen.Equal(v.FirstSeen) || !g.LastSeen.Equal(v.LastSeen) {
		t.Errorf("times first=%v last=%v, want %v %v", g.FirstSeen, g.LastSeen, v.FirstSeen, v.LastSeen)
	}
	g.FirstSeen, g.LastSeen = v.FirstSeen, v.LastSeen
	if !reflect.DeepEqual(g, v) {
		t.Errorf("row = %+v\nwant  %+v", g, v)
	}

	// The upsert: caused only rises, last_seen only advances, first_seen stays.
	again := v
	again.Caused, again.CauseTool = true, "Edit"
	again.LastSeen = t0.Add(time.Hour)
	again.FirstSeen = t0.Add(-time.Hour)
	if err := store.SaveInstructionPathVersion(again); err != nil {
		t.Fatal(err)
	}
	got, _ = store.ListInstructionPathVersions(10)
	if len(got) != 1 {
		t.Fatalf("rows after upsert = %d, want 1", len(got))
	}
	if g := got[0]; !g.Caused || !g.LastSeen.Equal(t0.Add(time.Hour)) || !g.FirstSeen.Equal(t0) {
		t.Errorf("after upsert: caused=%v last=%v first=%v, want true %v %v", g.Caused, g.LastSeen, g.FirstSeen, t0.Add(time.Hour), t0)
	}
	again.Caused = false
	if err := store.SaveInstructionPathVersion(again); err != nil {
		t.Fatal(err)
	}
	got, _ = store.ListInstructionPathVersions(10)
	if !got[0].Caused {
		t.Error("caused went back to false")
	}

	// Three versions over two paths: the limit keeps the newest by last_seen.
	if err := store.SaveInstructionPathVersion(instruction.PathVersion{SourcePath: "/repo/AGENTS.md", Hash: "h1",
		FirstSeen: t0, LastSeen: t0.Add(2 * time.Hour), ChangeType: "first_seen"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveInstructionPathVersion(instruction.PathVersion{SourcePath: "/repo/AGENTS.md", Hash: "h0",
		FirstSeen: t0, LastSeen: t0.Add(time.Second), ChangeType: "first_seen"}); err != nil {
		t.Fatal(err)
	}
	got, err = store.ListInstructionPathVersions(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Hash != "h1" || got[1].Hash != "hb" {
		t.Fatalf("limit 2 = %v, want h1 then hb", got)
	}

	// Reopen: migrate runs again and the rows are still there.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = storage.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store.Close()
	got, err = store.ListInstructionPathVersions(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("rows after reopen = %d, want 3", len(got))
	}
}
