package instruction

import (
	"sync"
	"testing"
)

func TestRegistryStats(t *testing.T) {
	reg := newProvenanceRegistry(t)
	reg.SetChangedSinceLastSeen(true)

	observe(reg, "s1", true, provPath, provA)
	observe(reg, "s1", true, provPath, provB) // uncaused change, scored
	observe(reg, "s2", false, provPath, provA)
	observe(reg, "s2", false, provPath, provB) // change, suppressed

	got := reg.Stats()
	want := Stats{
		IdentifiedSessions: 1, FallbackSessions: 1, TrackedPaths: 1,
		Changes: 2, SuppressedFindings: 1, ChangedSinceLastSeen: true,
	}
	if got != want {
		t.Fatalf("Stats() = %+v, want %+v", got, want)
	}
	reg.SetChangedSinceLastSeen(false)
	if reg.Stats().ChangedSinceLastSeen {
		t.Fatal("ChangedSinceLastSeen did not follow the setter")
	}
}

func TestRegistryStatsMarkCausesBeforeObserveIsUncounted(t *testing.T) {
	reg := newProvenanceRegistry(t)
	markEdit(reg, "s1", "t1")
	if s := reg.Stats(); s.IdentifiedSessions != 0 || s.FallbackSessions != 0 {
		t.Fatalf("session not yet observed was counted: %+v", s)
	}
	observe(reg, "s1", true, provPath, provA)
	if s := reg.Stats(); s.IdentifiedSessions != 1 {
		t.Fatalf("after Observe: %+v", s)
	}
}

func TestRegistryStopThenObserveDoesNotPanic(t *testing.T) {
	scanner, err := NewScanner(nil)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry(scanner, newMockStore(), 4)
	reg.Stop()
	// Both senders must be safe after Stop.
	observe(reg, "s1", true, provPath, provA)
	observe(reg, "s1", true, provPath, provB)
	reg.Stop()

	reg2 := NewRegistry(scanner, newMockStore(), 4)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				content := provA + string(rune('a'+(i+j)%26)) + "\n"
				observe(reg2, "s", true, provPath, content)
			}
		}(i)
	}
	reg2.Stop()
	wg.Wait()
}
