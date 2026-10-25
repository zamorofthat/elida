package main

import (
	"net/http"
	"testing"
	"time"

	"elida/internal/policy"
)

// Both save paths (the proxy's flagged-session save and the session-end
// save) persist the per-decision list and the representative metadata.
func TestSessionEnd_PersistsSemanticDecisions(t *testing.T) {
	quietLogs(t)
	a, send := historyApp(t)
	if code := send("sess-sd-end", "please zzprobezz now"); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	a.policyEngine.RecordSemanticViolation("sess-sd-end", policy.Violation{
		RuleName: "semantic_injection", Description: "semantic", Severity: policy.SeverityWarning,
		Action: "flag", Timestamp: time.Now(), SourceRole: "tool", MessageIndex: 2,
		EventCategory: "semantic_injection",
		Semantic: &policy.SemanticEvidence{DecisionID: "dec-end-1", Signal: "injection", Probability: 0.7,
			Model: "m", ModelVersion: "v", ThresholdSet: "v1", ExecutionMode: "inline", ProtectionScope: "current_request"},
	})
	a.manager.DrainActiveSessions()

	rec, err := a.sqliteStore.GetSession("sess-sd-end")
	if err != nil || rec == nil {
		t.Fatalf("GetSession: %v %v", rec, err)
	}
	if len(rec.SemanticDecisions) != 1 || rec.SemanticDecisions[0].Evidence.DecisionID != "dec-end-1" {
		t.Fatalf("decisions = %+v", rec.SemanticDecisions)
	}
	var found bool
	for _, v := range rec.Violations {
		if v.RuleName == "semantic_injection" {
			found = v.Semantic != nil && v.Semantic.DecisionID == "dec-end-1" && v.EventID == rec.SemanticDecisions[0].EventID
		}
	}
	if !found {
		t.Fatalf("the stored violation must carry its representative decision: %+v", rec.Violations)
	}
}

// The proxy's flagged-session save (on every response of a flagged
// session) persists the list before the session ends.
func TestFlaggedSave_PersistsSemanticDecisionsBeforeSessionEnd(t *testing.T) {
	quietLogs(t)
	a, send := historyApp(t)
	if code := send("sess-sd-flagged", "please zzprobezz now"); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	a.policyEngine.RecordSemanticViolation("sess-sd-flagged", policy.Violation{
		RuleName: "semantic_injection", Description: "semantic", Severity: policy.SeverityWarning,
		Action: "flag", Timestamp: time.Now(), SourceRole: "tool", MessageIndex: 2,
		EventCategory: "semantic_injection",
		Semantic: &policy.SemanticEvidence{DecisionID: "dec-flagged-1", Signal: "injection", Probability: 0.7,
			Model: "m", ModelVersion: "v", ThresholdSet: "v1", ExecutionMode: "inline", ProtectionScope: "current_request"},
	})
	if code := send("sess-sd-flagged", "hello again"); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}

	rec, err := a.sqliteStore.GetSession("sess-sd-flagged")
	if err != nil || rec == nil {
		t.Fatalf("GetSession: %v %v", rec, err)
	}
	if rec.State != "flagged" {
		t.Fatalf("state = %q, want the proxy's flagged save", rec.State)
	}
	if len(rec.SemanticDecisions) != 1 || rec.SemanticDecisions[0].Evidence.DecisionID != "dec-flagged-1" {
		t.Fatalf("decisions = %+v", rec.SemanticDecisions)
	}
	var found bool
	for _, v := range rec.Violations {
		if v.RuleName == "semantic_injection" {
			found = v.Semantic != nil && v.Semantic.DecisionID == "dec-flagged-1"
		}
	}
	if !found {
		t.Fatalf("the stored violation must carry its representative decision: %+v", rec.Violations)
	}

	// Session end saves the same session again: no duplicate records.
	a.manager.DrainActiveSessions()
	rec, err = a.sqliteStore.GetSession("sess-sd-flagged")
	if err != nil || rec == nil || len(rec.SemanticDecisions) != 1 {
		t.Fatalf("after session end: %+v %v", rec, err)
	}
}
