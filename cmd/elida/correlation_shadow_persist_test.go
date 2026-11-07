package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/decision/embedded"
)

// shadowStorageApp is slowBurnTraffic's wiring (shipped config, correlation
// on, decision shadow, slowBurnPipeline) with SQLite storage and the
// session-end callback, so correlation_shadow reaches the stored record.
func shadowStorageApp(t *testing.T) (*app, func(body string) int) {
	t.Helper()
	quietLogs(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","content":[{"type":"text","text":"noted"}]}`))
	}))
	t.Cleanup(backend.Close)
	cfg, err := config.Load(filepath.Join("..", "..", "configs", "elida.yaml"))
	if err != nil {
		t.Fatalf("load the shipped config: %v", err)
	}
	cfg.Correlation.Enabled = true
	for name, b := range cfg.Backends {
		b.URL = backend.URL
		cfg.Backends[name] = b
	}
	cfg.Decision.Enabled = true
	cfg.Decision.Mode = config.DecisionModeShadow
	cfg.Decision.ModelPath = goodModelPath
	cfg.Decision.InlineTimeout = 5 * time.Second
	cfg.Storage.Enabled = true
	cfg.Storage.Path = filepath.Join(t.TempDir(), "elida.db")

	a := &app{cfg: cfg, configPath: filepath.Join(t.TempDir(), "elida.yaml")}
	a.decisionPipeline = func(context.Context, string, *embedded.Manifest) (embedded.Pipeline, error) {
		return slowBurnPipeline{}, nil
	}
	pinInline(a)
	a.initSessionStore()
	a.initSQLiteStorage()
	t.Cleanup(func() { _ = a.sqliteStore.Close() })
	if err := a.setupPolicyEngine(); err != nil {
		t.Fatalf("setupPolicyEngine: %v", err)
	}
	if err := a.setupDecision(context.Background()); err != nil {
		t.Fatalf("setupDecision: %v", err)
	}
	t.Cleanup(func() { a.shutdownDecision(context.Background()) })
	a.initSessionEndCallback()
	a.initProxy()
	a.initControlAPI()
	send := func(body string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Session-ID", "sess-cs-e2e")
		w := httptest.NewRecorder()
		a.proxyHandler.ServeHTTP(w, req)
		return w.Code
	}
	return a, send
}

// D2: the shadow match recorded on the live session reaches the stored
// record at session end, and GET /control/history/{id} serves it.
func TestCorrelationShadowPersist_SessionEndStoresShadowMatches(t *testing.T) {
	a, send := shadowStorageApp(t)
	for _, body := range slowBurnBodies() {
		if code := send(body); code != http.StatusOK {
			t.Fatalf("status = %d", code)
		}
	}
	a.manager.DrainActiveSessions()
	rec, err := a.sqliteStore.GetSession("sess-cs-e2e")
	if err != nil || rec == nil {
		t.Fatalf("GetSession: %v %v", rec, err)
	}
	if len(rec.CorrelationShadow) != 1 || rec.CorrelationShadow[0].Rule != "slow_burn_injection" || len(rec.CorrelationShadow[0].EventIDs) != 3 {
		t.Fatalf("stored correlation_shadow = %+v, want one slow_burn_injection match over three decisions", rec.CorrelationShadow)
	}
	if len(rec.Violations) != 0 {
		t.Fatalf("shadow mode must store no violations: %+v", rec.Violations)
	}
	w := httptest.NewRecorder()
	a.controlHandler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/control/history/sess-cs-e2e", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"correlation_shadow"`) || !strings.Contains(w.Body.String(), rec.CorrelationShadow[0].MatchID) {
		t.Fatalf("history detail: %d %s", w.Code, w.Body.String())
	}
	wl := httptest.NewRecorder()
	a.controlHandler.ServeHTTP(wl, httptest.NewRequest(http.MethodGet, "/control/history", nil))
	if strings.Contains(wl.Body.String(), `"correlation_shadow"`) {
		t.Fatal("the history list must not carry correlation_shadow")
	}
	if strings.Contains(w.Body.String(), slowBurnMarker) || strings.Contains(fmt.Sprint(rec.CorrelationShadow), slowBurnMarker) {
		t.Fatal("request content reached the stored shadow record")
	}
}
