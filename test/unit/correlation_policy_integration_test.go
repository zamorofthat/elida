package unit

import (
	"os"
	"path/filepath"
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
	finding := telemetry.BuildPolicyDetection("sess-audit", telemetry.Violation{
		RuleName: got.RuleName, Description: got.Description, Severity: string(got.Severity),
		Action: got.Action, EventCategory: got.EventCategory, EvidenceOnly: got.EvidenceOnly,
	}, telemetry.SessionRecord{})
	if finding.ClassUID != 2004 || len(finding.FindingInfo.Types) != 1 || finding.FindingInfo.Types[0] != correlation.CategoryMatch {
		t.Fatalf("OCSF correlation finding = %#v", finding)
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
