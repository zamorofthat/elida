package redaction

import (
	"strings"
	"testing"
)

// Measured feedback #10: [REDACTED_PHONE] hit 716 unix timestamps; CC hit
// hashes with no Luhn check; every IP hit was loopback. These tables come
// from the field data.
func TestPhoneRequiresFormatContext(t *testing.T) {
	r := NewPatternRedactor()
	redacted := []string{
		"call me at 555-867-5309",
		"call (415) 555-2671 today",
		"+1 415.555.2671",
	}
	untouched := []string{
		`"created": 1753305600`,     // unix timestamp — the 716x false positive
		"id 1753305600123",          // bare digit run
		"n_params 7241000000",       // model metadata
		"9415-555-2671 order ref",   // digit run bleeding into phone-shaped tail (prevented by \b)
		"count 1234555234667 items", // bare digit run resembling phone but too long
	}
	for _, s := range redacted {
		if out := r.Redact(s); !strings.Contains(out, "[REDACTED_PHONE]") {
			t.Errorf("real phone not redacted: %q -> %q", s, out)
		}
	}
	for _, s := range untouched {
		if out := r.Redact(s); out != s {
			t.Errorf("false positive: %q -> %q", s, out)
		}
	}
}

func TestCreditCardRequiresLuhn(t *testing.T) {
	r := NewPatternRedactor()
	// 4532015112830366 passes Luhn (Visa test number); 4532015112830367 fails.
	if out := r.Redact("card 4532015112830366 exp 12/28"); !strings.Contains(out, "[REDACTED_CC]") {
		t.Errorf("Luhn-valid CC not redacted: %q", out)
	}
	if out := r.Redact("hash 4532015112830367 in metadata"); strings.Contains(out, "[REDACTED_CC]") {
		t.Errorf("non-Luhn digit run redacted: %q", out)
	}
	// Separated form still validates.
	if out := r.Redact("4532 0151 1283 0366"); !strings.Contains(out, "[REDACTED_CC]") {
		t.Errorf("separated Luhn-valid CC not redacted: %q", out)
	}
}

func TestPrivateIPsSkippedByDefault(t *testing.T) {
	r := NewPatternRedactor()
	untouched := []string{
		"--host 127.0.0.1 --port 8001", // 100% of the measured IP hits
		"gateway 172.17.0.1",
		"lan 192.168.1.5",
		"internal 10.0.0.7",
	}
	for _, s := range untouched {
		if out := r.Redact(s); out != s {
			t.Errorf("private/loopback IP redacted by default: %q -> %q", s, out)
		}
	}
	if out := r.Redact("connect to 203.0.113.9 now"); !strings.Contains(out, "[REDACTED_IP]") {
		t.Errorf("public IP not redacted: %q", out)
	}
}

func TestPrivateIPsRedactedWhenOptedIn(t *testing.T) {
	r := NewPatternRedactorWithOptions(Options{RedactPrivateIPs: true})
	if out := r.Redact("--host 127.0.0.1"); !strings.Contains(out, "[REDACTED_IP]") {
		t.Errorf("opt-in private IP redaction not applied: %q", out)
	}
}

// The secret patterns were CORRECT in the field data — pin them.
func TestSecretPatternsStillWork(t *testing.T) {
	r := NewPatternRedactor()
	cases := map[string]string{
		"Authorization: Bearer abcdefghijklmnopqrstuvwxyz1234": "[REDACTED",
		"key sk-abcdefghijklmnopqrstuvwx":                      "[REDACTED",
		`"password": "hunter2secret"`:                          "[REDACTED",
		"ssn 123-45-6789":                                      "[REDACTED",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abc":             "[REDACTED_JWT]",
		"AKIAIOSFODNN7EXAMPLE":                                 "[REDACTED_AWS_KEY]",
	}
	for in, want := range cases {
		if out := r.Redact(in); !strings.Contains(out, want) {
			t.Errorf("secret/PII no longer redacted: %q -> %q", in, out)
		}
	}
}

func TestLuhnValid(t *testing.T) {
	if !luhnValid("4532015112830366") {
		t.Error("valid Luhn rejected")
	}
	if luhnValid("4532015112830367") {
		t.Error("invalid Luhn accepted")
	}
	if luhnValid("12") {
		t.Error("too-short accepted")
	}
}
