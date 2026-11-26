package instruction

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const shapeDefaultThreshold = 0.7

var shapeFixtureNames = []string{
	"hook_context.md",
	"mcp_instructions.md",
	"self_identifying.md",
	"plan.md",
	"spec.md",
	"readme.md",
	"changelog.md",
	"source.go.txt",
}

func shapeFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "shape", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(b)
}

func TestShapeFixtures(t *testing.T) {
	tests := []struct {
		name string
		want bool // classifyByShape >= the default threshold
	}{
		{"hook_context.md", true},
		{"mcp_instructions.md", true},
		{"self_identifying.md", true},
		{"plan.md", false},
		{"spec.md", false},
		{"readme.md", false},
		{"changelog.md", false},
		{"source.go.txt", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := classifyByShape(shapeFixture(t, tt.name))
			if got := score >= shapeDefaultThreshold; got != tt.want {
				t.Errorf("score = %.2f, qualifies = %v, want %v", score, got, tt.want)
			}
		})
	}
}

// The mechanical root cause of the old scorer missing hook context: it asked
// whether text was about a software project, and this text is not.
func TestShapeDirectiveTextNeedsNoProjectVocabulary(t *testing.T) {
	content := shapeFixture(t, "hook_context.md")
	if score := addressScore(content); score < shapeDefaultThreshold {
		t.Errorf("addressScore = %.2f, want >= %.2f", score, shapeDefaultThreshold)
	}
	if selfIdentifies(content) {
		t.Error("fixture self-identifies; it must qualify on address alone")
	}
}

func TestShapeSelfIdentificationQualifiesAlone(t *testing.T) {
	content := shapeFixture(t, "self_identifying.md")
	if score := addressScore(content); score >= shapeDefaultThreshold {
		t.Errorf("addressScore = %.2f, want < %.2f", score, shapeDefaultThreshold)
	}
	if !selfIdentifies(content) {
		t.Error("selfIdentifies = false, want true")
	}
	if score := classifyByShape(content); score != 1.0 {
		t.Errorf("classifyByShape = %v, want 1.0", score)
	}
}

func TestShapeSelfIdentificationWindow(t *testing.T) {
	filler := strings.Repeat("The quarterly figures were broadly flat. ", 18) // 738 bytes
	if len(filler) < 700 {
		t.Fatalf("filler is %d bytes, want >= 700", len(filler))
	}
	if selfIdentifies(filler + "\n# CLAUDE.md\n") {
		t.Error("a self-identifying line after the window qualified")
	}
	if !selfIdentifies("# CLAUDE.md\n" + filler) {
		t.Error("the same line at the top did not qualify")
	}
}

func TestShapeNoSingleSignalQualifies(t *testing.T) {
	if len(addressSignals) == 0 {
		t.Fatal("no address signals")
	}
	var sum float64
	for i, sig := range addressSignals {
		if sig.weight >= shapeDefaultThreshold {
			t.Errorf("signal %d weight %.2f qualifies alone", i, sig.weight)
		}
		sum += sig.weight
	}
	if sum <= 1.0 {
		t.Errorf("weights sum to %.2f, want > 1.0", sum)
	}
}

func TestShapeIgnoresReplayLineNumbers(t *testing.T) {
	for _, name := range shapeFixtureNames {
		t.Run(name, func(t *testing.T) {
			content := shapeFixture(t, name)
			lines := strings.Split(content, "\n")
			for i, line := range lines {
				lines[i] = fmt.Sprintf("%6d\t", i+1) + line
			}
			plain, numbered := addressScore(content), addressScore(strings.Join(lines, "\n"))
			if math.Abs(plain-numbered) > 1e-9 {
				t.Errorf("score %.6f became %.6f with line numbers", plain, numbered)
			}
		})
	}
}

func TestShapeSkipsFencedCodeAndTables(t *testing.T) {
	const loud = "you must never always do not the user IMPORTANT"
	fenced := "\n```\n" + strings.Repeat(loud+"\n", 25) + "```\n"
	table := "\n| a | b |\n|---|---|\n" + strings.Repeat("| "+loud+" | "+loud+" |\n", 20)
	for _, name := range []string{"readme.md", "changelog.md"} {
		t.Run(name, func(t *testing.T) {
			content := shapeFixture(t, name)
			plain := addressScore(content)
			if plain <= 0 || plain >= 1 {
				t.Fatalf("fixture score %.2f is clamped; the comparison would prove nothing", plain)
			}
			if got := addressScore(content + fenced + table); math.Abs(plain-got) > 1e-9 {
				t.Errorf("score %.6f became %.6f with a fenced block and a table appended", plain, got)
			}
		})
	}
}

func TestShapeBelowProseFloor(t *testing.T) {
	if score := addressScore("You must never do that. Always ask the user. IMPORTANT."); score != 0 {
		t.Errorf("addressScore = %v, want 0 below the prose floor", score)
	}
	if score := classifyByShape("Short."); score != 0 {
		t.Errorf("classifyByShape = %v, want 0", score)
	}
}
