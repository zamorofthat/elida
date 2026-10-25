package policy

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"elida/internal/correlation"
)

func lockTestEngine(t *testing.T, rules int) *Engine {
	t.Helper()
	var rs []correlation.Rule
	for i := 0; i < rules; i++ {
		rs = append(rs, correlation.Rule{
			Name: fmt.Sprintf("r%d", i), Shape: correlation.ShapeRepeated,
			Category: "semantic_injection", Count: 2, Window: time.Minute,
			Severity: "critical", Mode: correlation.ModeAudit,
		})
	}
	return NewEngine(Config{
		Enabled: true, Mode: "enforce",
		RiskLadder: RiskLadderConfig{Enabled: true},
		Correlation: CorrelationConfig{
			Enabled: true, Rules: rs,
			Limits: correlation.Limits{MaxHistoryEvents: 1024, MaxSessions: 64},
		},
	})
}

func lockInput(id string, at time.Time) Violation {
	return Violation{
		RuleName: "semantic_injection", EventID: id, EventCategory: "semantic_injection",
		Severity: SeverityCritical, SourceRole: "user", Timestamp: at, EvidenceOnly: true,
	}
}

// Wiring review I-3: Observe runs with the policy lock released. While an
// Observe is held (however slow it is), a concurrent policy check on another
// session and a read of the same session both complete.
func TestCorrelationLock_ObserveDoesNotHoldPolicyLock(t *testing.T) {
	e := lockTestEngine(t, correlation.MaxRules)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	e.beforeObserve = func() {
		once.Do(func() {
			close(started)
			<-release
		})
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		e.recordViolations("sess-slow", []Violation{lockInput("ev_a", time.Now())})
	}()
	<-started

	checked := make(chan struct{})
	go func() {
		defer close(checked)
		e.recordViolations("sess-other", []Violation{{RuleName: "other", Severity: SeverityWarning, Timestamp: time.Now()}})
		_, _, _ = e.GetSessionRiskScore("sess-other")
		_ = e.GetFlaggedSession("sess-slow")
		_ = e.Stats()
	}()
	select {
	case <-checked:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("a policy check blocked behind an in-flight Observe: Observe holds the policy lock")
	}
	close(release)
	<-done
}

// A match is recorded once per match ID at the policy layer (M-7), and
// matches for a session that ended while Observe ran are discarded.
func TestCorrelationLock_MatchDedupAndEndedSession(t *testing.T) {
	e := lockTestEngine(t, 1)
	now := time.Now()
	e.recordViolations("sess-d", []Violation{lockInput("ev_a", now)})
	e.recordViolations("sess-d", []Violation{lockInput("ev_b", now.Add(time.Second))})
	fs := e.GetFlaggedSession("sess-d")
	var id string
	for _, v := range fs.Violations {
		if v.RuleName == "correlation.r0" {
			id = v.EventID
		}
	}
	if id == "" || fs.ViolationCounts["correlation.r0"] != 1 {
		t.Fatalf("want one match, got %+v", fs.ViolationCounts)
	}
	// Re-entering the same match ID records nothing.
	e.mu.Lock()
	m := e.matchViolationLocked(correlation.Match{ID: id, Rule: "r0", Severity: "critical", Mode: correlation.ModeAudit, LastEvent: now})
	e.recordViolationsLocked("sess-d", []Violation{m}, nil)
	e.mu.Unlock()
	if got := e.GetFlaggedSession("sess-d").ViolationCounts["correlation.r0"]; got != 1 {
		t.Fatalf("a re-entered match ID must be recorded once, count = %d", got)
	}

	// The session ends while Observe runs: the policy entry is gone when
	// the match comes back, so the match is discarded and the state that
	// Observe touched is forgotten.
	e.recordViolations("sess-e", []Violation{lockInput("ev_c", now)})
	e.beforeObserve = func() {
		e.mu.Lock()
		e.removeFlaggedLocked("sess-e")
		e.mu.Unlock()
	}
	e.recordViolations("sess-e", []Violation{lockInput("ev_d", now.Add(time.Second))})
	if got := e.correlationDiscarded.Load(); got != 1 {
		t.Fatalf("want one discarded match, got %d", got)
	}
	if fs := e.GetFlaggedSession("sess-e"); fs != nil {
		t.Fatalf("an ended session must not be re-flagged by its match: %+v", fs.Violations)
	}
	if e.correlation.Stats().Sessions != 1 { // sess-d only
		t.Fatal("the ended session's correlation state must be forgotten")
	}
}

// endDuringObserve simulates a session end (the flagged entry is deleted;
// the session-end Forget already ran) while Observe is in flight.
func endDuringObserve(e *Engine, sessionID string) func() {
	return func() {
		e.mu.Lock()
		e.removeFlaggedLocked(sessionID)
		e.mu.Unlock()
	}
}

// Re-review N-2: a session that ends during an Observe that returns no
// match has the state that Observe re-created forgotten, so a reused ID
// cannot inherit partial history.
func TestCorrelationLock_EndedSessionWithoutMatchIsForgotten(t *testing.T) {
	e := lockTestEngine(t, 1)
	e.beforeObserve = endDuringObserve(e, "sess-n2")
	e.recordViolations("sess-n2", []Violation{lockInput("ev_a", time.Now())})
	if got := e.correlation.Stats().Sessions; got != 0 {
		t.Fatalf("an ended session's re-created state must be forgotten, sessions = %d", got)
	}
	// A reused ID starts empty: one new event does not complete a match.
	e.beforeObserve = nil
	e.recordViolations("sess-n2", []Violation{lockInput("ev_b", time.Now())})
	if fs := e.GetFlaggedSession("sess-n2"); fs.ViolationCounts["correlation.r0"] != 0 {
		t.Fatal("a reused session ID inherited the ended instance's history")
	}
}

// Re-review N-3: a match for a session instance that was replaced by a
// reused ID during Observe is dropped and counted, not recorded on the new
// instance.
func TestCorrelationLock_ReplacedInstanceDropsMatch(t *testing.T) {
	e := lockTestEngine(t, 1)
	now := time.Now()
	e.recordViolations("sess-n3", []Violation{lockInput("ev_a", now)})
	e.beforeObserve = func() {
		e.mu.Lock()
		e.removeFlaggedLocked("sess-n3")
		e.recordViolationsLocked("sess-n3", []Violation{{RuleName: "other", Severity: SeverityWarning, Timestamp: now}}, nil)
		e.mu.Unlock()
	}
	e.recordViolations("sess-n3", []Violation{lockInput("ev_b", now.Add(time.Second))})
	fs := e.GetFlaggedSession("sess-n3")
	if fs == nil || fs.ViolationCounts["correlation.r0"] != 0 {
		t.Fatalf("the old instance's match landed on the new instance: %+v", fs)
	}
	if got := e.correlationDiscarded.Load(); got != 1 {
		t.Fatalf("want one discarded match, got %d", got)
	}
}

// Re-review N-4: a match from an engine that ReloadConfig replaced during
// Observe is dropped and counted.
func TestCorrelationLock_SwappedEngineDropsMatch(t *testing.T) {
	e := lockTestEngine(t, 1)
	now := time.Now()
	e.recordViolations("sess-n4", []Violation{lockInput("ev_a", now)})
	e.beforeObserve = func() {
		cfg := e.GetConfig()
		cfg.Correlation.Rules[0].Count = 3
		e.ReloadConfig(cfg)
	}
	e.recordViolations("sess-n4", []Violation{lockInput("ev_b", now.Add(time.Second))})
	if fs := e.GetFlaggedSession("sess-n4"); fs.ViolationCounts["correlation.r0"] != 0 {
		t.Fatal("a match from a replaced engine must not be recorded")
	}
	if got := e.correlationDiscarded.Load(); got != 1 {
		t.Fatalf("want one discarded match, got %d", got)
	}
}
