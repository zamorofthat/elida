package policy

import (
	"fmt"
	"math"
	"testing"
)

func TestRecordInstructionViolation(t *testing.T) {
	rows := []struct {
		role, sev string
		want      float64
	}{
		{"provenance", "critical", 10.0},
		{"provenance", "warning", 3.0},
		{"provenance", "info", 1.0},
		{"system", "critical", 1.0},
		{"system", "high", 1.0},
		{"system", "medium", 0.3},
		{"system", "low", 0.1},
	}
	e := NewEngine(Config{Enabled: true, Mode: "enforce", RiskLadder: RiskLadderConfig{Enabled: true}})
	for i, r := range rows {
		id := fmt.Sprintf("instr-sess-%d", i)
		e.RecordInstructionViolation(id, Violation{
			RuleName: "instruction_integrity", Severity: Severity(r.sev), SourceRole: r.role,
		})
		got, _, _ := e.GetSessionRiskScore(id)
		if math.Abs(got-r.want) > 0.05 {
			t.Errorf("%s/%s: score = %v, want %v", r.role, r.sev, got, r.want)
		}
	}

	t.Run("clears content and still scores evidence-only", func(t *testing.T) {
		e.RecordInstructionViolation("instr-clear", Violation{
			RuleName: "r", Severity: "critical", SourceRole: "provenance",
			MatchedText: "m", MatchedPattern: "p", SourceContent: "c", EvidenceOnly: true,
		})
		fs := e.GetFlaggedSession("instr-clear")
		if fs == nil || len(fs.Violations) == 0 {
			t.Fatal("expected a recorded violation")
		}
		for _, v := range fs.Violations {
			if v.MatchedText != "" || v.MatchedPattern != "" || v.SourceContent != "" {
				t.Errorf("content not cleared: %+v", v)
			}
			if v.EvidenceOnly {
				t.Error("EvidenceOnly must be forced false")
			}
		}
		if s, _, _ := e.GetSessionRiskScore("instr-clear"); s < 9.9 {
			t.Errorf("evidence-only input should still score, got %v", s)
		}
	})
	t.Run("empty rule records nothing", func(t *testing.T) {
		e.RecordInstructionViolation("instr-empty", Violation{Severity: "critical"})
		if e.GetFlaggedSession("instr-empty") != nil {
			t.Error("expected no flagged session")
		}
	})
	t.Run("weight key", func(t *testing.T) {
		if _, ok := SourceRoleWeights["provenance"]; !ok {
			t.Error("missing provenance weight")
		}
	})
}
