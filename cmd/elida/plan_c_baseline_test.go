package main

import (
	"net/http"
	"strings"
	"testing"
)

// KEEP. With decision and correlation disabled (the defaults), a flagged
// session's end emits no correlation or shadow log line. Plan C adds
// persistence and export of correlation evidence; a default deployment
// must not notice.
func TestBaselineC_DefaultConfigSessionEndLogsNothingNew(t *testing.T) {
	a, send := historyApp(t)
	if a.cfg.Decision.Enabled || a.cfg.Correlation.Enabled {
		t.Fatal("fixture: decision and correlation must be disabled")
	}
	logs := captureLogs(t)
	if code := send("sess-c-default", "please zzprobezz now"); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	a.manager.DrainActiveSessions()
	for _, line := range strings.Split(logs.String(), "\n") {
		l := strings.ToLower(line)
		if strings.Contains(l, "correlation") || strings.Contains(l, "shadow") {
			t.Fatalf("default config logged a correlation/shadow line: %s", line)
		}
	}
	if got := storedRules(t, a, "sess-c-default"); len(got) != 1 || got[0] != "probe_rule" {
		t.Fatalf("fixture: stored %v", got)
	}
}
