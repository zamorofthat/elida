package panel

import (
	"context"
	"log/slog"
	"math"
	"sync"
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

func TestPanel_AnomalyClampedToUnitInterval(t *testing.T) {
	cases := []struct {
		name string
		in   float64
		want float64
	}{{"above", 4.0, 1.0}, {"below", -0.5, 0}, {"nan", math.NaN(), 0}, {"inside", 0.4, 0.4}}
	for _, c := range cases {
		p := NewPanel()
		p.Seat(stubMember{"m", MemberOpinion{Member: "m", Anomaly: c.in}}, false, 1.0)
		v := p.Assess(SessionFeatures{})
		if v.RiskScore != c.want || v.Members[0].Anomaly != c.want {
			t.Fatalf("%s: RiskScore=%v member Anomaly=%v, want %v", c.name, v.RiskScore, v.Members[0].Anomaly, c.want)
		}
	}
}

type countHandler struct {
	mu    *sync.Mutex
	warns *int
}

func (h countHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h countHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Level == slog.LevelWarn {
		h.mu.Lock()
		*h.warns++
		h.mu.Unlock()
	}
	return nil
}
func (h countHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h countHandler) WithGroup(string) slog.Handler      { return h }

func TestPanel_OutOfRangeAnomalyWarnsOncePerMember(t *testing.T) {
	var mu sync.Mutex
	warns := 0
	old := slog.Default()
	slog.SetDefault(slog.New(countHandler{&mu, &warns}))
	t.Cleanup(func() { slog.SetDefault(old) })

	p := NewPanel()
	p.Seat(stubMember{"bad", MemberOpinion{Member: "bad", Anomaly: 9}}, false, 1.0)
	for i := 0; i < 3; i++ {
		p.Assess(SessionFeatures{})
	}
	mu.Lock()
	defer mu.Unlock()
	if warns != 1 {
		t.Fatalf("warnings = %d, want exactly 1 across 3 assessments", warns)
	}
}

func TestPanel_VetoIsOrderIndependentFloor(t *testing.T) {
	hi := stubMember{"hi", MemberOpinion{Member: "hi", Anomaly: 1.0}}
	vt := stubMember{"vt", MemberOpinion{Member: "vt", Veto: true}}
	for _, order := range [][]Member{{hi, vt}, {vt, hi}} {
		p := NewPanel()
		for _, m := range order {
			p.Seat(m, false, 1.0)
		}
		v := p.Assess(SessionFeatures{})
		if v.RiskScore != 1.0 || v.Driver != "veto:vt" {
			t.Fatalf("order %v: RiskScore=%v Driver=%q, want 1.0 / veto:vt", order, v.RiskScore, v.Driver)
		}
	}
}

func TestPanel_DriverNamesTheMaxMember(t *testing.T) {
	p := NewPanel()
	p.Seat(stubMember{"a", MemberOpinion{Member: "a", Anomaly: 0.2}}, false, 1.0)
	p.Seat(stubMember{"b", MemberOpinion{Member: "b", Anomaly: 0.6}}, false, 0.5) // 0.30 wins
	p.Seat(stubMember{"s", MemberOpinion{Member: "s", Anomaly: 1.0}}, true, 1.0)  // shadow never drives
	if v := p.Assess(SessionFeatures{}); v.Driver != "b" {
		t.Fatalf("Driver = %q, want b", v.Driver)
	}
	q := NewPanel()
	q.Seat(stubMember{"z", MemberOpinion{Member: "z"}}, false, 1.0)
	if v := q.Assess(SessionFeatures{}); v.Driver != "" || v.RiskScore != 0 {
		t.Fatalf("zero case: Driver=%q RiskScore=%v, want empty/0", v.Driver, v.RiskScore)
	}
}

func TestPanel_RiskScoreNeverExceedsOne(t *testing.T) {
	p := NewPanel()
	p.Seat(stubMember{"a", MemberOpinion{Member: "a", Anomaly: 1.0}}, false, 1.0)
	p.Seat(stubMember{"b", MemberOpinion{Member: "b", Anomaly: 1.0}}, false, 1.0)
	if v := p.Assess(SessionFeatures{}); v.RiskScore != 1.0 {
		t.Fatalf("RiskScore = %v, want 1.0", v.RiskScore)
	}
}

func TestPanel_SeatClampsWeightAndOpinionsCarrySeatPosture(t *testing.T) {
	p := NewPanel()
	p.Seat(stubMember{"hi", MemberOpinion{Member: "hi", Anomaly: 1.0}}, false, 3.0)
	p.Seat(stubMember{"lo", MemberOpinion{Member: "lo", Anomaly: 1.0}}, true, -1.0)
	ms := p.Members()
	if ms[0].Weight != 1.0 || ms[1].Weight != 0 {
		t.Fatalf("clamped weights = %v, %v; want 1, 0", ms[0].Weight, ms[1].Weight)
	}
	v := p.Assess(SessionFeatures{})
	if v.Members[0].Shadow || v.Members[0].Weight != 1.0 || !v.Members[1].Shadow || v.Members[1].Weight != 0 {
		t.Fatalf("opinions lost seat posture: %+v", v.Members)
	}
}

// seqMember emits a fixed sequence of opinions, one per Assess call (looping).
type seqMember struct {
	name string
	mu   sync.Mutex
	ops  []MemberOpinion
	i    int
}

func (s *seqMember) Name() string    { return s.name }
func (s *seqMember) Version() string { return "test" }
func (s *seqMember) Assess(SessionFeatures) MemberOpinion {
	s.mu.Lock()
	defer s.mu.Unlock()
	op := s.ops[s.i%len(s.ops)]
	s.i++
	op.Member = s.name
	return op
}

func TestPanel_RollingStatsMeanP95AndVetoCount(t *testing.T) {
	p := NewPanel()
	p.Seat(&seqMember{name: "live", ops: []MemberOpinion{{Anomaly: 0.2}, {Anomaly: 0.4}, {Anomaly: 1.0, Veto: true}}}, false, 1)
	p.Seat(&seqMember{name: "shadowy", ops: []MemberOpinion{{Anomaly: 0.5, Veto: false}}}, true, 1)
	for i := 0; i < 3; i++ {
		p.Assess(SessionFeatures{})
	}
	ms := p.Members()
	live := ms[0].Stats
	if live.SessionsScored != 3 {
		t.Errorf("SessionsScored = %d, want 3", live.SessionsScored)
	}
	if math.Abs(live.MeanAnomaly-0.5333333) > 1e-4 {
		t.Errorf("MeanAnomaly = %v, want ~0.5333", live.MeanAnomaly)
	}
	if live.P95Anomaly != 1.0 {
		t.Errorf("P95Anomaly = %v, want 1.0", live.P95Anomaly)
	}
	if live.VetoCount != 1 {
		t.Errorf("VetoCount = %d, want 1", live.VetoCount)
	}
	sh := ms[1].Stats
	if sh.SessionsScored != 3 || math.Abs(sh.MeanAnomaly-0.5) > 1e-9 || sh.P95Anomaly != 0.5 || sh.VetoCount != 0 {
		t.Errorf("shadow seat stats = %+v, want tracked (3, 0.5, 0.5, 0)", sh)
	}
}

func TestPanel_StatsUseClampedAnomaly(t *testing.T) {
	p := NewPanel()
	p.Seat(&seqMember{name: "wild", ops: []MemberOpinion{{Anomaly: 7}, {Anomaly: math.NaN()}}}, false, 1)
	p.Assess(SessionFeatures{})
	p.Assess(SessionFeatures{})
	st := p.Members()[0].Stats
	if math.Abs(st.MeanAnomaly-0.5) > 1e-9 || st.P95Anomaly != 1.0 {
		t.Errorf("stats = %+v, want mean 0.5 p95 1.0 from clamped values", st)
	}
}

func TestPanel_StatsRingIsBounded(t *testing.T) {
	p := NewPanel()
	// First 4000 assessments emit 1.0, the last 1000 emit 0.0: the mean over
	// the ring must reflect only the last 1000, while the count is since boot.
	m := &seqMember{name: "m", ops: make([]MemberOpinion, 5000)}
	for i := 0; i < 4000; i++ {
		m.ops[i].Anomaly = 1.0
	}
	p.Seat(m, false, 1)
	for i := 0; i < 5000; i++ {
		p.Assess(SessionFeatures{})
	}
	st := p.Members()[0].Stats
	if st.SessionsScored != 5000 {
		t.Errorf("SessionsScored = %d, want 5000 (since boot, uncapped)", st.SessionsScored)
	}
	if st.MeanAnomaly != 0 || st.P95Anomaly != 0 {
		t.Errorf("mean/p95 = %v/%v, want 0/0 (ring holds only the last 1000)", st.MeanAnomaly, st.P95Anomaly)
	}
	p.mu.Lock()
	n := len(p.seats[0].stats.ring)
	p.mu.Unlock()
	if n > statsRingSize {
		t.Errorf("ring len = %d, want <= %d", n, statsRingSize)
	}
}

func TestPanel_StatsZeroBeforeAnyAssessment(t *testing.T) {
	p := NewPanel()
	p.Seat(&seqMember{name: "m", ops: []MemberOpinion{{}}}, false, 1)
	if st := p.Members()[0].Stats; st != (MemberStats{}) {
		t.Errorf("stats = %+v, want zero value", st)
	}
}

func TestPanel_ConcurrentAssessAndMembersRace(t *testing.T) {
	p := NewPanel()
	p.Seat(&seqMember{name: "a", ops: []MemberOpinion{{Anomaly: 0.3}, {Anomaly: 0.9, Veto: true}}}, false, 1)
	p.Seat(&seqMember{name: "b", ops: []MemberOpinion{{Anomaly: 0.6}}}, true, 0.5)
	var wg sync.WaitGroup
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				p.Assess(SessionFeatures{})
				_ = p.Members()
			}
		}()
	}
	wg.Wait()
	if got := p.Members()[0].Stats.SessionsScored; got != 2500 {
		t.Errorf("SessionsScored = %d, want 2500", got)
	}
}
