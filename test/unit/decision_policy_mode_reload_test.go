package unit

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/control"
	"elida/internal/policy"
	"elida/internal/session"
)

// The policy.mode cap on decision.mode is computed at startup. A runtime
// policy.mode change that makes it stale is logged, so an operator is not
// left believing semantic enforcement followed the dashboard change.
func TestDecisionPolicyModeReload_WarnsWhenTheCapIsStale(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)
	engine := policy.NewEngine(policy.Config{Enabled: true, Mode: "enforce"})
	settingsStore, err := config.NewSettingsStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handler := control.New(store, manager, control.WithPolicy(engine))
	handler.SetSettingsStore(settingsStore)
	handler.SetDecisionProvider(stubDecisionProvider{status: control.DecisionStatus{
		Enabled: true, Mode: "enforce", EffectiveMode: "enforce", Capability: "inline",
	}})

	put := func(mode string) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"policy": map[string]any{"enabled": true, "mode": mode}})
		req := httptest.NewRequest(http.MethodPut, "/control/settings", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("PUT settings: %d %s", w.Code, w.Body.String())
		}
	}

	put("enforce")
	if strings.Contains(buf.String(), "effective mode computed at startup") {
		t.Fatalf("no warning expected while the cap still matches: %s", buf.String())
	}
	put("audit")
	if !strings.Contains(buf.String(), "effective mode computed at startup") {
		t.Fatalf("a stale cap must be logged: %q", buf.String())
	}
}
