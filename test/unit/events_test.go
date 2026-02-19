package unit

import (
	"context"
	"os"
	"testing"
	"time"

	"elida/internal/storage"
)

func TestEventStore_RecordEvent(t *testing.T) {
	// Create temporary database
	tmpFile, err := os.CreateTemp("", "elida-events-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := storage.NewSQLiteStore(tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()

	// Record an event
	err = store.RecordEvent(ctx, storage.EventSessionStarted, "session-1", "", storage.SessionStartedData{
		ClientAddr: "127.0.0.1",
		Backend:    "default",
	})
	if err != nil {
		t.Fatalf("failed to record event: %v", err)
	}

	// Verify it was recorded
	events, err := store.GetSessionEvents("session-1")
	if err != nil {
		t.Fatalf("failed to get events: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != storage.EventSessionStarted {
		t.Errorf("expected event type %s, got %s", storage.EventSessionStarted, events[0].Type)
	}
}

func TestEventStore_ListEvents(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "elida-events-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := storage.NewSQLiteStore(tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()

	// Record multiple events
	_ = store.RecordEvent(ctx, storage.EventSessionStarted, "session-1", "", storage.SessionStartedData{})
	_ = store.RecordEvent(ctx, storage.EventViolationDetected, "session-1", "warning", storage.ViolationDetectedData{
		RuleName: "test_rule",
		Severity: "warning",
	})
	_ = store.RecordEvent(ctx, storage.EventSessionEnded, "session-1", "", storage.SessionEndedData{})
	_ = store.RecordEvent(ctx, storage.EventSessionStarted, "session-2", "", storage.SessionStartedData{})

	// List all events
	events, err := store.ListEvents(storage.ListEventsOptions{})
	if err != nil {
		t.Fatalf("failed to list events: %v", err)
	}
	if len(events) != 4 {
		t.Errorf("expected 4 events, got %d", len(events))
	}

	// Filter by session
	events, err = store.ListEvents(storage.ListEventsOptions{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Errorf("expected 3 events for session-1, got %d", len(events))
	}

	// Filter by type
	events, err = store.ListEvents(storage.ListEventsOptions{Type: storage.EventViolationDetected})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Errorf("expected 1 violation event, got %d", len(events))
	}

	// Filter by severity
	events, err = store.ListEvents(storage.ListEventsOptions{Severity: "warning"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Errorf("expected 1 warning event, got %d", len(events))
	}
}

func TestEventStore_GetSessionEvents(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "elida-events-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := storage.NewSQLiteStore(tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	sessionID := "test-session-events"

	_ = store.RecordEvent(ctx, storage.EventSessionStarted, sessionID, "", storage.SessionStartedData{})
	_ = store.RecordEvent(ctx, storage.EventToolCalled, sessionID, "", storage.ToolCalledData{
		ToolName: "get_weather",
	})
	_ = store.RecordEvent(ctx, storage.EventTokensUsed, sessionID, "", storage.TokensUsedData{
		TokensIn:  100,
		TokensOut: 50,
	})

	events, err := store.GetSessionEvents(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Errorf("expected 3 events, got %d", len(events))
	}
}

func TestEventStore_Stats(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "elida-events-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := storage.NewSQLiteStore(tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()

	_ = store.RecordEvent(ctx, storage.EventSessionStarted, "s1", "", storage.SessionStartedData{})
	_ = store.RecordEvent(ctx, storage.EventViolationDetected, "s1", "warning", storage.ViolationDetectedData{})
	_ = store.RecordEvent(ctx, storage.EventViolationDetected, "s1", "critical", storage.ViolationDetectedData{})
	_ = store.RecordEvent(ctx, storage.EventSessionStarted, "s2", "", storage.SessionStartedData{})

	stats, err := store.GetEventStats(nil)
	if err != nil {
		t.Fatalf("failed to get stats: %v", err)
	}

	if stats.TotalEvents != 4 {
		t.Errorf("expected 4 total events, got %d", stats.TotalEvents)
	}
	if stats.UniqueSessionIDs != 2 {
		t.Errorf("expected 2 unique sessions, got %d", stats.UniqueSessionIDs)
	}
	if stats.EventsByType[string(storage.EventSessionStarted)] != 2 {
		t.Errorf("expected 2 session_started events, got %d", stats.EventsByType[string(storage.EventSessionStarted)])
	}
	if stats.EventsBySeverity["warning"] != 1 {
		t.Errorf("expected 1 warning event, got %d", stats.EventsBySeverity["warning"])
	}
}

func TestEventStore_FilterByTime(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "elida-events-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := storage.NewSQLiteStore(tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()

	// Record events
	_ = store.RecordEvent(ctx, storage.EventSessionStarted, "s1", "", storage.SessionStartedData{})
	time.Sleep(10 * time.Millisecond)
	midTime := time.Now()
	time.Sleep(10 * time.Millisecond)
	_ = store.RecordEvent(ctx, storage.EventSessionStarted, "s2", "", storage.SessionStartedData{})

	// Filter by time
	events, err := store.ListEvents(storage.ListEventsOptions{Since: &midTime})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Errorf("expected 1 event since midTime, got %d", len(events))
	}
}

func TestEventStore_Pagination(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "elida-events-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	store, err := storage.NewSQLiteStore(tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()

	// Record 10 events
	for i := 0; i < 10; i++ {
		_ = store.RecordEvent(ctx, storage.EventSessionStarted, "s1", "", storage.SessionStartedData{})
	}

	// Get first page
	events, err := store.ListEvents(storage.ListEventsOptions{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Errorf("expected 3 events in first page, got %d", len(events))
	}

	// Get second page
	events, err = store.ListEvents(storage.ListEventsOptions{Limit: 3, Offset: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Errorf("expected 3 events in second page, got %d", len(events))
	}
}
