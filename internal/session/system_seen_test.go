package session

import (
	"fmt"
	"testing"
)

func TestMarkSystemContentSeen_ReportsFirstSightOnly(t *testing.T) {
	s := NewSession("s", "b", "c")
	if !s.MarkSystemContentSeen("h1") {
		t.Fatal("first sight of h1 must report true")
	}
	if s.MarkSystemContentSeen("h1") {
		t.Fatal("second sight of h1 must report false")
	}
	if !s.MarkSystemContentSeen("h2") {
		t.Fatal("first sight of h2 must report true")
	}
}

// The set is filled from client-controlled content, so it must not grow
// without bound.
func TestMarkSystemContentSeen_IsBounded(t *testing.T) {
	s := NewSession("s", "b", "c")
	for i := 0; i < 3*maxSeenSystemHashes; i++ {
		s.MarkSystemContentSeen(fmt.Sprintf("h%d", i))
		if n := len(s.seenSystemHashes); n > maxSeenSystemHashes {
			t.Fatalf("after %d distinct hashes the set holds %d, over the bound of %d", i+1, n, maxSeenSystemHashes)
		}
	}
	last := fmt.Sprintf("h%d", 3*maxSeenSystemHashes-1)
	if s.MarkSystemContentSeen(last) {
		t.Fatal("the most recent hash must still be remembered after the set was trimmed")
	}
}

// A Session rebuilt from a stored blob has no set yet.
func TestMarkSystemContentSeen_WorksOnAZeroSession(t *testing.T) {
	s := &Session{}
	if !s.MarkSystemContentSeen("h") || s.MarkSystemContentSeen("h") {
		t.Fatal("a zero-value Session must record first sight")
	}
}
