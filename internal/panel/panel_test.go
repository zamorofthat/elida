package panel

import "testing"

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
