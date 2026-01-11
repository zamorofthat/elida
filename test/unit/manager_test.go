package unit

import (
	"testing"
	"time"

	"elida/internal/session"
)

func TestManager_GetOrCreate(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)

	// Create new session
	sess := manager.GetOrCreate("test-id", "http://backend", "127.0.0.1")
	if sess == nil {
		t.Fatal("expected session to be created")
	}
	if sess.ID != "test-id" {
		t.Errorf("expected ID 'test-id', got %s", sess.ID)
	}

	// Get existing session
	sess2 := manager.GetOrCreate("test-id", "http://backend", "127.0.0.1")
	if sess2 != sess {
		t.Error("expected to get same session instance")
	}
}

func TestManager_GetOrCreate_GeneratesID(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)

	sess := manager.GetOrCreate("", "http://backend", "127.0.0.1")
	if sess == nil {
		t.Fatal("expected session to be created")
	}
	if sess.ID == "" {
		t.Error("expected ID to be generated")
	}
	// UUID format check
	if len(sess.ID) != 36 {
		t.Errorf("expected UUID format (36 chars), got %d chars", len(sess.ID))
	}
}

func TestManager_GetOrCreate_RejectsKilledSession(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)

	// Create and kill session
	sess := manager.GetOrCreate("killed-session", "http://backend", "127.0.0.1")
	manager.Kill("killed-session")

	// Try to reuse killed session ID
	sess2 := manager.GetOrCreate("killed-session", "http://backend", "127.0.0.1")
	if sess2 != nil {
		t.Error("expected nil for killed session ID")
	}

	// Verify original session is still killed
	if sess.GetState() != session.Killed {
		t.Error("expected session to remain killed")
	}
}

func TestManager_GetOrCreate_AllowsTimedOutSessionID(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)

	// Create and timeout session
	sess := manager.GetOrCreate("timeout-session", "http://backend", "127.0.0.1")
	sess.SetState(session.TimedOut)

	// Reusing timed out session ID should create new session
	sess2 := manager.GetOrCreate("timeout-session", "http://backend", "127.0.0.1")
	if sess2 == nil {
		t.Fatal("expected new session to be created")
	}
	if sess2.GetState() != session.Active {
		t.Error("expected new session to be active")
	}
}

func TestManager_Kill(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)

	manager.GetOrCreate("test-id", "http://backend", "127.0.0.1")

	// Kill existing session
	if !manager.Kill("test-id") {
		t.Error("expected Kill to return true")
	}

	// Verify session is killed
	sess, _ := manager.Get("test-id")
	if sess.GetState() != session.Killed {
		t.Error("expected session to be killed")
	}

	// Kill already killed session
	if manager.Kill("test-id") {
		t.Error("expected Kill to return false for already killed session")
	}

	// Kill non-existent session
	if manager.Kill("nonexistent") {
		t.Error("expected Kill to return false for non-existent session")
	}
}

func TestManager_ListActive(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)

	manager.GetOrCreate("active-1", "http://backend", "127.0.0.1")
	manager.GetOrCreate("active-2", "http://backend", "127.0.0.1")
	manager.GetOrCreate("killed", "http://backend", "127.0.0.1")
	manager.Kill("killed")

	active := manager.ListActive()
	if len(active) != 2 {
		t.Errorf("expected 2 active sessions, got %d", len(active))
	}
}

func TestManager_Stats(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)

	sess1 := manager.GetOrCreate("active", "http://backend", "127.0.0.1")
	sess1.AddBytes(100, 200)

	manager.GetOrCreate("killed", "http://backend", "127.0.0.1")
	manager.Kill("killed")

	sess3 := manager.GetOrCreate("timeout", "http://backend", "127.0.0.1")
	sess3.SetState(session.TimedOut)

	stats := manager.Stats()

	if stats.Total != 3 {
		t.Errorf("expected Total 3, got %d", stats.Total)
	}
	if stats.Active != 1 {
		t.Errorf("expected Active 1, got %d", stats.Active)
	}
	if stats.Killed != 1 {
		t.Errorf("expected Killed 1, got %d", stats.Killed)
	}
	if stats.TimedOut != 1 {
		t.Errorf("expected TimedOut 1, got %d", stats.TimedOut)
	}
	if stats.TotalBytesIn != 100 {
		t.Errorf("expected TotalBytesIn 100, got %d", stats.TotalBytesIn)
	}
	if stats.TotalBytesOut != 200 {
		t.Errorf("expected TotalBytesOut 200, got %d", stats.TotalBytesOut)
	}
}

func TestManager_Complete(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)

	manager.GetOrCreate("test-id", "http://backend", "127.0.0.1")
	manager.Complete("test-id")

	sess, _ := manager.Get("test-id")
	if sess.GetState() != session.Completed {
		t.Errorf("expected state Completed, got %s", sess.GetState())
	}
}

func TestManager_Kill_PersistsState(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)

	// Create session
	manager.GetOrCreate("persist-kill", "http://backend", "127.0.0.1")

	// Kill it
	manager.Kill("persist-kill")

	// Retrieve fresh from store (simulating what happens after restart)
	sess, ok := store.Get("persist-kill")
	if !ok {
		t.Fatal("expected session to still exist in store")
	}

	// Verify state was persisted
	if sess.GetState() != session.Killed {
		t.Errorf("expected persisted state Killed, got %s", sess.GetState())
	}
}
