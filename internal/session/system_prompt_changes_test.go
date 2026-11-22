package session

import "testing"

// A change to a session's system prompt was a log line and nothing else. The
// counter is what an operator can query and alert on.
func TestSystemPromptChangesAreCounted(t *testing.T) {
	s := NewSession("s1", "b", "c")
	if got := s.Snapshot().SystemPromptChanges; got != 0 {
		t.Fatalf("a new session: SystemPromptChanges = %d, want 0", got)
	}
	s.RecordSystemPromptChange()
	s.RecordSystemPromptChange()
	if got := s.Snapshot().SystemPromptChanges; got != 2 {
		t.Errorf("SystemPromptChanges = %d, want 2", got)
	}
}
