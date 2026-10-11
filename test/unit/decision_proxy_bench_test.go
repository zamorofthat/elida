package unit

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/proxy"
	"elida/internal/session"
)

// BenchmarkDecisionProxy_ServeHTTPDisabled measures the request path with
// decision disabled (no assessor wired), with and without a policy engine.
// It is the before/after comparison for the message-extraction hoist.
func BenchmarkDecisionProxy_ServeHTTPDisabled(b *testing.B) {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(prev)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer backend.Close()
	body := `{"messages":[{"role":"user","content":"please summarize this"},{"role":"assistant","content":"sure"},{"role":"user","content":"thanks a lot"}]}`

	for _, withPolicy := range []bool{false, true} {
		name := "no_policy"
		if withPolicy {
			name = "policy"
		}
		b.Run(name, func(b *testing.B) {
			store := session.NewMemoryStore()
			manager := session.NewManager(store, 5*time.Minute)
			cfg := &config.Config{
				Backend: backend.URL,
				Session: config.SessionConfig{Header: "X-Session-ID", GenerateIfMissing: true, Timeout: 5 * time.Minute},
			}
			var opts []proxy.ProxyOption
			if withPolicy {
				opts = append(opts, proxy.WithPolicyEngine(newRiskLadderEngine(baselineRules(), nil)))
			}
			p, err := proxy.New(cfg, store, manager, opts...)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Session-ID", "bench-sess")
				w := httptest.NewRecorder()
				p.ServeHTTP(w, req)
			}
		})
	}
}
