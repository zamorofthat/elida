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

// cloneArtifact deep-copies the artifact so tests can mutate classes/matrices
// without touching the loaded golden value.
func cloneArtifact(a *ToolChainArtifact) *ToolChainArtifact {
	out := *a
	out.Classes = make(map[string]ClassChain, len(a.Classes))
	for k, c := range a.Classes {
		cc := c
		cc.States = append([]string(nil), c.States...)
		cc.Start = append([]float64(nil), c.Start...)
		cc.Matrix = make([][]float64, len(c.Matrix))
		for i, row := range c.Matrix {
			cc.Matrix[i] = append([]float64(nil), row...)
		}
		cc.Calibration.Percentiles = append([]float64(nil), c.Calibration.Percentiles...)
		cc.Calibration.Perplexity = append([]float64(nil), c.Calibration.Perplexity...)
		out.Classes[k] = cc
	}
	return &out
}

func perplexityOf(t *testing.T, op MemberOpinion) float64 {
	t.Helper()
	pp, ok := op.Detail["perplexity"].(float64)
	if !ok {
		t.Fatalf("Detail[perplexity] missing or not float64: %#v", op.Detail)
	}
	if math.IsNaN(pp) || math.IsInf(pp, 0) {
		t.Fatalf("perplexity must be finite, got %v", pp)
	}
	return pp
}

func TestToolChain_FromToolUnseenUsesOOVFloor(t *testing.T) {
	a := loadGolden(t)
	floor := a.Classes["global"].OOVFloor
	m := NewToolChainMember(a)

	op := m.Assess(traj("read", "never-seen", "read"))
	if op.Anomaly < 0 || op.Anomaly > 1 || math.IsNaN(op.Anomaly) {
		t.Fatalf("Anomaly %v out of [0,1]", op.Anomaly)
	}
	pp := perplexityOf(t, op)

	// read (start 0.8) -> never-seen (to-OOV: floor) -> read (from-OOV: floor)
	want := math.Exp(-(math.Log(0.8) + 2*math.Log(floor)) / 3)
	if math.Abs(pp-want)/want > 1e-9 {
		t.Errorf("perplexity = %v, want %v (both transitions touching the unseen tool use OOVFloor)", pp, want)
	}

	// Control: swap the unseen tool for a high-probability known tool.
	control := perplexityOf(t, m.Assess(traj("read", "read", "read")))
	if !(pp > control) {
		t.Errorf("unseen-tool perplexity %v must exceed known-tool control %v", pp, control)
	}

	// The from-unseen transition specifically: a trajectory that ends on the
	// unseen tool has one fewer floor transition, so must score strictly lower.
	toOnly := perplexityOf(t, m.Assess(traj("read", "read", "never-seen")))
	if !(pp > toOnly) {
		t.Errorf("from+to unseen perplexity %v must exceed to-only %v", pp, toOnly)
	}
}

func TestToolChain_NoChainFallsBackToZero(t *testing.T) {
	golden := loadGolden(t)
	sess := traj("read", "edit", "bash")
	sess.Class = "analyst"

	// Class present only as the "global" fallback.
	op := NewToolChainMember(golden).Assess(sess)
	if got := op.Detail["class_used"]; got != "global" {
		t.Errorf("class_used = %v, want global fallback", got)
	}
	if _, has := op.Detail["reason"]; has {
		t.Errorf("fallback to global must not report a reason, got %v", op.Detail["reason"])
	}

	// A specific class present is preferred over global.
	withClass := cloneArtifact(golden)
	withClass.Classes["analyst"] = withClass.Classes["global"]
	if got := NewToolChainMember(withClass).Assess(sess).Detail["class_used"]; got != "analyst" {
		t.Errorf("class_used = %v, want analyst", got)
	}

	// Neither the session class nor "global" exists.
	none := cloneArtifact(golden)
	delete(none.Classes, "analyst")
	delete(none.Classes, "global")
	op = NewToolChainMember(none).Assess(sess)
	if op.Anomaly != 0 {
		t.Errorf("no-chain Anomaly = %v, want 0", op.Anomaly)
	}
	if got := op.Detail["reason"]; got != "no-chain" {
		t.Errorf("reason = %v, want no-chain", got)
	}
}

func TestToolChain_ZeroProbabilityCellUsesFloor(t *testing.T) {
	const floor = 0.0001
	build := func(readToRead float64) *ToolChainArtifact {
		return &ToolChainArtifact{
			SchemaVersion: "1",
			MemberType:    "tool-chain",
			GeneratedBy:   "test",
			Classes: map[string]ClassChain{"global": {
				States: []string{"read", "edit", "bash"},
				Start:  []float64{0.8, 0.1, 0.1},
				Matrix: [][]float64{
					{readToRead, 0.3, 0.1},
					{0.4, 0.4, 0.2},
					{0.5, 0.3, 0.2},
				},
				OOVFloor:    floor,
				Calibration: Calibration{Percentiles: []float64{1, 50, 99}, Perplexity: []float64{1.2, 2.0, 6.0}},
			}},
		}
	}

	zero := NewToolChainMember(build(0)).Assess(traj("read", "read", "read"))
	if zero.Anomaly < 0 || zero.Anomaly > 1 || math.IsNaN(zero.Anomaly) {
		t.Fatalf("Anomaly %v out of [0,1]", zero.Anomaly)
	}
	pp := perplexityOf(t, zero)

	// Two read->read transitions each hit the zero cell and use the floor.
	want := math.Exp(-(math.Log(0.8) + 2*math.Log(floor)) / 3)
	if math.Abs(pp-want)/want > 1e-9 {
		t.Errorf("perplexity = %v, want %v (zero cell must use OOVFloor)", pp, want)
	}

	// Control: the same cell at its normal 0.6 probability scores lower.
	control := perplexityOf(t, NewToolChainMember(build(0.6)).Assess(traj("read", "read", "read")))
	if !(pp > control) {
		t.Errorf("zero-cell perplexity %v must exceed control %v", pp, control)
	}
}
