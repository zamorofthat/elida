package panel

import (
	"testing"
	"time"

	"elida/internal/fingerprint"
	"elida/internal/session"
)

type stubMember struct {
	name string
	op   MemberOpinion
}

func (s stubMember) Name() string                         { return s.name }
func (s stubMember) Version() string                      { return "test" }
func (s stubMember) Assess(SessionFeatures) MemberOpinion { return s.op }

func TestPanel_WeightedMaxAcrossLiveMembers(t *testing.T) {
	p := NewPanel()
	p.Seat(stubMember{"a", MemberOpinion{Member: "a", Anomaly: 0.2}}, false, 1.0)
	p.Seat(stubMember{"b", MemberOpinion{Member: "b", Anomaly: 0.6}}, false, 0.5) // 0.6*0.5=0.30
	v := p.Assess(SessionFeatures{})
	if v.RiskScore != 0.3 {
		t.Fatalf("RiskScore = %v, want 0.3 (max of 0.2 and 0.30)", v.RiskScore)
	}
	if len(v.Members) != 2 {
		t.Fatalf("expected 2 member opinions, got %d", len(v.Members))
	}
}

func TestPanel_VetoFloorsRiskToOne(t *testing.T) {
	p := NewPanel()
	p.Seat(stubMember{"a", MemberOpinion{Member: "a", Anomaly: 0.1}}, false, 1.0)
	p.Seat(stubMember{"veto", MemberOpinion{Member: "veto", Anomaly: 0.0, Veto: true}}, false, 1.0)
	if v := p.Assess(SessionFeatures{}); v.RiskScore != 1.0 {
		t.Fatalf("RiskScore = %v, want 1.0 on veto", v.RiskScore)
	}
}

func TestPanel_ShadowMemberExcludedFromRiskButReported(t *testing.T) {
	p := NewPanel()
	p.Seat(stubMember{"live", MemberOpinion{Member: "live", Anomaly: 0.2}}, false, 1.0)
	p.Seat(stubMember{"shadow", MemberOpinion{Member: "shadow", Anomaly: 0.9}}, true, 1.0)
	v := p.Assess(SessionFeatures{})
	if v.RiskScore != 0.2 {
		t.Fatalf("RiskScore = %v, want 0.2 (shadow 0.9 excluded)", v.RiskScore)
	}
	if len(v.Members) != 2 {
		t.Fatalf("expected both opinions reported, got %d", len(v.Members))
	}
}

func TestPanel_ClassFromHighestConfidenceLiveMember(t *testing.T) {
	p := NewPanel()
	p.Seat(stubMember{"a", MemberOpinion{Member: "a", Class: "benign", Confidence: 0.3}}, false, 1.0)
	p.Seat(stubMember{"b", MemberOpinion{Member: "b", Class: "attack", Confidence: 0.8}}, false, 1.0)
	if v := p.Assess(SessionFeatures{}); v.Class != "attack" {
		t.Fatalf("Class = %q, want attack (highest confidence)", v.Class)
	}
}

func TestPanel_ShadowToolChainDoesNotAffectRisk(t *testing.T) {
	p := NewPanel()
	// live M3-lite-like member at notable → RiskScore driven by it
	p.Seat(stubMember{"m3-lite", MemberOpinion{Member: "m3-lite", Anomaly: 1.0}}, false, 1.0)
	// shadow tool-chain member screaming anomaly
	p.Seat(NewToolChainMember(loadGolden(t)), true, 0)
	v := p.Assess(traj("read", "deploy")) // OOV → high tool-chain anomaly
	if v.RiskScore != 1.0 {
		t.Fatalf("RiskScore = %v, want 1.0 (shadow tool-chain excluded)", v.RiskScore)
	}
	found := false
	for _, op := range v.Members {
		if op.Member == "tool-chain" {
			found = true
		}
	}
	if !found {
		t.Fatal("shadow tool-chain opinion must still be reported in Members")
	}
}

func TestBuildFeatures_PopulatesAggregateAndClass(t *testing.T) {
	snap := session.NewSession("sess-x", "backend-a", "127.0.0.1")
	f := BuildFeatures(snap)
	if len(f.Aggregate) != fingerprint.NumFeatures {
		t.Fatalf("Aggregate len = %d, want %d", len(f.Aggregate), fingerprint.NumFeatures)
	}
	wantClass := fingerprint.SessionClass(snap)
	if f.Class != wantClass {
		t.Fatalf("Class = %q, want %q", f.Class, wantClass)
	}
	if f.Trajectory != nil {
		t.Fatalf("Trajectory = %v, want nil (Phase 1)", f.Trajectory)
	}
	got, ok := f.snapshot()
	if !ok || got != snap {
		t.Fatalf("snapshot() did not return the source session")
	}
}

func TestBuildFeatures_PopulatesTrajectoryFromToolHistory(t *testing.T) {
	snap := session.NewSession("sess-c", "backend-a", "127.0.0.1")
	base := time.Now()
	snap.ToolCallHistory = []session.ToolCallRecord{
		{ToolName: "read", Timestamp: base},
		{ToolName: "edit", Timestamp: base.Add(150 * time.Millisecond)},
	}
	f := BuildFeatures(snap)
	if len(f.Trajectory) != 2 {
		t.Fatalf("Trajectory len = %d, want 2", len(f.Trajectory))
	}
	if f.Trajectory[0].Tool != "read" || f.Trajectory[1].Tool != "edit" {
		t.Fatalf("tools = %q,%q", f.Trajectory[0].Tool, f.Trajectory[1].Tool)
	}
	if f.Trajectory[0].DtMs != 0 || f.Trajectory[1].DtMs != 150 {
		t.Errorf("DtMs = %d,%d, want 0,150", f.Trajectory[0].DtMs, f.Trajectory[1].DtMs)
	}
}
