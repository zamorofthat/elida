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

// The violation also carries the whole message it came from. That message
// holds the secret, so a redacting rule must not record it either.
func TestRedactMatchKeepsTheSecretOutOfTheViolationSourceContent(t *testing.T) {
	secret := "AKIA" + strings.Repeat("Z", 16)
	e := NewEngine(Config{
		Enabled:        true,
		Mode:           "enforce",
		CaptureContent: true,
		MaxCaptureSize: 4096,
		Rules: []Rule{{
			Name: "secret_in_request", Type: RuleTypeContentMatch, Target: RuleTargetRequest,
			Patterns: []string{`\b(AKIA|ASIA)[0-9A-Z]{16}\b`}, Severity: SeverityCritical,
			Action: "flag", RedactMatch: true,
		}},
	})
	content := "deploy with " + secret + " now"
	res := e.EvaluateContentWithSource("s1", content, RuleTargetRequest,
		&ContentSource{Role: "user", MessageIndex: 1, Content: content})
	if res == nil || len(res.Violations) != 1 {
		t.Fatalf("want one violation, got %+v", res)
	}
	got := res.Violations[0].SourceContent
	if strings.Contains(strings.ToLower(got), strings.ToLower(secret)) {
		t.Errorf("SourceContent carries the secret: %q", got)
	}
}

// The secret is in the message, not in one rule's match. When a redacting rule
// matches a message, every other violation recorded from that message (another
// content rule, the entropy rule that fires on a pasted key) must keep the
// message out of its record too.
func TestRedactMatchRedactsSourceContentOfEveryViolationFromThatMessage(t *testing.T) {
	secret := "AKIA" + strings.Repeat("Z", 16)
	noise := ""
	for i := 0; i < 40; i++ {
		noise += string(rune('a'+(i*7)%26)) + string(rune('A'+(i*11)%26)) + string(rune('0'+(i*3)%10)) + "+/"
	}
	e := NewEngine(Config{
		Enabled:        true,
		Mode:           "enforce",
		CaptureContent: true,
		MaxCaptureSize: 4096,
		Rules: []Rule{
			{Name: "secret_in_request", Type: RuleTypeContentMatch, Target: RuleTargetRequest,
				Patterns: []string{`\b(AKIA|ASIA)[0-9A-Z]{16}\b`}, Severity: SeverityCritical,
				Action: "flag", RedactMatch: true},
			{Name: "mentions_deploy", Type: RuleTypeContentMatch, Target: RuleTargetRequest,
				Patterns: []string{`deploy`}, Severity: SeverityWarning, Action: "flag"},
			{Name: "high_entropy", Type: RuleTypeContentEntropy, Target: RuleTargetRequest,
				ThresholdFloat: 3.0, MinSamples: 10, Severity: SeverityWarning, Action: "flag"},
		},
	})
	content := "deploy with " + secret + " " + noise
	res := e.EvaluateContentWithSource("s1", content, RuleTargetRequest,
		&ContentSource{Role: "user", MessageIndex: 1, Content: content})
	if res == nil || len(res.Violations) < 3 {
		t.Fatalf("test setup: want the three rules to fire, got %+v", res)
	}
	for _, v := range res.Violations {
		if strings.Contains(strings.ToLower(v.SourceContent), strings.ToLower(secret)) {
			t.Errorf("rule %s recorded the message with the secret in it: %q", v.RuleName, v.SourceContent)
		}
	}

	// A message the redacting rule does not match is recorded as before.
	other := "deploy to staging " + noise
	res = e.EvaluateContentWithSource("s2", other, RuleTargetRequest,
		&ContentSource{Role: "user", MessageIndex: 1, Content: other})
	if res == nil {
		t.Fatal("expected violations for the second message")
	}
	for _, v := range res.Violations {
		if v.SourceContent == RedactedMatch {
			t.Errorf("rule %s redacted the source of a message with no secret in it", v.RuleName)
		}
	}
}

// Tool arguments are a message too: a loose argument rule must not record a
// secret that a redacting argument rule matched in the same arguments.
func TestRedactMatchCoversOtherToolArgumentRules(t *testing.T) {
	secret := "AKIA" + strings.Repeat("R", 16)
	e := NewEngine(Config{
		Enabled: true,
		Mode:    "enforce",
		Rules: []Rule{
			{Name: "secret_in_tool_args", Type: RuleTypeToolArgumentPattern, Patterns: []string{`\b(AKIA|ASIA)[0-9A-Z]{16}\b`},
				Severity: SeverityCritical, Action: "flag", RedactMatch: true},
			{Name: "loose_arg_rule", Type: RuleTypeToolArgumentPattern, Patterns: []string{`"key":\s*"[^"]+"`},
				Severity: SeverityWarning, Action: "flag"},
		},
	})
	res := e.EvaluateToolCallsUncorrelated("s1", []ToolCall{{Name: "deploy", Arguments: `{"key": "` + secret + `"}`}})
	if res == nil || len(res.Violations) != 2 {
		t.Fatalf("want both argument rules to fire, got %+v", res)
	}
	for _, v := range res.Violations {
		if strings.Contains(strings.ToLower(v.MatchedText), strings.ToLower(secret)) {
			t.Errorf("rule %s recorded the secret in its match text: %q", v.RuleName, v.MatchedText)
		}
	}

	// Arguments with no secret are recorded as before.
	res = e.EvaluateToolCallsUncorrelated("s2", []ToolCall{{Name: "deploy", Arguments: `{"key": "staging"}`}})
	if res == nil || len(res.Violations) != 1 || res.Violations[0].MatchedText == RedactedMatch {
		t.Errorf("arguments without a secret must record their match: %+v", res)
	}
}

// The shipped secret rule is a request-side content rule, but the key travels
// further: a model response can echo it, and a tool call can carry it. A rule on
// any target or type that fires on text holding the secret must not record it.
func TestRedactMatchCoversOtherTargetsAndRuleTypes(t *testing.T) {
	secret := "AKIA" + strings.Repeat("S", 16)
	e := NewEngine(Config{
		Enabled: true,
		Mode:    "enforce",
		Rules: []Rule{
			{Name: "secret_in_request", Type: RuleTypeContentMatch, Target: RuleTargetRequest,
				Patterns: []string{`\b(AKIA|ASIA)[0-9A-Z]{16}\b`}, Severity: SeverityCritical,
				Action: "flag", RedactMatch: true},
			{Name: "loose_response_rule", Type: RuleTypeContentMatch, Target: RuleTargetResponse,
				Patterns: []string{`deploy\s+\S+\s+\S+`}, Severity: SeverityWarning, Action: "flag"},
			{Name: "loose_arg_rule", Type: RuleTypeToolArgumentPattern, Patterns: []string{`"key":\s*"[^"]+"`},
				Severity: SeverityWarning, Action: "flag"},
		},
	})

	res := e.EvaluateResponseContent("s1", "I will deploy with "+secret+" to prod")
	if res == nil || len(res.Violations) != 1 {
		t.Fatalf("want the response rule to fire, got %+v", res)
	}
	if strings.Contains(strings.ToLower(res.Violations[0].MatchedText), strings.ToLower(secret)) {
		t.Errorf("a response rule recorded the secret: %q", res.Violations[0].MatchedText)
	}

	res = e.EvaluateToolCallsUncorrelated("s2", []ToolCall{{Name: "deploy", Arguments: `{"key": "` + secret + `"}`}})
	if res == nil || len(res.Violations) != 1 {
		t.Fatalf("want the argument rule to fire, got %+v", res)
	}
	if strings.Contains(strings.ToLower(res.Violations[0].MatchedText), strings.ToLower(secret)) {
		t.Errorf("an argument rule recorded the secret held by a content-rule match: %q", res.Violations[0].MatchedText)
	}
}
