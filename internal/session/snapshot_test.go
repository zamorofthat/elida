package session

import "testing"

// TestSnapshotIsDeepCopy pins the deep-copy semantics of Session.Snapshot():
// mutating the original session after taking a snapshot must not affect the
// snapshot, and mutating the snapshot's slices must not alias the original's.
// This must pass both before and after the Snapshot() signature changes from
// a value return to a pointer return.
func TestSnapshotIsDeepCopy(t *testing.T) {
	s := NewSession("snap-test", "backend", "127.0.0.1:1")
	s.AddFailedBackend("b1")
	snap := s.Snapshot()

	// Mutating the original after snapshotting must not affect the snapshot.
	s.AddFailedBackend("b2")
	if len(snap.FailedBackends) != 1 || snap.FailedBackends[0] != "b1" {
		t.Errorf("snapshot not isolated from original: %v", snap.FailedBackends)
	}
	// And the snapshot must not alias the original's slices.
	snap.FailedBackends[0] = "mutated"
	if s.GetFailedBackends()[0] == "mutated" {
		t.Error("snapshot aliases original slice")
	}
}
