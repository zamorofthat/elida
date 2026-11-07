package main

import (
	"context"
	"encoding/json"
	"testing"
)

// D4: with correlation enabled the status carries the shadow correlator's
// counters; with it disabled the key is absent.
func TestDecisionStatus_ReportsShadowCorrelationStats(t *testing.T) {
	quietLogs(t)
	for _, enabled := range []bool{true, false} {
		cfg := decisionTestConfig(t)
		cfg.Correlation.Enabled = enabled
		a := newDecisionApp(t, cfg, &gate{})
		a.initSessionStore()
		if err := a.setupDecision(context.Background()); err != nil {
			t.Fatalf("setupDecision: %v", err)
		}
		raw, _ := json.Marshal(a.DecisionStatus())
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		sc, ok := m["shadow_correlation"].(map[string]any)
		if enabled && (!ok || sc["rules"] != float64(1) || sc["sessions"] != float64(0)) {
			t.Fatalf("enabled: shadow_correlation = %v", m["shadow_correlation"])
		}
		if !enabled && ok {
			t.Fatalf("disabled: shadow_correlation must be absent: %s", raw)
		}
		a.shutdownDecision(context.Background())
	}
}
