package unit

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"elida/internal/storage"
)

func TestSDRCanonicalizationStableAcrossMapOrder(t *testing.T) {
	timestamp := time.Date(2026, 5, 3, 14, 30, 0, 123, time.FixedZone("EDT", -4*60*60))
	eventA := storage.Event{
		ID:        7,
		Timestamp: timestamp,
		Type:      storage.EventPolicyAction,
		SessionID: "session-1",
		Severity:  "warning",
		Data:      json.RawMessage(`{"b":2,"a":1}`),
	}
	eventB := eventA
	eventB.Data = json.RawMessage(`{"a":1,"b":2}`)

	canonicalA, err := storage.CanonicalEventJSON(eventA)
	if err != nil {
		t.Fatalf("canonicalize event A: %v", err)
	}
	canonicalB, err := storage.CanonicalEventJSON(eventB)
	if err != nil {
		t.Fatalf("canonicalize event B: %v", err)
	}
	if string(canonicalA) != string(canonicalB) {
		t.Fatalf("expected stable canonical JSON, got\nA=%s\nB=%s", canonicalA, canonicalB)
	}
	if !strings.Contains(string(canonicalA), `"timestamp":"2026-05-03T18:30:00.000000123Z"`) {
		t.Fatalf("expected UTC RFC3339Nano timestamp, got %s", canonicalA)
	}

	hashA, err := storage.HashEventLeaf(eventA)
	if err != nil {
		t.Fatalf("hash event A: %v", err)
	}
	hashB, err := storage.HashEventLeaf(eventB)
	if err != nil {
		t.Fatalf("hash event B: %v", err)
	}
	if hashA != hashB {
		t.Fatalf("expected stable hash across map order, got %s != %s", hashA, hashB)
	}
}

func TestSDRCanonicalizationChangesHashWhenFieldChanges(t *testing.T) {
	event := storage.Event{
		ID:        1,
		Timestamp: time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC),
		Type:      storage.EventViolationDetected,
		SessionID: "session-1",
		Severity:  "warning",
		Data:      json.RawMessage(`{"rule":"one"}`),
	}
	original, err := storage.HashEventLeaf(event)
	if err != nil {
		t.Fatal(err)
	}

	event.Severity = "critical"
	changed, err := storage.HashEventLeaf(event)
	if err != nil {
		t.Fatal(err)
	}
	if original == changed {
		t.Fatal("expected severity change to alter event leaf hash")
	}
}

func TestMerkleRootAndProofVerification(t *testing.T) {
	leaves := []string{
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}
	root, err := storage.ComputeMerkleRoot(leaves)
	if err != nil {
		t.Fatal(err)
	}

	for i, leaf := range leaves {
		path, buildErr := storage.BuildMerkleProof(leaves, i)
		if buildErr != nil {
			t.Fatalf("build proof for leaf %d: %v", i, buildErr)
		}
		proof := storage.SDRProof{
			EventIndex:  i,
			EventHash:   leaf,
			RootHash:    root,
			SiblingPath: path,
		}
		if !storage.VerifySDRProof(proof) {
			t.Fatalf("expected proof for leaf %d to verify", i)
		}
	}

	path, err := storage.BuildMerkleProof(leaves, 1)
	if err != nil {
		t.Fatal(err)
	}
	validProof := storage.SDRProof{EventIndex: 1, EventHash: leaves[1], RootHash: root, SiblingPath: path}

	tamperedHash := validProof
	tamperedHash.EventHash = leaves[0]
	if storage.VerifySDRProof(tamperedHash) {
		t.Fatal("expected proof with wrong event hash to fail")
	}

	tamperedPath := validProof
	tamperedPath.SiblingPath = append([]storage.MerkleProofNode(nil), validProof.SiblingPath...)
	tamperedPath.SiblingPath[0].Hash = leaves[2]
	if storage.VerifySDRProof(tamperedPath) {
		t.Fatal("expected proof with wrong sibling path to fail")
	}

	tamperedRoot := validProof
	tamperedRoot.RootHash = leaves[0]
	if storage.VerifySDRProof(tamperedRoot) {
		t.Fatal("expected proof with wrong root to fail")
	}

	tamperedIndex := validProof
	tamperedIndex.EventIndex = 0
	if storage.VerifySDRProof(tamperedIndex) {
		t.Fatal("expected proof with wrong index to fail")
	}
}

func TestMerkleProofPowerOf2AndOddLeafCounts(t *testing.T) {
	makeLeaves := func(n int) []string {
		leaves := make([]string, n)
		for i := range leaves {
			h := strings.Repeat(string(rune('a'+i)), 64)
			leaves[i] = h
		}
		return leaves
	}

	for _, tc := range []struct {
		name  string
		count int
	}{
		{"2 leaves (power of 2)", 2},
		{"4 leaves (power of 2)", 4},
		{"5 leaves (odd, exercises duplication)", 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			leaves := makeLeaves(tc.count)
			root, err := storage.ComputeMerkleRoot(leaves)
			if err != nil {
				t.Fatalf("ComputeMerkleRoot: %v", err)
			}
			if root == "" {
				t.Fatal("expected non-empty root")
			}

			for i, leaf := range leaves {
				path, buildErr := storage.BuildMerkleProof(leaves, i)
				if buildErr != nil {
					t.Fatalf("BuildMerkleProof(%d): %v", i, buildErr)
				}
				proof := storage.SDRProof{
					EventIndex:  i,
					EventHash:   leaf,
					RootHash:    root,
					SiblingPath: path,
				}
				if !storage.VerifySDRProof(proof) {
					t.Fatalf("proof for leaf %d failed verification", i)
				}
			}

			// Verify wrong hash fails
			path, buildErr := storage.BuildMerkleProof(leaves, 0)
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			bad := storage.SDRProof{
				EventIndex:  0,
				EventHash:   leaves[tc.count-1],
				RootHash:    root,
				SiblingPath: path,
			}
			if tc.count > 1 && storage.VerifySDRProof(bad) {
				t.Fatal("expected proof with wrong event hash to fail")
			}
		})
	}
}

func TestSingleLeafMerkleRootEqualsLeaf(t *testing.T) {
	leaf := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	root, err := storage.ComputeMerkleRoot([]string{leaf})
	if err != nil {
		t.Fatal(err)
	}
	if root != leaf {
		t.Fatalf("expected single-leaf root to equal leaf hash, got %s", root)
	}
}

func TestSDRIntegrityStorageAndProof(t *testing.T) {
	store := newTempIntegrityStore(t)
	defer store.Close()

	ctx := context.Background()
	sessionID := "session-integrity"

	if err := store.RecordEvent(ctx, storage.EventSessionEnded, sessionID, "", storage.SessionEndedData{State: "completed"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordEvent(ctx, storage.EventTokensUsed, sessionID, "", storage.TokensUsedData{TokensIn: 10, TokensOut: 20}); err != nil {
		t.Fatal(err)
	}

	integrity, err := store.ComputeAndStoreSDRIntegrity(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if integrity == nil {
		t.Fatal("expected integrity metadata")
		return
	}
	if integrity.EventCount != 2 {
		t.Fatalf("expected 2 events, got %d", integrity.EventCount)
	}
	if integrity.RootHash == "" {
		t.Fatal("expected root hash")
	}

	loaded, err := store.GetSDRIntegrity(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil || loaded.RootHash != integrity.RootHash {
		t.Fatalf("expected stored root %s, got %#v", integrity.RootHash, loaded)
	}

	events, err := store.GetSessionEvents(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := store.GetSDRProof(ctx, sessionID, events[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if proof == nil {
		t.Fatal("expected proof")
		return
	}
	if !storage.VerifySDRProof(*proof) {
		t.Fatal("expected stored proof to verify")
	}
}

func TestSDRIntegrityMissingSession(t *testing.T) {
	store := newTempIntegrityStore(t)
	defer store.Close()

	integrity, err := store.GetSDRIntegrity(context.Background(), "missing")
	if err != nil {
		t.Fatal(err)
	}
	if integrity != nil {
		t.Fatalf("expected nil integrity for missing session, got %#v", integrity)
	}

	proof, err := store.GetSDRProof(context.Background(), "missing", 1)
	if err != nil {
		t.Fatal(err)
	}
	if proof != nil {
		t.Fatalf("expected nil proof for missing session, got %#v", proof)
	}
}

func newTempIntegrityStore(t *testing.T) *storage.SQLiteStore {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "elida-integrity-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()
	t.Cleanup(func() { _ = os.Remove(tmpFile.Name()) })

	store, err := storage.NewSQLiteStore(tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	return store
}
