package policy

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// The violation record is not the only place a match is written: the engine
// also logs every content violation, with the (lowercased) match and the full
// source message. A rule that redacts its match must keep both out of the log.
func TestRedactMatchKeepsTheSecretOutOfTheStructuredLog(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	secret := "AKIA" + strings.Repeat("Q", 16)
	e := NewEngine(Config{
		Enabled: true,
		Mode:    "enforce",
		Rules: []Rule{{
			Name: "secret_in_request", Type: RuleTypeContentMatch, Target: RuleTargetRequest,
			Patterns: []string{`\b(AKIA|ASIA)[0-9A-Z]{16}\b`}, Severity: SeverityCritical,
			Action: "flag", RedactMatch: true,
		}},
	})
	content := "deploy with " + secret + " now"
	res := e.EvaluateContentWithSource("s1", content, RuleTargetRequest,
		&ContentSource{Role: "user", MessageIndex: 2, Content: content})
	if res == nil || len(res.Violations) != 1 {
		t.Fatalf("want one violation, got %+v", res)
	}

	out := strings.ToLower(buf.String())
	if !strings.Contains(out, "content policy violation detected") {
		t.Fatalf("test setup: the violation was not logged: %q", out)
	}
	if strings.Contains(out, strings.ToLower(secret)) {
		t.Errorf("the structured log carries the secret:\n%s", buf.String())
	}
}

// A rule with no RedactMatch can still match the secret: a loose user rule
// like `deploy\s+\S+\s+\S+` captures "deploy with AKIA...". Once a redacting
// rule matches the message, no violation from it, whichever rule raised it,
// may carry the secret in its match text or in the log.
func TestRedactMatchCoversTheMatchTextOfOtherRules(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	secret := "AKIA" + strings.Repeat("M", 16)
	e := NewEngine(Config{
		Enabled: true,
		Mode:    "enforce",
		Rules: []Rule{
			{Name: "secret_in_request", Type: RuleTypeContentMatch, Target: RuleTargetRequest,
				Patterns: []string{`\b(AKIA|ASIA)[0-9A-Z]{16}\b`}, Severity: SeverityCritical,
				Action: "flag", RedactMatch: true},
			{Name: "loose_deploy_rule", Type: RuleTypeContentMatch, Target: RuleTargetRequest,
				Patterns: []string{`deploy\s+\S+\s+\S+`}, Severity: SeverityWarning, Action: "flag"},
		},
	})
	content := "please deploy with " + secret + " to prod"
	res := e.EvaluateContentWithSource("s1", content, RuleTargetRequest,
		&ContentSource{Role: "user", MessageIndex: 1, Content: content})
	if res == nil || len(res.Violations) != 2 {
		t.Fatalf("want both rules to fire, got %+v", res)
	}
	for _, v := range res.Violations {
		if strings.Contains(strings.ToLower(v.MatchedText), strings.ToLower(secret)) {
			t.Errorf("rule %s recorded the secret in its match text: %q", v.RuleName, v.MatchedText)
		}
	}
	if strings.Contains(strings.ToLower(buf.String()), strings.ToLower(secret)) {
		t.Errorf("the structured log carries the secret:\n%s", buf.String())
	}
}
