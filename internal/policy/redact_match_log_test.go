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
