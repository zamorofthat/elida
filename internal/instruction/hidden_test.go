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

func TestHiddenContentTallyIsCapped(t *testing.T) {
	s := hiddenScanner(t, hiddenX)
	content := strings.Repeat("​", 1<<20) // 3MB of U+200B
	var v Violation
	allocs := testing.AllocsPerRun(1, func() {
		v = s.Scan(content).Violations[0]
	})
	if !v.Hidden.Truncated || v.Hidden.Total != MaxHiddenCounted {
		t.Fatalf("truncated=%v total=%d, want true and %d", v.Hidden.Truncated, v.Hidden.Total, MaxHiddenCounted)
	}
	if got := v.Hidden.CodePoints["U+200B"]; got != MaxHiddenCounted {
		t.Errorf("U+200B count = %d, want %d", got, MaxHiddenCounted)
	}
	// A capped tally allocates a few times per counted match (about 3x the
	// cap, about 6x under -race); an uncapped one allocates for every one of
	// the string's million runes.
	if allocs > 20*MaxHiddenCounted {
		t.Errorf("allocations per scan = %.0f, want at most %d", allocs, 20*MaxHiddenCounted)
	}

	small := s.Scan("a​b").Violations[0].Hidden
	if small.Truncated || small.Total != 1 {
		t.Errorf("small: truncated=%v total=%d, want false and 1", small.Truncated, small.Total)
	}
}

// An operator's hidden-content pattern that matches visible text counts it,
// but only invisible code points are labelled and given line numbers.
func TestHiddenContentLabelsOnlyInvisible(t *testing.T) {
	s := hiddenScanner(t, Rule{Name: "instruction_hidden_x", Patterns: []string{"[a\\x{200B}]"}, Severity: "critical", Action: "flag"})
	h := s.Scan("xa\nyy\n​").Violations[0].Hidden
	if _, ok := h.CodePoints["U+0061"]; ok {
		t.Errorf("visible rune labelled: %v", h.CodePoints)
	}
	if !reflect.DeepEqual(h.CodePoints, map[string]int{"U+200B": 1}) {
		t.Errorf("code points = %v, want only U+200B", h.CodePoints)
	}
	if !reflect.DeepEqual(h.Lines, []int{3}) {
		t.Errorf("lines = %v, want [3]", h.Lines)
	}
	if h.Total != 2 {
		t.Errorf("total = %d, want 2", h.Total)
	}
}
