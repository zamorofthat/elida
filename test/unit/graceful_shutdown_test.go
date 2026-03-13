package unit

import (
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/session"
)

func TestDrainActiveSessions(t *testing.T) {
	store := session.NewMemoryStore()
	mgr := session.NewManager(store, 5*time.Minute)

	// Track which sessions had the callback invoked
	callbackSessions := map[string]bool{}
	mgr.SetSessionEndCallback(func(sess *session.Session) {
		callbackSessions[sess.ID] = true
	})

	// Create 3 active sessions
	s1 := mgr.GetOrCreate("active-1", "http://backend", "127.0.0.1:1234")
	s2 := mgr.GetOrCreate("active-2", "http://backend", "127.0.0.2:1234")
	s3 := mgr.GetOrCreate("active-3", "http://backend", "127.0.0.3:1234")

	// Simulate some activity
	s1.Touch()
	s2.Touch()
	s3.Touch()

	// Drain
	drained := mgr.DrainActiveSessions()

	if drained != 3 {
		t.Fatalf("expected 3 drained, got %d", drained)
	}

	// All sessions should have had callback invoked
	for _, id := range []string{"active-1", "active-2", "active-3"} {
		if !callbackSessions[id] {
			t.Errorf("callback not invoked for session %s", id)
		}
	}

	// All sessions should be in Completed state
	for _, id := range []string{"active-1", "active-2", "active-3"} {
		sess, ok := store.Get(id)
		if !ok {
			t.Fatalf("session %s not found in store", id)
		}
		if sess.GetState() != session.Completed {
			t.Errorf("session %s state = %v, want Completed", id, sess.GetState())
		}
	}
}

func TestDrainNoActiveSessions(t *testing.T) {
	store := session.NewMemoryStore()
	mgr := session.NewManager(store, 5*time.Minute)

	callbackCalled := false
	mgr.SetSessionEndCallback(func(sess *session.Session) {
		callbackCalled = true
	})

	drained := mgr.DrainActiveSessions()

	if drained != 0 {
		t.Fatalf("expected 0 drained, got %d", drained)
	}
	if callbackCalled {
		t.Error("callback should not have been called with no sessions")
	}
}

func TestDrainIncludesTimedOutSessions(t *testing.T) {
	store := session.NewMemoryStore()
	mgr := session.NewManager(store, 5*time.Minute)

	callbackSessions := map[string]bool{}
	mgr.SetSessionEndCallback(func(sess *session.Session) {
		callbackSessions[sess.ID] = true
	})

	// Create one active and one timed-out session
	mgr.GetOrCreate("active-1", "http://backend", "127.0.0.1:1234")

	timedOut := session.NewSession("timed-out-1", "http://backend", "127.0.0.2:1234")
	timedOut.SetState(session.TimedOut)
	store.Put(timedOut)

	drained := mgr.DrainActiveSessions()

	if drained != 2 {
		t.Fatalf("expected 2 drained (1 active + 1 timed-out), got %d", drained)
	}

	if !callbackSessions["active-1"] {
		t.Error("callback not invoked for active session")
	}
	if !callbackSessions["timed-out-1"] {
		t.Error("callback not invoked for timed-out session")
	}
}

func TestDrainSkipsKilledAndCompletedSessions(t *testing.T) {
	store := session.NewMemoryStore()
	mgr := session.NewManager(store, 5*time.Minute)

	callbackSessions := map[string]bool{}
	mgr.SetSessionEndCallback(func(sess *session.Session) {
		callbackSessions[sess.ID] = true
	})

	// Active session — should be drained
	mgr.GetOrCreate("active-1", "http://backend", "127.0.0.1:1234")

	// Killed session — already had callback via Kill(), skip
	killed := session.NewSession("killed-1", "http://backend", "127.0.0.2:1234")
	killed.Kill()
	store.Put(killed)

	// Completed session — already ended, skip
	completed := session.NewSession("completed-1", "http://backend", "127.0.0.3:1234")
	completed.SetState(session.Completed)
	store.Put(completed)

	drained := mgr.DrainActiveSessions()

	// Only 1 active session drained (killed and completed are skipped)
	if drained != 1 {
		t.Fatalf("expected 1 drained, got %d", drained)
	}

	if !callbackSessions["active-1"] {
		t.Error("callback not invoked for active session")
	}
	if callbackSessions["killed-1"] {
		t.Error("callback should not be invoked for killed session")
	}
	if callbackSessions["completed-1"] {
		t.Error("callback should not be invoked for completed session")
	}
}

func TestDrainWithoutCallback(t *testing.T) {
	store := session.NewMemoryStore()
	mgr := session.NewManager(store, 5*time.Minute)

	// No callback set — should not panic
	mgr.GetOrCreate("active-1", "http://backend", "127.0.0.1:1234")

	drained := mgr.DrainActiveSessions()

	if drained != 1 {
		t.Fatalf("expected 1 drained, got %d", drained)
	}

	// Session should still be marked Completed
	sess, ok := store.Get("active-1")
	if !ok {
		t.Fatal("session not found")
	}
	if sess.GetState() != session.Completed {
		t.Errorf("session state = %v, want Completed", sess.GetState())
	}
}

func TestDrainCallbackSeesCompletedState(t *testing.T) {
	store := session.NewMemoryStore()
	mgr := session.NewManager(store, 5*time.Minute)

	var callbackState session.State
	mgr.SetSessionEndCallback(func(sess *session.Session) {
		callbackState = sess.GetState()
	})

	mgr.GetOrCreate("active-1", "http://backend", "127.0.0.1:1234")
	mgr.DrainActiveSessions()

	if callbackState != session.Completed {
		t.Errorf("callback saw state %v, want Completed", callbackState)
	}
}

func TestShutdownTimeoutConfig(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		cfg, err := config.Load("nonexistent.yaml")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.ShutdownTimeout != 30*time.Second {
			t.Errorf("default shutdown_timeout = %v, want 30s", cfg.ShutdownTimeout)
		}
	})

	t.Run("env_override", func(t *testing.T) {
		t.Setenv("ELIDA_SHUTDOWN_TIMEOUT", "60s")
		// Use the real config file so applyEnvOverrides is called
		cfg, err := config.Load("../../configs/elida.yaml")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.ShutdownTimeout != 60*time.Second {
			t.Errorf("shutdown_timeout = %v, want 60s", cfg.ShutdownTimeout)
		}
	})
}
