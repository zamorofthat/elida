package unit

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"elida/internal/policy"
)

// flaggedCopyEngine returns an engine with one flagged session that already
// has a violation, an event and a capture, so later writes update entries
// in place as well as appending.
func flaggedCopyEngine(t *testing.T, sessionID string) *policy.Engine {
	t.Helper()
	pe := newRiskLadderEngine(baselineRules(), []policy.RiskThreshold{
		{Score: 1000, Action: policy.ActionTerminate},
	})
	pe.RecordSemanticViolation(sessionID, flaggedCopyViolation(0))
	pe.CaptureRequest(sessionID, policy.CapturedRequest{Method: "POST", Path: "/v1/chat"})
	return pe
}

func flaggedCopyViolation(i int) policy.Violation {
	return policy.Violation{
		RuleName:      "semantic_injection",
		Severity:      policy.SeverityInfo,
		Action:        "flag",
		SourceRole:    "tool",
		EventCategory: "semantic_injection",
		Timestamp:     time.Now(),
		Semantic: &policy.SemanticEvidence{
			Signal:      "injection",
			Probability: 0.9,
			DecisionID:  fmt.Sprintf("dec-%d", i),
		},
	}
}

// TestPolicy_FlaggedSessionViewsAreDeepCopies: a reader iterating the view
// returned by GetFlaggedSession(s) while a writer records violations and
// captures must not race. Run under -race; a shallow copy shares the
// Violations, ViolationEvents and CapturedContent backing arrays and the
// ViolationCounts map with the engine.
func TestPolicy_FlaggedSessionViewsAreDeepCopies(t *testing.T) {
	const id = "sess-copy-race"
	pe := flaggedCopyEngine(t, id)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 1; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			pe.RecordSemanticViolation(id, flaggedCopyViolation(i))
			pe.UpdateLastCaptureWithResponseAndStatus(id, "ok", 200)
			pe.CaptureRequest(id, policy.CapturedRequest{Method: "POST", Path: "/v1/chat"})
		}
	}()

	var seen int
	read := func(fs *policy.FlaggedSession) {
		n := 0
		for _, v := range fs.Violations {
			n += len(v.RuleName) + len(v.EventID)
			if v.Semantic != nil {
				n += len(v.Semantic.DecisionID)
			}
			_ = v.Timestamp
		}
		for _, ev := range fs.ViolationEvents {
			n += len(ev.RuleName)
		}
		for _, c := range fs.CapturedContent {
			n += len(c.ResponseBody) + c.StatusCode
		}
		for k, c := range fs.ViolationCounts {
			n += len(k) + c
		}
		seen += n
	}
	for i := 0; i < 200; i++ {
		if fs := pe.GetFlaggedSession(id); fs != nil {
			read(fs)
		}
		for _, fs := range pe.GetFlaggedSessions() {
			read(fs)
		}
		for _, fs := range pe.GetFlaggedSessionsBySeverity(policy.SeverityInfo) {
			read(fs)
		}
	}
	close(stop)
	wg.Wait()
	if seen == 0 {
		t.Fatal("the reader saw nothing; the fixture did not flag the session")
	}
}

// TestPolicy_FlaggedSessionViewMutationDoesNotReachTheEngine: the view is
// the caller's to change.
func TestPolicy_FlaggedSessionViewMutationDoesNotReachTheEngine(t *testing.T) {
	const id = "sess-copy-mut"
	pe := flaggedCopyEngine(t, id)

	fs := pe.GetFlaggedSession(id)
	fs.Violations[0].RuleName = "tampered"
	fs.Violations[0].Semantic.DecisionID = "tampered"
	fs.ViolationEvents[0].RuleName = "tampered"
	fs.CapturedContent[0].Path = "tampered"
	fs.ViolationCounts["semantic_injection"] = 99

	got := pe.GetFlaggedSession(id)
	if got.Violations[0].RuleName != "semantic_injection" ||
		got.Violations[0].Semantic.DecisionID != "dec-0" ||
		got.ViolationEvents[0].RuleName != "semantic_injection" ||
		got.CapturedContent[0].Path != "/v1/chat" ||
		got.ViolationCounts["semantic_injection"] != 1 {
		t.Fatalf("mutating the returned view changed engine state: %+v", got)
	}
}
