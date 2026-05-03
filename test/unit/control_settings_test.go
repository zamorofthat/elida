package unit

import (
	"encoding/json"
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

func TestSettingsPUT_CustomRuleWithThresholdFloat(t *testing.T) {
	// Set up minimal dependencies
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)
	engine := policy.NewEngine(policy.Config{Enabled: true, Mode: "enforce"})
	settingsStore, err := config.NewSettingsStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	handler := control.New(store, manager, control.WithPolicy(engine))
	handler.SetSettingsStore(settingsStore)

	// PUT settings with a custom entropy rule using ThresholdFloat and MinSamples
	settings := map[string]interface{}{
		"policy": map[string]interface{}{
			"enabled": true,
			"custom_rules": []map[string]interface{}{
				{
					"name":            "test_entropy",
					"type":            "content_entropy",
					"target":          "request",
					"threshold_float": 5.5,
					"min_samples":     50,
					"severity":        "warning",
					"action":          "flag",
					"description":     "Test entropy rule via settings API",
				},
			},
		},
	}

	body, _ := json.Marshal(settings)
	req := httptest.NewRequest(http.MethodPut, "/control/settings", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify the engine got the rule with ThresholdFloat/MinSamples
	// by checking that high-entropy content triggers the rule
	raw := make([]byte, 300)
	for i := range raw {
		raw[i] = byte((i*7 + 13*i*i + 37) % 256)
	}

	result := engine.EvaluateRequestContent("test-session", string(raw))
	if result == nil {
		t.Fatal("expected entropy violation after settings reload")
	}
	found := false
	for _, v := range result.Violations {
		if v.RuleName == "test_entropy" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected test_entropy rule to be active after settings PUT")
	}
}
