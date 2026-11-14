package instruction

import (
	"reflect"
	"strings"
	"testing"
)

func hiddenScanner(t *testing.T, rules ...Rule) *Scanner {
	t.Helper()
	s, err := NewScanner(rules)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var hiddenX = Rule{Name: "instruction_hidden_x", Patterns: []string{"[\\x{200B}\\x{202E}]"}, Severity: "critical", Action: "flag"}

func TestHiddenContentTally(t *testing.T) {
	result := hiddenScanner(t, hiddenX).Scan("a​b\n\nc‮d‮\ne​")
	if len(result.Violations) != 1 {
		t.Fatalf("violations = %d", len(result.Violations))
	}
	v := result.Violations[0]
	if v.MatchedText != "" {
		t.Errorf("matched text = %q", v.MatchedText)
	}
	if v.Hidden == nil || v.Hidden.Total != 4 {
		t.Fatalf("hidden = %+v", v.Hidden)
	}
	if !reflect.DeepEqual(v.Hidden.CodePoints, map[string]int{"U+200B": 2, "U+202E": 2}) {
		t.Errorf("code points = %v", v.Hidden.CodePoints)
	}
	if !reflect.DeepEqual(v.Hidden.Lines, []int{1, 3, 4}) {
		t.Errorf("lines = %v", v.Hidden.Lines)
	}
}

func TestHiddenContentLineCap(t *testing.T) {
	content := strings.Repeat("x​\n", 30)
	v := hiddenScanner(t, hiddenX).Scan(content).Violations[0]
	if len(v.Hidden.Lines) != MaxHiddenLines || v.Hidden.Total != 30 {
		t.Errorf("lines=%d total=%d", len(v.Hidden.Lines), v.Hidden.Total)
	}
}

func TestHiddenContentLeadingBOM(t *testing.T) {
	s := hiddenScanner(t, Rule{Name: "instruction_hidden_x", Patterns: []string{"[\\x{FEFF}]"}, Severity: "critical", Action: "flag"})
	if r := s.Scan("\ufeff# Rules"); len(r.Violations) != 0 {
		t.Errorf("leading BOM counted: %+v", r.Violations)
	}
	r := s.Scan("# Rules\ufeff")
	if len(r.Violations) != 1 || !reflect.DeepEqual(r.Violations[0].Hidden.CodePoints, map[string]int{"U+FEFF": 1}) {
		t.Errorf("trailing BOM: %+v", r.Violations)
	}
}

func TestStrippedBeforeOtherRules(t *testing.T) {
	s := hiddenScanner(t, Rule{Name: "instruction_prompt_injection", Patterns: []string{`ignore\s+(all\s+)?previous`}, Severity: "critical", Action: "block"}, hiddenX)
	r := s.Scan("ig​nore previous")
	if len(r.Violations) != 2 {
		t.Fatalf("violations = %+v", r.Violations)
	}
}

func TestLegacyHiddenRuleNameStillTallies(t *testing.T) {
	s := hiddenScanner(t, Rule{Name: RuleHiddenContent, Patterns: []string{"[​]"}, Severity: "critical", Action: "block"})
	r := s.Scan("a​b")
	if len(r.Violations) != 1 || r.Violations[0].Hidden == nil {
		t.Fatalf("%+v", r.Violations)
	}
}

func TestStripInvisible(t *testing.T) {
	if got := stripInvisible("a​⁠b\U000E0041c"); got != "abc" {
		t.Errorf("got %q", got)
	}
}
