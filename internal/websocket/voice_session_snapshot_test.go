package websocket

import "testing"

// TestVoiceSessionSnapshotIsDeepCopy pins the deep-copy semantics of
// VoiceSession.Snapshot(): mutating the original session after taking a
// snapshot must not affect the snapshot, and mutating the snapshot's
// slices/maps must not alias the original's. This must pass both before and
// after the Snapshot() signature changes from a value return to a pointer
// return.
func TestVoiceSessionSnapshotIsDeepCopy(t *testing.T) {
	v := NewVoiceSession("parent-1")
	v.AddTranscript("user", "hello", "stt", true)
	v.SetMetadata("k1", "v1")
	snap := v.Snapshot()

	// Mutating the original after snapshotting must not affect the snapshot.
	v.AddTranscript("assistant", "hi there", "tts", true)
	v.SetMetadata("k2", "v2")
	if len(snap.Transcript) != 1 || snap.Transcript[0].Text != "hello" {
		t.Errorf("snapshot transcript not isolated from original: %+v", snap.Transcript)
	}
	if len(snap.Metadata) != 1 || snap.Metadata["k1"] != "v1" {
		t.Errorf("snapshot metadata not isolated from original: %+v", snap.Metadata)
	}

	// And the snapshot must not alias the original's slice/map.
	snap.Transcript[0].Text = "mutated"
	if v.GetTranscript()[0].Text == "mutated" {
		t.Error("snapshot aliases original transcript slice")
	}
	snap.Metadata["k1"] = "mutated"
	origMeta := make(map[string]string)
	v.mu.RLock()
	for k, val := range v.Metadata {
		origMeta[k] = val
	}
	v.mu.RUnlock()
	if origMeta["k1"] == "mutated" {
		t.Error("snapshot aliases original metadata map")
	}
}
