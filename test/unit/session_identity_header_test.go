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

// TestClaudeCodeSessionHeader pins that Claude Code's own session header
// identifies a session when the configured header is absent, ahead of the
// body-derived id, and that an unusable value is ignored rather than trusted.
func TestClaudeCodeSessionHeader(t *testing.T) {
	const cc = "0c1d2e3f-aaaa-bbbb-cccc-000000000001"
	tests := []struct {
		name    string
		headers map[string]string
		body    string
		// wantID is the exact id; "" means any non-empty id other than notID.
		wantID string
		notID  string
	}{
		{name: "Claude header alone", headers: map[string]string{session.ClaudeCodeSessionHeader: cc}, wantID: cc},
		{name: "configured header wins", headers: map[string]string{session.ClaudeCodeSessionHeader: cc, "X-Session-ID": "op-1"}, wantID: "op-1"},
		{name: "header beats body", headers: map[string]string{session.ClaudeCodeSessionHeader: cc}, body: `{"user":"u-9"}`, wantID: cc},
		{name: "unsafe value", headers: map[string]string{session.ClaudeCodeSessionHeader: "a b/../c"}, notID: "a b/../c"},
		{name: "65 characters", headers: map[string]string{session.ClaudeCodeSessionHeader: strings.Repeat("a", 65)}, notID: strings.Repeat("a", 65)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, _ := newIdentityProxy(t)
			rec := postIdentity(p, "10.0.0.1:1", tt.headers, tt.body)
			got := rec.Header().Get("X-Session-ID")
			if tt.wantID != "" && got != tt.wantID {
				t.Errorf("session id = %q, want %q", got, tt.wantID)
			}
			if tt.notID != "" && (got == tt.notID || got == "") {
				t.Errorf("session id = %q, want a non-empty id other than %q", got, tt.notID)
			}
		})
	}

	t.Run("two addresses, one conversation", func(t *testing.T) {
		p, manager := newIdentityProxy(t)
		h := map[string]string{session.ClaudeCodeSessionHeader: cc}
		a := postIdentity(p, "10.0.0.1:1", h, "").Header().Get("X-Session-ID")
		b := postIdentity(p, "10.0.0.2:1", h, "").Header().Get("X-Session-ID")
		if a == "" || a != b {
			t.Errorf("ids = %q and %q, want one shared non-empty id", a, b)
		}
		if total := manager.Stats().Total; total != 1 {
			t.Errorf("Stats().Total = %d, want 1", total)
		}
	})
}

func newIdentityProxy(t *testing.T) (*proxy.Proxy, *session.Manager) {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(backend.Close)
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)
	cfg := &config.Config{
		Backend: backend.URL,
		Session: config.SessionConfig{Header: "X-Session-ID", GenerateIfMissing: true, Timeout: 5 * time.Minute},
	}
	cfg.Session.DeriveFrom.OpenAIUser = true
	p, err := proxy.New(cfg, store, manager)
	if err != nil {
		t.Fatalf("proxy.New: %v", err)
	}
	return p, manager
}

func postIdentity(p *proxy.Proxy, remote string, headers map[string]string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.RemoteAddr = remote
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	return rec
}
