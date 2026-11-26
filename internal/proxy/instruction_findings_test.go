package proxy

import (
	"math"
	"testing"

	"elida/internal/instruction"
	"elida/internal/policy"
)

func TestStaticSeverity(t *testing.T) {
	high := &instruction.InstructionFile{Confidence: instruction.ConfidenceHigh, SourcePath: "/repo/CLAUDE.md"}
	medium := &instruction.InstructionFile{Confidence: instruction.ConfidenceMedium}
	rows := []struct {
		name string
		file *instruction.InstructionFile
		v    instruction.Violation
		want string
	}{
		{"block action is critical", high, instruction.Violation{RuleName: "instruction_pipe_to_shell", Severity: "medium", Action: "block"}, "critical"},
		{"flag keeps its severity", high, instruction.Violation{RuleName: "instruction_shell_exec", Severity: "warning", Action: "flag"}, "warning"},
		{"shape-detected non-hidden is info", medium, instruction.Violation{RuleName: "instruction_shell_exec", Severity: "critical", Action: "flag"}, "info"},
		{"shape cap wins over block", medium, instruction.Violation{RuleName: "instruction_pipe_to_shell", Severity: "critical", Action: "block"}, "info"},
		{"shape-detected hidden keeps its severity", medium, instruction.Violation{RuleName: "instruction_hidden_bidi", Severity: "critical", Action: "flag"}, "critical"},
		{"shape-detected hidden block is critical", medium, instruction.Violation{RuleName: "instruction_hidden_format", Severity: "medium", Action: "block"}, "critical"},
	}
	for _, r := range rows {
		if got := staticSeverity(r.file, r.v); got != r.want {
			t.Errorf("%s: got %q, want %q", r.name, got, r.want)
		}
	}
}

// A shape-detected file's non-hidden hit scores as info at system weight:
// SeverityWeights[info] 1 x SourceRoleWeights[system] 0.1 = 0.1.
func TestShapeDetectedStaticHitScore(t *testing.T) {
	engine := policy.NewEngine(policy.Config{
		Enabled: true,
		Mode:    "enforce",
		RiskLadder: policy.RiskLadderConfig{
			Enabled:    true,
			Thresholds: []policy.RiskThreshold{{Score: 90, Action: policy.ActionWarn}},
		},
	})
	p := &Proxy{policy: engine}
	file := &instruction.InstructionFile{Type: instruction.FileTypeClaudeMD, Confidence: instruction.ConfidenceMedium, Hash: "0123456789abcdef"}
	obs := instruction.Observation{Static: []instruction.Violation{{RuleName: "instruction_shell_exec", Severity: "critical", Action: "flag"}}}
	if !p.recordInstructionFindings("s1", file, obs) {
		t.Fatal("nothing recorded")
	}
	got, _, _ := engine.GetSessionRiskScore("s1")
	if math.Abs(got-0.1) > 1e-6 {
		t.Fatalf("risk score = %.4f, want 0.1", got)
	}
}

// A hidden-content hit with no invisible code point has no labels: its
// description omits the code-point clause.
func TestHiddenDescriptionWithoutLabels(t *testing.T) {
	file := &instruction.InstructionFile{Type: instruction.FileTypeClaudeMD, SourcePath: "/repo/CLAUDE.md", Hash: "0123456789abcdef"}
	v := instruction.Violation{RuleName: "instruction_hidden_x", Hidden: &instruction.HiddenContent{CodePoints: map[string]int{}, Lines: []int{}, Total: 3}}
	if got, want := staticDescription(file, v), "instruction_hidden_x in /repo/CLAUDE.md (claude_md, 0123456789ab)"; got != want {
		t.Fatalf("description = %q, want %q", got, want)
	}
}
