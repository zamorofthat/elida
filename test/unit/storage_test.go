package unit

import (
	"os"
	"testing"
	"time"

	"elida/internal/storage"
)

func TestSQLiteStore_SaveAndGet(t *testing.T) {
	// Create temp database
	tmpFile, err := os.CreateTemp("", "elida-test-*.db")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := storage.NewSQLiteStore(tmpFile.Name())
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Create test record
	record := storage.SessionRecord{
		ID:           "test-session-1",
		State:        "completed",
		StartTime:    time.Now().Add(-10 * time.Minute),
		EndTime:      time.Now(),
		DurationMs:   600000,
		RequestCount: 5,
		BytesIn:      1024,
		BytesOut:     2048,
		Backend:      "http://localhost:11434",
		ClientAddr:   "127.0.0.1:12345",
		Metadata:     map[string]string{"key": "value"},
	}

	// Save
	err = store.SaveSession(record)
	if err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	// Get
	retrieved, err := store.GetSession("test-session-1")
	if err != nil {
		t.Fatalf("failed to get session: %v", err)
	}

	if retrieved == nil {
		t.Fatal("retrieved session is nil")
	}

	if retrieved.ID != record.ID {
		t.Errorf("expected ID %s, got %s", record.ID, retrieved.ID)
	}
	if retrieved.State != record.State {
		t.Errorf("expected state %s, got %s", record.State, retrieved.State)
	}
	if retrieved.RequestCount != record.RequestCount {
		t.Errorf("expected request count %d, got %d", record.RequestCount, retrieved.RequestCount)
	}
	if retrieved.BytesIn != record.BytesIn {
		t.Errorf("expected bytes in %d, got %d", record.BytesIn, retrieved.BytesIn)
	}
}

func TestSQLiteStore_ListSessions(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "elida-test-*.db")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := storage.NewSQLiteStore(tmpFile.Name())
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Create multiple records
	now := time.Now()
	for i := 1; i <= 5; i++ {
		record := storage.SessionRecord{
			ID:           "session-" + string(rune('0'+i)),
			State:        "completed",
			StartTime:    now.Add(-time.Duration(i) * time.Minute),
			EndTime:      now,
			DurationMs:   int64(i * 60000),
			RequestCount: i,
			BytesIn:      int64(i * 100),
			BytesOut:     int64(i * 200),
			Backend:      "http://localhost:11434",
			ClientAddr:   "127.0.0.1:12345",
		}
		if err := store.SaveSession(record); err != nil {
			t.Fatalf("failed to save session %d: %v", i, err)
		}
	}

	// List all
	sessions, err := store.ListSessions(storage.ListSessionsOptions{Limit: 10})
	if err != nil {
		t.Fatalf("failed to list sessions: %v", err)
	}

	if len(sessions) != 5 {
		t.Errorf("expected 5 sessions, got %d", len(sessions))
	}

	// List with limit
	sessions, err = store.ListSessions(storage.ListSessionsOptions{Limit: 2})
	if err != nil {
		t.Fatalf("failed to list sessions with limit: %v", err)
	}

	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(sessions))
	}
}

func TestSQLiteStore_GetStats(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "elida-test-*.db")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := storage.NewSQLiteStore(tmpFile.Name())
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	now := time.Now()
	// Create records with different states
	states := []string{"completed", "completed", "killed", "timeout"}
	for i, state := range states {
		record := storage.SessionRecord{
			ID:           "session-" + string(rune('a'+i)),
			State:        state,
			StartTime:    now.Add(-time.Duration(i) * time.Minute),
			EndTime:      now,
			DurationMs:   int64(60000),
			RequestCount: 1,
			BytesIn:      100,
			BytesOut:     200,
			Backend:      "http://localhost:11434",
			ClientAddr:   "127.0.0.1:12345",
		}
		if err := store.SaveSession(record); err != nil {
			t.Fatalf("failed to save session: %v", err)
		}
	}

	stats, err := store.GetStats(nil)
	if err != nil {
		t.Fatalf("failed to get stats: %v", err)
	}

	if stats.TotalSessions != 4 {
		t.Errorf("expected 4 total sessions, got %d", stats.TotalSessions)
	}

	if stats.SessionsByState["completed"] != 2 {
		t.Errorf("expected 2 completed sessions, got %d", stats.SessionsByState["completed"])
	}
}

func TestSQLiteStore_GetNotFound(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "elida-test-*.db")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := storage.NewSQLiteStore(tmpFile.Name())
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	record, err := store.GetSession("non-existent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if record != nil {
		t.Error("expected nil for non-existent session")
	}
}

func TestSQLiteStore_Cleanup(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "elida-test-*.db")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := storage.NewSQLiteStore(tmpFile.Name())
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	now := time.Now()
	// Create old and new records
	oldRecord := storage.SessionRecord{
		ID:        "old-session",
		State:     "completed",
		StartTime: now.AddDate(0, 0, -40), // 40 days ago
		EndTime:   now.AddDate(0, 0, -40),
	}
	newRecord := storage.SessionRecord{
		ID:        "new-session",
		State:     "completed",
		StartTime: now.Add(-1 * time.Hour),
		EndTime:   now,
	}

	store.SaveSession(oldRecord)
	store.SaveSession(newRecord)

	// Cleanup with 30 day retention
	deleted, err := store.Cleanup(30)
	if err != nil {
		t.Fatalf("failed to cleanup: %v", err)
	}

	if deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", deleted)
	}

	// Verify old session is gone
	old, _ := store.GetSession("old-session")
	if old != nil {
		t.Error("old session should have been deleted")
	}

	// Verify new session still exists
	new, _ := store.GetSession("new-session")
	if new == nil {
		t.Error("new session should still exist")
	}
}
