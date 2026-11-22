package redaction

import (
	"strings"
	"testing"
)

// The policy engine matches against lowercased content, so the violation text
// it hands the redactor is lowercase. Every secret pattern must still catch it.
func TestSecretPatternsCatchLowercasedText(t *testing.T) {
	r := NewPatternRedactor()
	for name, secret := range fakeSecrets() {
		lowered := strings.ToLower(secret)
		out := r.Redact("matched: " + lowered)
		if strings.Contains(out, lowered) {
			t.Errorf("%s: lowercased secret survived: %q", name, out)
		}
	}
}

// A private key header with no END line must not blank the rest of a capture:
// documentation and code mention the header all the time, and a caller must not
// be able to erase the record after it.
func TestPrivateKeyHeaderDoesNotSwallowTheRestOfTheCapture(t *testing.T) {
	r := NewPatternRedactor()
	tail := strings.Repeat("the deploy then failed with a timeout. ", 400)
	in := "-----BEGIN " + "PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\n" + tail
	out := r.Redact(in)
	if strings.Contains(out, "MIIEvQIBADANBgkqhkiG9w0BAQEFAASC") {
		t.Errorf("key material survived: %q", out[:80])
	}
	if !strings.Contains(out, "the deploy then failed with a timeout.") {
		t.Error("text far after an unterminated key header was redacted")
	}

	closed := "-----BEGIN " + "RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\n-----END " + "RSA PRIVATE KEY-----\nand then more text"
	out = r.Redact(closed)
	if strings.Contains(out, "MIIEowIBAAKCAQEA") || !strings.HasSuffix(out, "and then more text") {
		t.Errorf("terminated key block: got %q", out)
	}
}

// OpenAI project and service-account keys contain hyphens.
func TestOpenAIProjectKeysAreRedacted(t *testing.T) {
	r := NewPatternRedactor()
	for _, key := range []string{
		"sk-proj-" + strings.Repeat("Ab1_-", 12),
		"sk-svcacct-" + strings.Repeat("Ab1_-", 12),
	} {
		if out := r.Redact("key " + key); strings.Contains(out, key[:20]) {
			t.Errorf("project key survived: %q", out)
		}
	}
}
