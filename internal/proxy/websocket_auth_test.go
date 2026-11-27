package proxy

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"elida/internal/config"
)

// recordingWS stands in for the WebSocket handler and counts the upgrades it
// is handed.
type recordingWS struct{ calls int64 }

func (r *recordingWS) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	atomic.AddInt64(&r.calls, 1)
	w.WriteHeader(http.StatusSwitchingProtocols)
}

func upgradeRequest(key string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	if key != "" {
		req.Header.Set("X-Elida-API-Key", key)
	}
	return req
}

// proxy.auth guards the proxy. A WebSocket upgrade was handed to the WebSocket
// handler before the auth check ran, so an unauthenticated caller could open a
// WebSocket to the proxy whenever auth was on.
func TestWebSocketUpgradeRequiresProxyAuth(t *testing.T) {
	var hits int64
	backend := e2eBackend(&hits)
	defer backend.Close()
	p, _ := newE2EProxy(t, backend.URL, func(cfg *config.Config) {
		cfg.Proxy.Auth.Enabled = true
		cfg.Proxy.Auth.APIKey = "shared-key"
	})
	ws := &recordingWS{}
	p.SetWebSocketHandler(ws)

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, upgradeRequest(""))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("upgrade with no key: status %d, want 401", rec.Code)
	}
	rec = httptest.NewRecorder()
	p.ServeHTTP(rec, upgradeRequest("wrong"))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("upgrade with a wrong key: status %d, want 401", rec.Code)
	}
	if n := atomic.LoadInt64(&ws.calls); n != 0 {
		t.Errorf("the WebSocket handler was reached %d times by unauthenticated upgrades", n)
	}

	rec = httptest.NewRecorder()
	p.ServeHTTP(rec, upgradeRequest("shared-key"))
	if rec.Code != http.StatusSwitchingProtocols || atomic.LoadInt64(&ws.calls) != 1 {
		t.Errorf("upgrade with the right key: status %d, handler calls %d, want 101 and 1", rec.Code, atomic.LoadInt64(&ws.calls))
	}
}

// With auth off nothing changes: upgrades go straight to the handler.
func TestWebSocketUpgradeWithAuthOffIsUnchanged(t *testing.T) {
	var hits int64
	backend := e2eBackend(&hits)
	defer backend.Close()
	p, _ := newE2EProxy(t, backend.URL, nil)
	ws := &recordingWS{}
	p.SetWebSocketHandler(ws)

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, upgradeRequest(""))
	if rec.Code != http.StatusSwitchingProtocols || atomic.LoadInt64(&ws.calls) != 1 {
		t.Errorf("auth off: status %d, handler calls %d, want 101 and 1", rec.Code, atomic.LoadInt64(&ws.calls))
	}
}

// A trusted-network peer still skips the key check on an upgrade, as it does
// for any other request.
func TestWebSocketUpgradeFromATrustedNetworkSkipsTheKey(t *testing.T) {
	var hits int64
	backend := e2eBackend(&hits)
	defer backend.Close()
	p, _ := newE2EProxy(t, backend.URL, func(cfg *config.Config) {
		cfg.Proxy.Auth.Enabled = true
		cfg.Proxy.Auth.APIKey = "shared-key"
		cfg.Proxy.Auth.TrustedNetworks = []string{"192.0.2.0/24"} // httptest's peer
	})
	ws := &recordingWS{}
	p.SetWebSocketHandler(ws)

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, upgradeRequest(""))
	if rec.Code != http.StatusSwitchingProtocols || atomic.LoadInt64(&ws.calls) != 1 {
		t.Errorf("trusted peer: status %d, handler calls %d, want 101 and 1", rec.Code, atomic.LoadInt64(&ws.calls))
	}
}
