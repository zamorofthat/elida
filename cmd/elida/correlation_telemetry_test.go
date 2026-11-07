package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"elida/internal/storage"
	"elida/internal/telemetry"
)

// The pure record conversion carries the match evidence and nothing else new.
func TestTelemetryRecord_CarriesCorrelationEvidenceAndNoContent(t *testing.T) {
	a, send := correlationHistoryApp(t)
	for i := 0; i < 2; i++ {
		send("sess-corr-telem", "please zzprobezz sentinel7f3a now")
	}
	sess, ok := a.manager.Get("sess-corr-telem")
	if !ok {
		t.Fatal("session missing")
	}
	snap := sess.Snapshot()
	record := storage.SessionRecord{ID: "sess-corr-telem", StartTime: snap.StartTime}
	a.enrichRecordFromPolicy(&record, "sess-corr-telem", snap.StartTime)
	tr := telemetryRecordFrom(&record, &snap, time.Now(), nil)
	var corr *telemetry.Violation
	for i := range tr.Violations {
		if tr.Violations[i].Correlation != nil {
			corr = &tr.Violations[i]
		}
	}
	if corr == nil || corr.EventID == "" || corr.Correlation.MatchID != corr.EventID || corr.Correlation.EventCount != 2 || len(corr.Correlation.EventIDs) != 2 {
		t.Fatalf("the correlation violation must carry its evidence: %+v", tr.Violations)
	}
	raw, _ := json.Marshal(tr.Violations)
	if strings.Contains(string(raw), "sentinel7f3a") {
		t.Fatalf("request content reached the telemetry violations: %s", raw)
	}
	for _, v := range tr.Violations {
		f, _ := json.Marshal(telemetry.BuildPolicyDetection("sess-corr-telem", v, tr))
		if strings.Contains(string(f), "sentinel7f3a") {
			t.Fatalf("request content reached an OCSF finding: %s", f)
		}
	}
}

func TestTelemetryRecord_CarriesCorrelationShadow(t *testing.T) {
	a, send := shadowStorageApp(t)
	for _, body := range slowBurnBodies() {
		send(body)
	}
	sess, _ := a.manager.Get("sess-cs-e2e")
	snap := sess.Snapshot()
	record := storage.SessionRecord{ID: "sess-cs-e2e", StartTime: snap.StartTime,
		CorrelationShadow: storage.CorrelationShadowFromSession(snap.CorrelationShadow)}
	tr := telemetryRecordFrom(&record, &snap, time.Now(), nil)
	if len(tr.CorrelationShadow) != 1 || tr.CorrelationShadow[0].Metadata.Rule != "slow_burn_injection" ||
		tr.CorrelationShadow[0].Metadata.EventCount != 3 || tr.CorrelationShadow[0].Severity != "warning" {
		t.Fatalf("shadow matches must reach the export record: %+v", tr.CorrelationShadow)
	}
	raw, _ := json.Marshal(tr.CorrelationShadow)
	if strings.Contains(string(raw), slowBurnMarker) {
		t.Fatalf("request content reached the exported shadow list: %s", raw)
	}
}
