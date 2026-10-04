package panel

import (
	"math"
	"testing"
)

func loadGolden(t *testing.T) *ToolChainArtifact {
	t.Helper()
	a, err := LoadToolChainArtifact("testdata/tool-chain.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func traj(tools ...string) SessionFeatures {
	tf := make([]TurnFeature, len(tools))
	for i, name := range tools {
		tf[i] = TurnFeature{Tool: name}
	}
	return SessionFeatures{Trajectory: tf, Class: "global"}
}

func TestToolChainMember_NormalSequenceLowAnomaly(t *testing.T) {
	m := NewToolChainMember(loadGolden(t))
	// read->read: start 0.8, transition 0.6 → low perplexity → low percentile
	op := m.Assess(traj("read", "read", "read"))
	if op.Anomaly < 0 || op.Anomaly > 1 {
		t.Fatalf("Anomaly %v out of [0,1]", op.Anomaly)
	}
	if op.Anomaly > 0.5 {
		t.Errorf("expected low anomaly for a likely sequence, got %v", op.Anomaly)
	}
}

func TestToolChainMember_OOVToolIsFiniteHighAnomaly(t *testing.T) {
	m := NewToolChainMember(loadGolden(t))
	op := m.Assess(traj("read", "deploy")) // "deploy" not in states → oov_floor
	if math.IsNaN(op.Anomaly) || math.IsInf(op.Anomaly, 0) {
		t.Fatalf("Anomaly must be finite, got %v", op.Anomaly)
	}
	if op.Anomaly <= 0.5 {
		t.Errorf("expected high anomaly for an OOV tool, got %v", op.Anomaly)
	}
}

func TestToolChainMember_ShortSessionZeroAnomaly(t *testing.T) {
	m := NewToolChainMember(loadGolden(t))
	if op := m.Assess(traj("read")); op.Anomaly != 0 {
		t.Errorf("single-tool session Anomaly = %v, want 0", op.Anomaly)
	}
	if op := m.Assess(traj()); op.Anomaly != 0 {
		t.Errorf("empty session Anomaly = %v, want 0", op.Anomaly)
	}
}

func TestToolChainMember_Name(t *testing.T) {
	if NewToolChainMember(loadGolden(t)).Name() != "tool-chain" {
		t.Fatal("Name() must be tool-chain")
	}
}
