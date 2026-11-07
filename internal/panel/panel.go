// Package panel is a pluggable ensemble ("panel") of behavioral detectors.
// Each Member judges a session; the Panel aggregates their opinions into a
// Verdict that feeds the policy risk-ladder. The panel informs; it never enforces.
package panel

import (
	"log/slog"
	"math"
	"sort"
	"sync"

	"elida/internal/fingerprint"
	"elida/internal/session"
)

// MaxRiskPoints is the risk-ladder value a Verdict.RiskScore of 1.0 maps to
// (fingerprint.RiskSevere). Risk points = round(RiskScore * MaxRiskPoints).
const MaxRiskPoints = fingerprint.RiskSevere

// TurnFeature is one per-turn record; Trajectory is populated by
// BuildFeatures from the session's tool-call history.
type TurnFeature struct {
	Tool      string
	DtMs      int64
	TokensIn  int
	TokensOut int
}

// SessionFeatures is the feature contract handed to every member.
type SessionFeatures struct {
	Aggregate  []float64
	Trajectory []TurnFeature
	Class      string

	// snap carries the raw session snapshot for members (like M3-lite) that
	// still score against *session.Session directly rather than Aggregate.
	// Populated by a later task's SessionFeatures constructor.
	snap *session.Session
}

// snapshot returns the raw session snapshot and whether one is set.
func (f SessionFeatures) snapshot() (*session.Session, bool) { return f.snap, f.snap != nil }

// BuildFeatures projects a session snapshot into the feature contract handed
// to panel members. Trajectory is built from snap.ToolCallHistory: one
// TurnFeature per tool call record, with DtMs the elapsed milliseconds since
// the previous call (0 for the first). TokensIn/TokensOut are left 0 (spec
// §8) until a later task fills them in.
func BuildFeatures(snap *session.Session) SessionFeatures {
	fv := fingerprint.Extract(snap)
	agg := make([]float64, fingerprint.NumFeatures)
	copy(agg, fv[:])

	var traj []TurnFeature
	if len(snap.ToolCallHistory) > 0 {
		traj = make([]TurnFeature, len(snap.ToolCallHistory))
		for i, rec := range snap.ToolCallHistory {
			tf := TurnFeature{Tool: rec.ToolName}
			if i > 0 {
				tf.DtMs = rec.Timestamp.Sub(snap.ToolCallHistory[i-1].Timestamp).Milliseconds()
			}
			traj[i] = tf
		}
	}

	return SessionFeatures{
		Aggregate:  agg,
		Trajectory: traj,
		Class:      fingerprint.SessionClass(snap),
		snap:       snap,
	}
}

// MemberOpinion is one detector's judgement. A member emits an anomaly score,
// a class, or both. Veto floors the panel RiskScore to 1.0 (order-independent).
// Anomaly is clamped to [0,1] by Panel.Assess. Shadow and Weight are set by
// Panel.Assess from the member's seat, not by the member itself.
type MemberOpinion struct {
	Member     string
	Anomaly    float64
	Class      string
	Confidence float64
	Veto       bool
	Detail     map[string]any
	Shadow     bool    // set by Panel.Assess from the seat
	Weight     float64 // set by Panel.Assess from the seat
}

// Verdict is the structured output the panel produces.
type Verdict struct {
	RiskScore  float64
	Class      string
	Confidence float64
	Members    []MemberOpinion
	// Driver is the member whose weight*Anomaly set RiskScore, "veto:<member>"
	// when a veto set it, and "" when RiskScore is 0.
	Driver string
}

// Member is one detector on the panel.
type Member interface {
	Name() string
	Version() string
	Assess(SessionFeatures) MemberOpinion
}

// MemberInfo is a read-only view of a seated member (for the control API).
type MemberInfo struct {
	Name    string
	Version string
	Shadow  bool
	Weight  float64
	Stats   MemberStats
}

// MemberStats is a content-free rolling summary of one seat's output since
// boot. SessionsScored and VetoCount count every assessment since boot; the
// mean and p95 are over the last statsRingSize clamped anomalies only.
type MemberStats struct {
	SessionsScored int
	MeanAnomaly    float64
	P95Anomaly     float64
	VetoCount      int
}

// statsRingSize bounds the per-seat anomaly history kept for mean/p95.
const statsRingSize = 1000

// seatStats is the mutable per-seat accumulator, guarded by Panel.mu.
type seatStats struct {
	count int
	vetos int
	ring  []float64 // last statsRingSize clamped anomalies (circular once full)
	next  int       // next write index once the ring is full
}

func (st *seatStats) record(anomaly float64, veto bool) {
	st.count++
	if veto {
		st.vetos++
	}
	if len(st.ring) < statsRingSize {
		st.ring = append(st.ring, anomaly)
		return
	}
	st.ring[st.next] = anomaly
	st.next = (st.next + 1) % statsRingSize
}

// snapshot copies the accumulator state. Caller holds Panel.mu; the returned
// ring copy can be sorted and reduced after the lock is released.
func (st *seatStats) snapshot() statsSnapshot {
	ring := make([]float64, len(st.ring))
	copy(ring, st.ring)
	return statsSnapshot{count: st.count, vetos: st.vetos, ring: ring}
}

type statsSnapshot struct {
	count, vetos int
	ring         []float64
}

// summary computes the exported stats; it sorts its own ring copy in place
// and needs no lock.
func (sn statsSnapshot) summary() MemberStats {
	out := MemberStats{SessionsScored: sn.count, VetoCount: sn.vetos}
	n := len(sn.ring)
	if n == 0 {
		return out
	}
	sort.Float64s(sn.ring)
	var sum float64
	for _, v := range sn.ring {
		sum += v
	}
	out.MeanAnomaly = sum / float64(n)
	// nearest-rank p95: ceil(95n/100)-th smallest (1-based), integer arithmetic.
	rank := (95*n + 99) / 100
	if rank < 1 {
		rank = 1
	}
	out.P95Anomaly = sn.ring[rank-1]
	return out
}

type seat struct {
	m      Member
	shadow bool
	weight float64
	stats  seatStats // guarded by Panel.mu
}

// Panel holds seated members and aggregates their opinions. Seat weights are
// clamped to [0,1] and member anomalies to [0,1], so RiskScore stays in [0,1].
type Panel struct {
	seats  []seat
	mu     sync.Mutex // guards seats[i].stats
	warned sync.Map   // member name -> struct{}; out-of-range anomaly warned once
}

func NewPanel() *Panel { return &Panel{} }

// Seat adds a member. shadow members are assessed and reported but excluded
// from the aggregated RiskScore/Class. weight is clamped to [0,1].
func (p *Panel) Seat(m Member, shadow bool, weight float64) {
	p.seats = append(p.seats, seat{m: m, shadow: shadow, weight: clamp01(weight)})
}

func clamp01(x float64) float64 {
	if math.IsNaN(x) || x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// Assess runs every member and aggregates: weighted-max with an
// order-independent veto floor. Out-of-range anomalies are clamped to [0,1]
// (NaN becomes 0) with one warning per member name.
func (p *Panel) Assess(f SessionFeatures) Verdict {
	v := Verdict{Members: make([]MemberOpinion, 0, len(p.seats))}
	var bestClassConf float64
	var vetoBy string
	// Stats are gathered locally and flushed under one lock acquisition after
	// all members ran, so the (possibly slow) member.Assess calls never hold mu.
	type scored struct {
		anomaly float64
		veto    bool
	}
	scores := make([]scored, 0, len(p.seats))
	defer func() {
		// Only seats whose opinion was collected are recorded, so a panicking
		// member leaves no phantom zero entries for itself or later seats.
		p.mu.Lock()
		for i, sc := range scores {
			p.seats[i].stats.record(sc.anomaly, sc.veto)
		}
		p.mu.Unlock()
	}()
	for i := range p.seats {
		// Index, don't copy: a struct copy would read seat.stats unlocked.
		s := &p.seats[i]
		op := s.m.Assess(f)
		if c := clamp01(op.Anomaly); c != op.Anomaly { // NaN != NaN, so NaN is caught too
			if _, seen := p.warned.LoadOrStore(op.Member, struct{}{}); !seen {
				slog.Warn("panel member emitted out-of-range anomaly; clamped to [0,1]", "member", op.Member, "anomaly", op.Anomaly)
			}
			op.Anomaly = c
		}
		scores = append(scores, scored{anomaly: op.Anomaly, veto: op.Veto})
		op.Shadow, op.Weight = s.shadow, s.weight
		v.Members = append(v.Members, op)
		if s.shadow {
			continue
		}
		if op.Veto && vetoBy == "" {
			vetoBy = op.Member
		}
		if w := s.weight * op.Anomaly; w > v.RiskScore {
			v.RiskScore = w
			v.Driver = op.Member
		}
		if op.Class != "" && op.Confidence >= bestClassConf {
			bestClassConf = op.Confidence
			v.Class = op.Class
			v.Confidence = op.Confidence
		}
	}
	if vetoBy != "" {
		v.RiskScore = 1.0 // order-independent floor: RiskScore is already <= 1
		v.Driver = "veto:" + vetoBy
	}
	return v
}

// Members returns a read-only view of the seats, for the control API.
func (p *Panel) Members() []MemberInfo {
	snaps := make([]statsSnapshot, len(p.seats))
	p.mu.Lock()
	for i := range p.seats {
		snaps[i] = p.seats[i].stats.snapshot()
	}
	p.mu.Unlock()
	// Sorting and reduction happen outside the lock so polling the control API
	// never blocks Assess.
	out := make([]MemberInfo, 0, len(p.seats))
	for i := range p.seats {
		s := &p.seats[i]
		out = append(out, MemberInfo{Name: s.m.Name(), Version: s.m.Version(), Shadow: s.shadow, Weight: s.weight, Stats: snaps[i].summary()})
	}
	return out
}
