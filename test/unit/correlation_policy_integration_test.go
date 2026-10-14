package unit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/correlation"
	"elida/internal/policy"
	"elida/internal/telemetry"
)

func correlationPolicyEngine(t *testing.T, mode correlation.Mode) *policy.Engine {
	t.Helper()
	return policy.NewEngine(policy.Config{
		Enabled: true,
		RiskLadder: policy.RiskLadderConfig{
			Enabled:    true,
			Thresholds: []policy.RiskThreshold{{Score: 5, Action: policy.ActionWarn}},
		},
		Correlation: policy.CorrelationConfig{
			Enabled: true,
			Limits:  okLimits(),
			Rules: []correlation.Rule{{
				Name: "repeated_injection", Shape: correlation.ShapeRepeated,
				Category: "semantic_injection", Count: 2, Window: time.Minute,
				Severity: "critical", Mode: mode,
			}},
		},
	})
}

func recordCorrelationInput(e *policy.Engine, sessionID, eventID string, at time.Time) {
	e.RecordSemanticViolation(sessionID, policy.Violation{
		RuleName: "semantic_injection", EventID: eventID,
		EventCategory: "semantic_injection", Severity: policy.SeverityCritical,
		SourceRole: "user", MessageIndex: 0, Timestamp: at, EvidenceOnly: true,
	})
}

func TestCorrelationPolicy_AuditMatchIsEvidenceOnlyAndExportsOCSF(t *testing.T) {
	e := correlationPolicyEngine(t, correlation.ModeAudit)
	now := time.Now()
	recordCorrelationInput(e, "sess-audit", "ev_a", now)
	recordCorrelationInput(e, "sess-audit", "ev_b", now.Add(time.Second))

	fs := e.GetFlaggedSession("sess-audit")
	if fs == nil || len(fs.ViolationEvents) != 3 {
		t.Fatalf("events = %#v, want two inputs plus one correlation match", fs)
	}
	got := fs.Violations[len(fs.Violations)-1]
	if got.RuleName != "correlation.repeated_injection" || got.EventCategory != correlation.CategoryMatch {
		t.Fatalf("correlation violation = %#v", got)
	}
	if !got.EvidenceOnly || fs.RiskScore != 0 || fs.CurrentAction != string(policy.ActionObserve) {
		t.Fatalf("audit match contributed risk: evidence=%v score=%v action=%q", got.EvidenceOnly, fs.RiskScore, fs.CurrentAction)
	}
	// The match ID is the violation's event ID and its evidence's match ID.
	if !strings.HasPrefix(got.EventID, "cm_") || got.Correlation == nil || got.Correlation.MatchID != got.EventID {
		t.Fatalf("match ID: event_id=%q evidence=%+v", got.EventID, got.Correlation)
	}
	// Every field the session-end export copies (cmd/elida enrichRecordFromPolicy).
	finding := telemetry.BuildPolicyDetection("sess-audit", telemetry.Violation{
		RuleName: got.RuleName, Description: got.Description, Severity: string(got.Severity),
		MatchedText: got.MatchedText, Action: got.Action, EventCategory: got.EventCategory,
		FrameworkRef: got.FrameworkRef, SourceRole: got.SourceRole,
		EvidenceOnly: got.EvidenceOnly, EventID: got.EventID,
	}, telemetry.SessionRecord{})
	if finding.ClassUID != 2004 || len(finding.FindingInfo.Types) != 1 || finding.FindingInfo.Types[0] != correlation.CategoryMatch {
		t.Fatalf("OCSF correlation finding = %#v", finding)
	}
	if !strings.HasPrefix(finding.FindingInfo.Title, correlation.RuleNamePrefix) || finding.Analytic.Name != "correlation.repeated_injection" {
		t.Fatalf("the finding must carry the correlation.-prefixed rule name: %+v %+v", finding.FindingInfo, finding.Analytic)
	}
	if !finding.Unmapped.EvidenceOnly || finding.SeverityID != telemetry.OCSFSeverityInfo ||
		!strings.HasPrefix(finding.Message, "Policy evidence (no risk contributed)") {
		t.Fatalf("an audit match exports as informational evidence: sev=%d evidence=%v msg=%q",
			finding.SeverityID, finding.Unmapped.EvidenceOnly, finding.Message)
	}
	// No new export (C-1 allowlist): the match ID is not exported (it is on
	// /control/flagged/{id}), analytic.uid is not misused for it, and no
	// event ID or semantic metadata is attached to a non-semantic finding.
	if finding.Analytic.UID != "" || finding.Unmapped.EventID != "" || finding.Unmapped.Semantic != nil {
		t.Fatalf("correlation finding exports more than the allowlist: %+v %+v", finding.Analytic, finding.Unmapped)
	}
	raw, err := json.Marshal(finding)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), got.EventID) || strings.Contains(string(raw), "ev_a") {
		t.Fatalf("the finding must not carry match or contributing event IDs: %s", raw)
	}
}

func TestCorrelationPolicy_EnforceMatchFeedsRiskLadder(t *testing.T) {
	e := correlationPolicyEngine(t, correlation.ModeEnforce)
	now := time.Now()
	recordCorrelationInput(e, "sess-enforce", "ev_a", now)
	recordCorrelationInput(e, "sess-enforce", "ev_b", now.Add(time.Second))

	fs := e.GetFlaggedSession("sess-enforce")
	if fs == nil || fs.RiskScore < 5 || fs.CurrentAction != string(policy.ActionWarn) {
		t.Fatalf("enforced correlation did not feed risk ladder: %#v", fs)
	}
	if got := fs.Violations[len(fs.Violations)-1]; got.EvidenceOnly {
		t.Fatal("enforce match was recorded as evidence-only")
	}
}

func TestCorrelationConfig_LoadsAndRejectsReservedPolicyPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "elida.yaml")
	err := os.WriteFile(path, []byte(`
listen: :8080
backend: http://localhost:11434
correlation:
  enabled: true
  max_history_events: 32
  max_sessions: 100
  rules:
    - name: repeated_injection
      shape: repeated
      category: semantic_injection
      count: 2
      count_by: message
      window: 10m
      severity: critical
      mode: audit
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Correlation.Enabled || len(cfg.Correlation.Rules) != 1 || cfg.Correlation.Rules[0].CountBy != correlation.CountByMessage {
		t.Fatalf("correlation config = %#v", cfg.Correlation)
	}

	cfg.Policy.Rules = []config.PolicyRule{{Name: "correlation.spoofed"}}
	result := cfg.Validate()
	if result.Valid {
		t.Fatal("reserved correlation policy prefix was accepted")
	}
}
