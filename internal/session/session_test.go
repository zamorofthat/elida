package session

import (
	"testing"
	"time"
)

func TestNewSession(t *testing.T) {
	sess := NewSession("test-id", "http://backend", "127.0.0.1")

	if sess.ID != "test-id" {
		t.Errorf("expected ID 'test-id', got %s", sess.ID)
	}
	if sess.Backend != "http://backend" {
		t.Errorf("expected Backend 'http://backend', got %s", sess.Backend)
	}
	if sess.ClientAddr != "127.0.0.1" {
		t.Errorf("expected ClientAddr '127.0.0.1', got %s", sess.ClientAddr)
	}
	if sess.GetState() != Active {
		t.Errorf("expected state Active, got %s", sess.GetState())
	}
	if sess.RequestCount != 0 {
		t.Errorf("expected RequestCount 0, got %d", sess.RequestCount)
	}
}

func TestSessionTouch(t *testing.T) {
	sess := NewSession("test-id", "http://backend", "127.0.0.1")
	initialActivity := sess.LastActivity

	time.Sleep(10 * time.Millisecond)
	sess.Touch()

	if sess.RequestCount != 1 {
		t.Errorf("expected RequestCount 1, got %d", sess.RequestCount)
	}
	if !sess.LastActivity.After(initialActivity) {
		t.Error("expected LastActivity to be updated")
	}
}

func TestSessionAddBytes(t *testing.T) {
	sess := NewSession("test-id", "http://backend", "127.0.0.1")

	sess.AddBytes(100, 200)
	if sess.BytesIn != 100 {
		t.Errorf("expected BytesIn 100, got %d", sess.BytesIn)
	}
	if sess.BytesOut != 200 {
		t.Errorf("expected BytesOut 200, got %d", sess.BytesOut)
	}

	sess.AddBytes(50, 50)
	if sess.BytesIn != 150 {
		t.Errorf("expected BytesIn 150, got %d", sess.BytesIn)
	}
	if sess.BytesOut != 250 {
		t.Errorf("expected BytesOut 250, got %d", sess.BytesOut)
	}
}

func TestSessionKill(t *testing.T) {
	sess := NewSession("test-id", "http://backend", "127.0.0.1")

	if !sess.IsActive() {
		t.Error("expected session to be active initially")
	}

	sess.Kill()

	if sess.IsActive() {
		t.Error("expected session to not be active after kill")
	}
	if sess.GetState() != Killed {
		t.Errorf("expected state Killed, got %s", sess.GetState())
	}
	if sess.EndTime == nil {
		t.Error("expected EndTime to be set")
	}

	// Verify kill channel is closed
	select {
	case <-sess.KillChan():
		// Expected - channel is closed
	default:
		t.Error("expected kill channel to be closed")
	}
}

func TestSessionSetState(t *testing.T) {
	sess := NewSession("test-id", "http://backend", "127.0.0.1")

	sess.SetState(Completed)
	if sess.GetState() != Completed {
		t.Errorf("expected state Completed, got %s", sess.GetState())
	}
	if sess.EndTime == nil {
		t.Error("expected EndTime to be set for non-active state")
	}
}

func TestSessionDuration(t *testing.T) {
	sess := NewSession("test-id", "http://backend", "127.0.0.1")

	time.Sleep(50 * time.Millisecond)
	duration := sess.Duration()

	if duration < 50*time.Millisecond {
		t.Errorf("expected duration >= 50ms, got %v", duration)
	}
}

func TestSessionIdleTime(t *testing.T) {
	sess := NewSession("test-id", "http://backend", "127.0.0.1")

	time.Sleep(50 * time.Millisecond)
	idleTime := sess.IdleTime()

	if idleTime < 50*time.Millisecond {
		t.Errorf("expected idle time >= 50ms, got %v", idleTime)
	}

	sess.Touch()
	idleTime = sess.IdleTime()

	if idleTime > 10*time.Millisecond {
		t.Errorf("expected idle time < 10ms after touch, got %v", idleTime)
	}
}

func TestSessionSnapshot(t *testing.T) {
	sess := NewSession("test-id", "http://backend", "127.0.0.1")
	sess.SetMetadata("key", "value")
	sess.AddBytes(100, 200)
	sess.Touch()

	snap := sess.Snapshot()

	if snap.ID != sess.ID {
		t.Error("snapshot ID mismatch")
	}
	if snap.BytesIn != sess.BytesIn {
		t.Error("snapshot BytesIn mismatch")
	}
	if snap.Metadata["key"] != "value" {
		t.Error("snapshot metadata mismatch")
	}

	// Verify snapshot is independent
	snap.Metadata["key"] = "modified"
	if sess.Metadata["key"] == "modified" {
		t.Error("snapshot should be independent of original")
	}
}

func TestStateString(t *testing.T) {
	tests := []struct {
		state    State
		expected string
	}{
		{Active, "active"},
		{Completed, "completed"},
		{Killed, "killed"},
		{TimedOut, "timeout"},
		{State(99), "unknown"},
	}

	for _, tt := range tests {
		if tt.state.String() != tt.expected {
			t.Errorf("expected %s, got %s", tt.expected, tt.state.String())
		}
	}
}
