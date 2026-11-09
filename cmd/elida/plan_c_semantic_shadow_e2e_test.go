package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/session"
)

const semanticShadowLogBody = "shadow semantic decision:"

// With the switch on, a shadow-mode run exports one log per stored shadow
// decision through the real exporter, none of it content, and moves no risk.
func TestPlanC_SemanticShadowReachesLogsWithoutContent(t *testing.T) {
	r := planCAppWith(t, config.DecisionModeShadow, func(c *config.Config) { c.Telemetry.SemanticShadowLogs = true })
	r.a.manager.DrainActiveSessions()
	rec, err := r.a.sqliteStore.GetSession(r.id)
	if err != nil || rec == nil || len(rec.SemanticShadow) == 0 {
		t.Fatalf("stored record must carry semantic_shadow: %+v %v", rec, err)
	}
	want := len(rec.SemanticShadow)

	out := r.otel(t, fmt.Sprint(want))
	if planCMarker(t, out) != fmt.Sprint(want) {
		t.Fatalf("the child stored %s shadow decisions, want %d", planCMarker(t, out), want)
	}
	export := out[:strings.LastIndex(out, "planc-marker=")]
	if got := strings.Count(export, semanticShadowLogBody); got != want {
		t.Fatalf("OTEL semantic shadow logs = %d, want %d: %s", got, want, planCTail(export))
	}
	if !strings.Contains(export, "elida.semantic.shadow.count") {
		t.Fatalf("the session span must carry elida.semantic.shadow.count: %s", planCTail(export))
	}
	assertNoSentinel(t, "OTEL export", out)
	if evs := planCNozzleEvents(r.nozzle); len(evs) != 0 {
		t.Fatalf("a shadow decision must produce no OCSF finding: %v", evs)
	}
	if score, _, _ := r.a.policyEngine.GetSessionRiskScore(r.id); score != 0 {
		t.Fatalf("shadow export moved risk: %v", score)
	}
	assertNoSentinel(t, "logs", r.logs.String())
}

// Default config (switch off): the same run exports nothing new. This is the
// default-config log-diff gate: no semantic shadow log, no span count.
func TestPlanC_SemanticShadowSwitchOffExportsNothing(t *testing.T) {
	r := planCApp(t, config.DecisionModeShadow)
	r.a.manager.DrainActiveSessions()
	rec, err := r.a.sqliteStore.GetSession(r.id)
	if err != nil || rec == nil || len(rec.SemanticShadow) == 0 {
		t.Fatalf("fixture: the run must record shadow decisions: %+v %v", rec, err)
	}
	out := r.otel(t, "off")
	for _, s := range []string{semanticShadowLogBody, "elida.semantic.shadow", "elida.semantic.shadow.count"} {
		if strings.Contains(out, s) {
			t.Fatalf("switch off, but the export contains %q: %s", s, planCTail(out))
		}
	}
	assertNoSentinel(t, "OTEL export", out)
}

// A session holding 50 shadow decisions exports 50 logs; one that saw 51
// exports 50 (the session cap is inherited, the exporter adds none).
func TestPlanC_SemanticShadowLogsBoundedAtTheSessionCap(t *testing.T) {
	for _, n := range []int{session.MaxSemanticShadow, session.MaxSemanticShadow + 1} {
		t.Run(fmt.Sprintf("recorded=%d", n), func(t *testing.T) {
			quietLogs(t)
			cfg := decisionTestConfig(t)
			cfg.Decision.Enabled = false
			cfg.Storage.Enabled = true
			cfg.Storage.Path = filepath.Join(t.TempDir(), "elida.db")
			cfg.Telemetry.SemanticShadowLogs = true
			a := newDecisionApp(t, cfg, nil)
			a.initSessionStore()
			a.initSQLiteStorage()
			t.Cleanup(func() { _ = a.sqliteStore.Close() })
			tp, otelOut := planCTelemetry(t)
			a.tp = tp
			a.initSessionEndCallback()

			id := fmt.Sprintf("sess-sem-shadow-cap-%d", n)
			sess := a.manager.GetOrCreate(id, "default", "127.0.0.1:1")
			base := time.Now().Add(-time.Minute)
			for i := 0; i < n; i++ {
				sess.RecordSemanticShadow(session.SemanticShadow{
					Timestamp: base.Add(time.Duration(i) * time.Millisecond), DecisionID: fmt.Sprintf("dec-cap-%03d", i),
					Signal: "injection", Probability: 0.9, SourceRole: "tool", MessageIndex: i,
					Model: "m", ModelVersion: "v", ThresholdSet: "v1", ExecutionMode: "async",
					ProtectionScope: "later_requests", CoverageComplete: true, LatencyMs: 5,
				})
			}
			a.manager.DrainActiveSessions()

			want := min(n, session.MaxSemanticShadow)
			rec, err := a.sqliteStore.GetSession(id)
			if err != nil || rec == nil || len(rec.SemanticShadow) != want {
				t.Fatalf("stored semantic_shadow: %v %v", rec, err)
			}
			out := otelOut(t, fmt.Sprint(len(rec.SemanticShadow)))
			if planCMarker(t, out) != fmt.Sprint(want) {
				t.Fatalf("the child stored %s shadow decisions, want %d", planCMarker(t, out), want)
			}
			if got := strings.Count(out, semanticShadowLogBody); got != want {
				t.Fatalf("OTEL semantic shadow logs = %d, want %d", got, want)
			}
			if n > session.MaxSemanticShadow && strings.Contains(out, "dec-cap-000") {
				t.Fatal("the oldest decision beyond the cap must not be exported")
			}
		})
	}
}
