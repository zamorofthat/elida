package main

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"elida/internal/policy"
	"elida/internal/storage"
)

// The session-end path writes violation_detected rows that say which
// violations were evidence only, so stored history never presents audit
// evidence as an ordinary finding.
func TestSessionEnd_ViolationEventsCarryEvidenceOnly(t *testing.T) {
	quietLogs(t)
	a, send := historyApp(t)

	if code := send("sess-evidence", "please zzprobezz now"); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	a.policyEngine.RecordSemanticViolation("sess-evidence", policy.Violation{
		RuleName:      "semantic_injection",
		Description:   "semantic injection signal (audit)",
		Severity:      policy.SeverityCritical,
		Action:        "flag",
		Timestamp:     time.Now(),
		SourceRole:    "tool",
		EventCategory: "semantic_injection",
		EvidenceOnly:  true,
		Semantic:      &policy.SemanticEvidence{DecisionID: "dec-evidence-1"},
	})
	a.manager.DrainActiveSessions()

	evs, err := a.sqliteStore.GetSessionEvents("sess-evidence")
	if err != nil {
		t.Fatalf("GetSessionEvents: %v", err)
	}
	got := map[string]storage.ViolationDetectedData{}
	for _, ev := range evs {
		if ev.Type != storage.EventViolationDetected {
			continue
		}
		var d storage.ViolationDetectedData
		if err := json.Unmarshal(ev.Data, &d); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		got[d.RuleName] = d
	}
	if d, ok := got["semantic_injection"]; !ok || !d.EvidenceOnly {
		t.Fatalf("semantic_injection event = %+v (present %v), want evidence_only true", d, ok)
	}
	if d, ok := got["probe_rule"]; !ok || d.EvidenceOnly {
		t.Fatalf("probe_rule event = %+v (present %v), want an ordinary violation", d, ok)
	}
}
