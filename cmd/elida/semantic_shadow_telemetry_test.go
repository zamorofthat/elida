package main

import (
	"reflect"
	"testing"
	"time"

	"elida/internal/config"
	"elida/internal/session"
	"elida/internal/storage"
)

func storedSemanticShadow(i int) storage.SemanticShadow {
	return storage.SemanticShadow{
		Timestamp: time.Date(2026, 11, 7, 9, 0, i, 0, time.UTC), DecisionID: "dec_abc", Signal: "injection",
		Probability: 0.91, AuxProbability: 0.02, Vetoed: false, SourceRole: "tool", MessageIndex: 4 + i,
		Transform: "base64_decode", TransformDepth: 1, WindowStartByte: 10, WindowEndByte: 210,
		Model: "minilm-multihead", ModelVersion: "v5-fp32", ModelChecksum: "sha256:0f3a9c", ThresholdSet: "v1",
		ExecutionMode: "async", ProtectionScope: "later_requests", CoverageComplete: true, LatencyMs: 37,
	}
}

func TestTelemetryRecordFrom_SemanticShadowIsConvertedFieldByField(t *testing.T) {
	// Twenty stored fields: if one is added, this test must be revisited.
	if n := reflect.TypeOf(storage.SemanticShadow{}).NumField(); n != 20 {
		t.Fatalf("storage.SemanticShadow has %d fields, want 20; decide whether the new one is exportable", n)
	}
	record, snap := newShadowTelemetryFixture(t)
	record.SemanticShadow = []storage.SemanticShadow{storedSemanticShadow(0), storedSemanticShadow(1)}

	tr := telemetryRecordFrom(record, snap, time.Now(), nil)
	if len(tr.SemanticShadow) != 2 {
		t.Fatalf("telemetry shadow list = %d, want 2 (order preserved)", len(tr.SemanticShadow))
	}
	got, want := tr.SemanticShadow[1], storedSemanticShadow(1)
	if !got.Timestamp.Equal(want.Timestamp) || got.SourceRole != want.SourceRole || got.MessageIndex != want.MessageIndex || got.LatencyMs != want.LatencyMs {
		t.Fatalf("envelope fields: %+v", got)
	}
	m := got.Metadata
	if m.Signal != want.Signal || m.Probability != want.Probability || m.AuxProbability != want.AuxProbability || m.Vetoed != want.Vetoed ||
		m.Model != want.Model || m.ModelVersion != want.ModelVersion || m.ModelChecksum != want.ModelChecksum || m.ThresholdSet != want.ThresholdSet ||
		m.DecisionID != want.DecisionID || m.Transform != want.Transform || m.TransformDepth != want.TransformDepth ||
		m.WindowStartByte != want.WindowStartByte || m.WindowEndByte != want.WindowEndByte || m.CoverageComplete != want.CoverageComplete ||
		m.ExecutionMode != want.ExecutionMode || m.ProtectionScope != want.ProtectionScope {
		t.Fatalf("metadata fields: %+v", m)
	}
}

func TestTelemetryRecord_SemanticShadowSwitch(t *testing.T) {
	record, snap := newShadowTelemetryFixture(t)
	record.SemanticShadow = []storage.SemanticShadow{storedSemanticShadow(0)}

	off := &app{cfg: config.DefaultConfig()}
	if got := off.telemetryRecord(record, snap, time.Now(), nil); len(got.SemanticShadow) != 0 {
		t.Fatalf("switch off: %d shadow entries reached the export record, want 0", len(got.SemanticShadow))
	}
	on := &app{cfg: config.DefaultConfig()}
	on.cfg.Telemetry.SemanticShadowLogs = true
	if got := on.telemetryRecord(record, snap, time.Now(), nil); len(got.SemanticShadow) != 1 {
		t.Fatalf("switch on: %d shadow entries, want 1", len(got.SemanticShadow))
	}
}

// newShadowTelemetryFixture returns a stored record and a session snapshot
// for telemetryRecordFrom. The snapshot is built as a pointer literal, so no
// lock-holding session.Session is copied.
func newShadowTelemetryFixture(t *testing.T) (*storage.SessionRecord, *session.Session) {
	t.Helper()
	start := time.Now().Add(-time.Minute)
	rec := &storage.SessionRecord{ID: "sess-ss", StartTime: start}
	snap := &session.Session{ID: "sess-ss", StartTime: start}
	return rec, snap
}
