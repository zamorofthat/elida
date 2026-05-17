package unit

import (
	"os"
	"testing"
	"time"

	"elida/internal/instruction"
	"elida/internal/storage"
)

func newInstructionTestStore(t *testing.T) *storage.SQLiteStore {
	t.Helper()
	f, err := os.CreateTemp("", "elida-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	store, err := storage.NewSQLiteStore(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestSaveAndGetInstructionFile(t *testing.T) {
	store := newInstructionTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	record := instruction.Record{
		Hash:         "abc123",
		FileType:     "claude_md",
		Confidence:   "high",
		SourcePath:   "/project/CLAUDE.md",
		Content:      "# Rules\nBe good.",
		ScanStatus:   "clean",
		ScanResults:  nil,
		FirstSeen:    now,
		LastSeen:     now,
		SessionCount: 1,
	}

	if err := store.SaveInstructionFile(record); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetInstructionFile("abc123")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected record, got nil")
		return
	}
	if got.FileType != "claude_md" {
		t.Errorf("file_type = %q, want %q", got.FileType, "claude_md")
	}
	if got.Content != "# Rules\nBe good." {
		t.Errorf("content mismatch")
	}
	if got.SessionCount != 1 {
		t.Errorf("session_count = %d, want 1", got.SessionCount)
	}
}

func TestIncrementSessionCount(t *testing.T) {
	store := newInstructionTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	record := instruction.Record{
		Hash: "abc123", FileType: "claude_md", Confidence: "high",
		Content: "content", ScanStatus: "clean",
		FirstSeen: now, LastSeen: now, SessionCount: 1,
	}
	if err := store.SaveInstructionFile(record); err != nil {
		t.Fatal(err)
	}

	if err := store.IncrementInstructionFileSessionCount("abc123", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	got, _ := store.GetInstructionFile("abc123")
	if got.SessionCount != 2 {
		t.Errorf("session_count = %d, want 2", got.SessionCount)
	}
}

func TestSaveInstructionEvent(t *testing.T) {
	store := newInstructionTestStore(t)

	err := store.SaveInstructionEvent(storage.InstructionEvent{
		Timestamp: time.Now(),
		EventType: "instruction_integrity",
		SessionID: "session-1",
		Severity:  "info",
		Data: map[string]interface{}{
			"hash":        "abc123",
			"file_type":   "claude_md",
			"change_type": "first_seen",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestListInstructionFiles(t *testing.T) {
	store := newInstructionTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	for i, ft := range []string{"claude_md", "cursorrules", "agents_md"} {
		if err := store.SaveInstructionFile(instruction.Record{
			Hash: ft + "_hash", FileType: ft, Confidence: "high",
			Content: "content " + ft, ScanStatus: "clean",
			FirstSeen: now.Add(time.Duration(i) * time.Minute), LastSeen: now, SessionCount: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}

	records, err := store.ListInstructionFiles("", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Errorf("count = %d, want 3", len(records))
	}

	// Filter by type
	records, err = store.ListInstructionFiles("claude_md", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Errorf("filtered count = %d, want 1", len(records))
	}
}
