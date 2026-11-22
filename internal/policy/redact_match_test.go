package policy

import (
	"strings"
	"testing"
)

// A rule that exists to catch credentials must not copy the credential into
// the violation it raises. The engine matches against lowercased content, so
// the violation text is the secret, lowercased, and it is read by the control
// API, the websocket log and the block log as well as by storage.
func TestRedactMatchKeepsTheMatchedSecretOutOfTheViolation(t *testing.T) {
	secret := "AKIA" + strings.Repeat("A", 16)
	build := func(redact bool) *Engine {
		return NewEngine(Config{
			Enabled: true,
			Mode:    "enforce",
			Rules: []Rule{{
				Name: "secret_in_request", Type: RuleTypeContentMatch, Target: RuleTargetRequest,
				Patterns: []string{`\b(AKIA|ASIA)[0-9A-Z]{16}\b`}, Severity: SeverityCritical,
				Action: "flag", RedactMatch: redact,
			}},
		})
	}
	content := "please use " + secret + " for the deploy"

	res := build(true).EvaluateRequestContent("s1", content)
	if res == nil || len(res.Violations) != 1 {
		t.Fatalf("want one violation, got %+v", res)
	}
	v := res.Violations[0]
	if strings.Contains(strings.ToLower(v.MatchedText), strings.ToLower(secret)) {
		t.Errorf("violation carries the secret: %q", v.MatchedText)
	}
	if v.MatchedText != RedactedMatch {
		t.Errorf("MatchedText = %q, want %q", v.MatchedText, RedactedMatch)
	}

	// Without the flag the behavior is unchanged.
	res = build(false).EvaluateRequestContent("s2", content)
	if res == nil || len(res.Violations) != 1 || !strings.Contains(res.Violations[0].MatchedText, strings.ToLower(secret)) {
		t.Errorf("without RedactMatch the match text must be recorded as before: %+v", res)
	}
}
