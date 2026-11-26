package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"elida/internal/instruction"
)

// The narrow-block warning names the rules and never their patterns.
func TestNarrowBlockWarningNamesRulesNotPatterns(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	warnRulesBlockingOutsideNarrowSet([]instruction.Rule{
		{Name: "instruction_prompt_injection", Patterns: []string{`ignore\s+previous`}, Severity: "critical", Action: "block"},
		{Name: "my_secret_rule", Patterns: []string{`zebra-pattern-[0-9]+`}, Severity: "critical", Action: "block"},
		{Name: "instruction_pipe_to_shell", Patterns: []string{`curl\s+x`}, Severity: "critical", Action: "block"},
	})

	out := buf.String()
	for _, p := range []string{`ignore\\s+previous`, "zebra-pattern", `curl\\s+x`, "ignore", "zebra"} {
		if strings.Contains(out, p) {
			t.Errorf("warning carries pattern text %q", p)
		}
	}
	var rec struct {
		Level string   `json:"level"`
		Rules []string `json:"rules"`
	}
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("one JSON record expected: %v", err)
	}
	if rec.Level != "WARN" {
		t.Errorf("level = %q, want WARN", rec.Level)
	}
	if want := []string{"instruction_prompt_injection", "my_secret_rule"}; !reflect.DeepEqual(rec.Rules, want) {
		t.Errorf("rules = %v, want %v", rec.Rules, want)
	}

	buf.Reset()
	warnRulesBlockingOutsideNarrowSet([]instruction.Rule{{Name: "instruction_pipe_to_shell", Action: "block"}})
	if buf.Len() != 0 {
		t.Error("warning logged with no rule outside the narrow set")
	}
}
