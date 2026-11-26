package redaction

import (
	"math/rand"
	"strings"
	"testing"
)

// unguarded is the default pattern set with the prefilters removed: the
// reference the guarded redactor must match byte for byte.
func unguarded() *PatternRedactor {
	ps := defaultPatterns(false)
	for i := range ps {
		ps[i].MayMatch = nil
	}
	return NewPatternRedactorWithPatterns(ps)
}

// Every default pattern has a prefilter, and the prefilter never skips text
// its regexp would change.
func TestPrefilter_EveryDefaultPatternIsGuarded(t *testing.T) {
	for _, p := range defaultPatterns(false) {
		if p.MayMatch == nil {
			t.Errorf("pattern %s has no prefilter", p.Name)
		}
	}
}

func TestPrefilter_SameOutputAsUnguardedOnPositiveSamples(t *testing.T) {
	ref, got := unguarded(), NewPatternRedactor()
	samples := []string{
		"mail alice@example.com now",
		"ssn 123-45-6789 here",
		"card 4111 1111 1111 1111 ok",
		"call (555) 123-4567 or 555-123-4567",
		"Authorization: BEARER abcdefghijklmnopqrstuvwxyz0123",
		"key sk-abcdefghijklmnopqrstuvwxyz and SK-ABCDEFGHIJKLMNOPQRSTUVWXYZ",
		"API_KEY=abcdefghijklmnop1234 Secret-Key: abcdefghijklmnop1234 AUTH_TOKEN abcdefghijklmnop1234",
		`{"Password": "hunter2x"} and PWD=hunter2x and passwd: hunter2x`,
		"ip 8.8.8.8 and 10.0.0.1 and 127.0.0.1",
		"jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abc_DEF-123",
		"aws akiaiosfodnn7example and AKIAIOSFODNN7EXAMPLE",
		"SECRET=" + strings.Repeat("A", 44) + " private_key: " + strings.Repeat("b", 44),
		// Unicode folds the (?i) regexps accept: Kelvin sign and long s.
		"\u212Aey=abcdefghijklmnop1234 \u017Fk-abcdefghijklmnopqrstuvwxyz \u017Fecret=" + strings.Repeat("A", 44),
	}
	for _, s := range samples {
		if want, have := ref.Redact(s), got.Redact(s); want != have {
			t.Errorf("guarded output differs for %q:\n want %q\n have %q", s, want, have)
		}
	}
}

func TestPrefilter_SameOutputAsUnguardedOnRandomText(t *testing.T) {
	ref, got := unguarded(), NewPatternRedactor()
	words := []string{"key", "KEY", "token", "Secret", "secret", "private", "pwd", "password", "passwd",
		"bearer", "Bearer", "sk-", "akia", "AKIA", "eyJ", "@", "a.b", ".", "-", "=", ":", " ", "\n", `"`,
		"4111", "1111", "123", "45", "6789", "8.8.8.8", "x@y.io", "\u212A", "\u017F", "é", "abcdefghijklmnopqrst"}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 3000; i++ {
		var b strings.Builder
		for n := rng.Intn(14); n >= 0; n-- {
			b.WriteString(words[rng.Intn(len(words))])
			if rng.Intn(2) == 0 {
				b.WriteByte(' ')
			}
		}
		s := b.String()
		if want, have := ref.Redact(s), got.Redact(s); want != have {
			t.Fatalf("guarded output differs for %q:\n want %q\n have %q", s, want, have)
		}
	}
}
