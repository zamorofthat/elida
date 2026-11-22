package session

import "testing"

// A session keeps its newest MaxSemanticShadow decisions and
// MaxCorrelationShadow correlation matches and silently discarded the rest, so
// a reader of a long session could not tell evidence was missing. The drop
// counters are the only trace of what was lost.

func TestSemanticShadowDropsAreCounted(t *testing.T) {
	s := NewSession("s1", "b", "c")
	for i := 0; i < MaxSemanticShadow; i++ {
		s.RecordSemanticShadow(SemanticShadow{})
	}
	if snap := s.Snapshot(); snap.SemanticShadowDropped != 0 || len(snap.SemanticShadow) != MaxSemanticShadow {
		t.Fatalf("at the cap: dropped=%d len=%d, want 0/%d", snap.SemanticShadowDropped, len(snap.SemanticShadow), MaxSemanticShadow)
	}
	for i := 0; i < 3; i++ {
		s.RecordSemanticShadow(SemanticShadow{})
	}
	snap := s.Snapshot()
	if snap.SemanticShadowDropped != 3 {
		t.Errorf("SemanticShadowDropped = %d, want 3", snap.SemanticShadowDropped)
	}
	if len(snap.SemanticShadow) != MaxSemanticShadow {
		t.Errorf("list length = %d, want it held at %d", len(snap.SemanticShadow), MaxSemanticShadow)
	}
	if snap.CorrelationShadowDropped != 0 {
		t.Errorf("CorrelationShadowDropped = %d, want 0 (separate list)", snap.CorrelationShadowDropped)
	}
}

func TestCorrelationShadowDropsAreCounted(t *testing.T) {
	s := NewSession("s1", "b", "c")
	for i := 0; i < MaxCorrelationShadow+4; i++ {
		s.RecordCorrelationShadow(CorrelationShadow{})
	}
	snap := s.Snapshot()
	if snap.CorrelationShadowDropped != 4 {
		t.Errorf("CorrelationShadowDropped = %d, want 4", snap.CorrelationShadowDropped)
	}
	if len(snap.CorrelationShadow) != MaxCorrelationShadow {
		t.Errorf("list length = %d, want it held at %d", len(snap.CorrelationShadow), MaxCorrelationShadow)
	}
	if snap.SemanticShadowDropped != 0 {
		t.Errorf("SemanticShadowDropped = %d, want 0 (separate list)", snap.SemanticShadowDropped)
	}
}
