package redaction

import (
	"encoding/json"
	"strings"
	"testing"
)

// prose builds roughly n bytes of ordinary assistant-style text with a few
// numbers, paths and identifiers so the digit-led patterns have work to do.
func prose(n int) string {
	para := "The deployment finished in 42 seconds on node 7 (build 20260315). " +
		"Open internal/session/manager.go and look at checkTimeouts around line 118; " +
		"the retry count is 3 and the timeout is 30s. Results: 1024 rows, 0.37 ms each, " +
		"total 378.88 ms. Contact the on-call at oncall@example.com if latency exceeds 250 ms. "
	var b strings.Builder
	for b.Len() < n {
		b.WriteString(para)
	}
	return b.String()[:n]
}

func jsonBody(n int) string {
	b, _ := json.Marshal(map[string]any{
		"model":    "m",
		"messages": []map[string]any{{"role": "user", "content": prose(n)}},
	})
	return string(b)
}

func BenchmarkRedactBodyJSON50KB(b *testing.B) {
	r := NewPatternRedactor()
	body := jsonBody(50 * 1024)
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.RedactBody(body)
	}
}

// BenchmarkPerPattern50KB times each default pattern alone over the same text,
// to show which ones account for the cost.
func BenchmarkPerPattern50KB(b *testing.B) {
	text := prose(50 * 1024)
	for _, p := range defaultPatterns(false) {
		p := p
		r := NewPatternRedactorWithPatterns([]Pattern{p})
		b.Run(p.Name, func(b *testing.B) {
			b.SetBytes(int64(len(text)))
			for i := 0; i < b.N; i++ {
				_ = r.Redact(text)
			}
		})
	}
}
