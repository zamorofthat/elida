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

func TestManager_Resume(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)

	// Create and kill a session
	manager.GetOrCreate("resume-test", "http://backend", "127.0.0.1")
	manager.Kill("resume-test")

	// Resume the killed session
	if !manager.Resume("resume-test") {
		t.Error("expected Resume to return true")
	}

	// Verify session is active again
	sess, ok := manager.Get("resume-test")
	if !ok {
		t.Fatal("expected session to exist")
	}
	if sess.GetState() != session.Active {
		t.Errorf("expected session to be Active, got %v", sess.GetState())
	}

	// Resume already active session should fail
	if manager.Resume("resume-test") {
		t.Error("expected Resume to return false for already active session")
	}

	// Resume non-existent session should fail
	if manager.Resume("nonexistent") {
		t.Error("expected Resume to return false for non-existent session")
	}
}

func TestManager_Resume_AllowsNewRequests(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)

	// Create, kill, then resume a session
	sess := manager.GetOrCreate("resume-flow", "http://backend", "127.0.0.1")
	originalID := sess.ID
	manager.Kill("resume-flow")

	// Verify killed session is blocked
	blocked := manager.GetOrCreate("resume-flow", "http://backend", "127.0.0.1")
	if blocked != nil {
		t.Error("expected killed session to block new requests")
	}

	// Resume the session
	manager.Resume("resume-flow")

	// Now new requests should work
	resumed := manager.GetOrCreate("resume-flow", "http://backend", "127.0.0.1")
	if resumed == nil {
		t.Fatal("expected resumed session to allow requests")
	}
	if resumed.ID != originalID {
		t.Errorf("expected same session ID after resume, got %s", resumed.ID)
	}
}

func TestManager_Terminate(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)

	// Create and terminate a session
	manager.GetOrCreate("terminate-test", "http://backend", "127.0.0.1")
	if !manager.Terminate("terminate-test") {
		t.Error("expected Terminate to return true")
	}

	// Verify session is terminated
	sess, ok := manager.Get("terminate-test")
	if !ok {
		t.Fatal("expected session to exist")
	}
	if !sess.IsTerminated() {
		t.Error("expected session to be terminated")
	}

	// Terminated session cannot be resumed
	if manager.Resume("terminate-test") {
		t.Error("expected Resume to fail for terminated session")
	}

	// Terminate non-existent session should fail
	if manager.Terminate("nonexistent") {
		t.Error("expected Terminate to return false for non-existent session")
	}
}

func TestManager_Terminate_CannotResume(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)

	// Create, kill, then try to resume after terminate
	manager.GetOrCreate("no-resume", "http://backend", "127.0.0.1")
	manager.Kill("no-resume")

	// Can resume after kill
	if !manager.Resume("no-resume") {
		t.Error("expected Resume to succeed after kill")
	}

	// Kill again and then terminate
	manager.Kill("no-resume")
	manager.Terminate("no-resume")

	// Cannot resume after terminate
	if manager.Resume("no-resume") {
		t.Error("expected Resume to fail after terminate")
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

// ============ Kill Block Mode Tests ============

func TestManager_KillBlock_DurationMode(t *testing.T) {
	store := session.NewMemoryStore()
	// Create manager with duration mode, 100ms block duration
	manager := session.NewManagerWithKillBlock(store, 5*time.Minute, session.KillBlockConfig{
		Mode:     session.KillBlockDuration,
		Duration: 100 * time.Millisecond,
	})

	// Create session via client IP tracking
	sess := manager.GetOrCreateByClient("192.168.1.1:12345", "default", "http://backend")
	if sess == nil {
		t.Fatal("expected session to be created")
	}
	sessionID := sess.ID

	// Kill the session
	if !manager.Kill(sessionID) {
		t.Fatal("expected kill to succeed")
	}

	// Immediately try to create new session - should be blocked
	sess2 := manager.GetOrCreateByClient("192.168.1.1:12345", "default", "http://backend")
	if sess2 != nil {
		t.Error("expected request to be blocked immediately after kill")
	}

	// Wait for block duration to expire
	time.Sleep(150 * time.Millisecond)

	// Now should be allowed
	sess3 := manager.GetOrCreateByClient("192.168.1.1:12345", "default", "http://backend")
	if sess3 == nil {
		t.Error("expected request to be allowed after block duration expired")
	}
}

func TestManager_KillBlock_PermanentMode(t *testing.T) {
	store := session.NewMemoryStore()
	// Create manager with permanent mode
	manager := session.NewManagerWithKillBlock(store, 5*time.Minute, session.KillBlockConfig{
		Mode: session.KillBlockPermanent,
	})

	// Create session via client IP tracking
	sess := manager.GetOrCreateByClient("192.168.1.2:12345", "default", "http://backend")
	if sess == nil {
		t.Fatal("expected session to be created")
	}
	sessionID := sess.ID

	// Kill the session
	if !manager.Kill(sessionID) {
		t.Fatal("expected kill to succeed")
	}

	// Try multiple times - should always be blocked
	for i := 0; i < 3; i++ {
		sess2 := manager.GetOrCreateByClient("192.168.1.2:12345", "default", "http://backend")
		if sess2 != nil {
			t.Errorf("iteration %d: expected request to be blocked permanently", i)
		}
	}
}

func TestManager_KillBlock_UntilHourChangeMode(t *testing.T) {
	store := session.NewMemoryStore()
	// Create manager with until_hour_change mode
	manager := session.NewManagerWithKillBlock(store, 5*time.Minute, session.KillBlockConfig{
		Mode: session.KillBlockUntilHourChange,
	})

	// Create session via client IP tracking
	sess := manager.GetOrCreateByClient("192.168.1.3:12345", "default", "http://backend")
	if sess == nil {
		t.Fatal("expected session to be created")
	}
	sessionID := sess.ID

	// Kill the session
	if !manager.Kill(sessionID) {
		t.Fatal("expected kill to succeed")
	}

	// Should be blocked (same hour, same session ID generated)
	sess2 := manager.GetOrCreateByClient("192.168.1.3:12345", "default", "http://backend")
	if sess2 != nil {
		t.Error("expected request to be blocked until hour change")
	}
}

func TestManager_KillBlock_DifferentClientsNotBlocked(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManagerWithKillBlock(store, 5*time.Minute, session.KillBlockConfig{
		Mode: session.KillBlockPermanent,
	})

	// Create and kill session for client 1
	sess1 := manager.GetOrCreateByClient("192.168.1.100:12345", "default", "http://backend")
	if sess1 == nil {
		t.Fatal("expected session to be created for client 1")
	}
	manager.Kill(sess1.ID)

	// Client 2 should not be affected
	sess2 := manager.GetOrCreateByClient("192.168.1.101:12345", "default", "http://backend")
	if sess2 == nil {
		t.Error("expected client 2 to not be blocked by client 1's kill")
	}
}

func TestManager_GetOrCreateByClient_GeneratesConsistentID(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)

	// Same client IP should get same session
	sess1 := manager.GetOrCreateByClient("10.0.0.1:5000", "default", "http://backend")
	sess2 := manager.GetOrCreateByClient("10.0.0.1:5001", "default", "http://backend") // Different port, same IP

	if sess1.ID != sess2.ID {
		t.Errorf("expected same session ID for same IP, got %s and %s", sess1.ID, sess2.ID)
	}

	// Different IP should get different session
	sess3 := manager.GetOrCreateByClient("10.0.0.2:5000", "default", "http://backend")
	if sess1.ID == sess3.ID {
		t.Error("expected different session ID for different IP")
	}
}

func TestManager_GetOrCreateByClient_SessionIDFormat(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)

	sess := manager.GetOrCreateByClient("172.16.0.1:8080", "default", "http://backend")

	// Session ID should start with "client-" prefix
	if len(sess.ID) < 7 || sess.ID[:7] != "client-" {
		t.Errorf("expected session ID to start with 'client-', got %s", sess.ID)
	}

	// Should be "client-" + 8 char hex hash + "-" + backend name
	// Format: client-xxxxxxxx-default (23 chars for "default" backend)
	expectedSuffix := "-default"
	if len(sess.ID) < len(expectedSuffix) || sess.ID[len(sess.ID)-len(expectedSuffix):] != expectedSuffix {
		t.Errorf("expected session ID to end with '%s', got %s", expectedSuffix, sess.ID)
	}
}
