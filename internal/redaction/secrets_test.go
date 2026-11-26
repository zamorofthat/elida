package redaction

import (
	"strings"
	"testing"
)

// Token shapes are assembled at run time so this file never contains a
// string that a secret scanner would flag.
func fakeSecrets() map[string]string {
	return map[string]string{
		"aws_akia":     "AKIA" + strings.Repeat("A", 16),
		"aws_asia":     "ASIA" + strings.Repeat("B", 16),
		"github_pat":   "ghp_" + strings.Repeat("a1", 18),
		"github_fine":  "github_pat_" + strings.Repeat("a1", 22),
		"slack_bot":    "xoxb-" + strings.Repeat("1", 12) + "-" + strings.Repeat("a", 24),
		"anthropic":    "sk-ant-" + "api03-" + strings.Repeat("Ab1_", 10),
		"pem_header":   "-----BEGIN " + "RSA PRIVATE KEY-----",
		"pem_header_2": "-----BEGIN " + "OPENSSH PRIVATE KEY-----",
	}
}

// A secret that reaches a stored violation or captured body must not survive
// redaction, whichever provider issued it.
func TestCommonProviderSecretsAreRedacted(t *testing.T) {
	r := NewPatternRedactor()
	for name, secret := range fakeSecrets() {
		in := "use this credential: " + secret + " for the deploy"
		out := r.Redact(in)
		if strings.Contains(out, secret) {
			t.Errorf("%s: secret survived redaction: %q", name, out)
		}
		if !strings.Contains(out, "[REDACTED_") {
			t.Errorf("%s: no redaction marker in %q", name, out)
		}
	}
}

// Ordinary prose and code that merely resembles a token prefix is left alone.
func TestSecretPatternsLeaveOrdinaryTextAlone(t *testing.T) {
	r := NewPatternRedactor()
	for _, s := range []string{
		"the ghp_ prefix marks a GitHub personal access token",
		"AKIA is the prefix of an AWS access key id",
		"ask-ant-eater is a hyphenated word",
		"xoxb tokens start with xoxb-",
		"-----BEGIN CERTIFICATE-----",
	} {
		if out := r.Redact(s); out != s {
			t.Errorf("ordinary text altered: %q -> %q", s, out)
		}
	}
}
