// Package panel is a pluggable ensemble ("panel") of behavioral detectors.
// Each Member judges a session; the Panel aggregates their opinions into a
// Verdict that feeds the policy risk-ladder. The panel informs; it never enforces.
package panel

// TurnFeature is one per-turn record; the Trajectory slot stays empty until a
// trajectory member exists (Phase 2).
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
}

// MemberOpinion is one detector's judgement. A member emits an anomaly score,
// a class, or both. Veto floors the panel RiskScore to 1.0.
type MemberOpinion struct {
	Member     string
	Anomaly    float64
	Class      string
	Confidence float64
	Veto       bool
	Detail     map[string]any
}

// Verdict is the structured output the panel produces.
type Verdict struct {
	RiskScore  float64
	Class      string
	Confidence float64
	Members    []MemberOpinion
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
}

type seat struct {
	m      Member
	shadow bool
	weight float64
}

// Panel holds seated members and aggregates their opinions.
type Panel struct {
	seats []seat
}

func NewPanel() *Panel { return &Panel{} }

// Seat adds a member. shadow members are assessed and reported but excluded
// from the aggregated RiskScore/Class.
func (p *Panel) Seat(m Member, shadow bool, weight float64) {
	p.seats = append(p.seats, seat{m: m, shadow: shadow, weight: weight})
}

// Assess runs every member and aggregates: weighted-max-with-veto.
func (p *Panel) Assess(f SessionFeatures) Verdict {
	v := Verdict{Members: make([]MemberOpinion, 0, len(p.seats))}
	var bestClassConf float64
	for _, s := range p.seats {
		op := s.m.Assess(f)
		v.Members = append(v.Members, op)
		if s.shadow {
			continue
		}
		if op.Veto {
			v.RiskScore = 1.0
		} else if w := s.weight * op.Anomaly; w > v.RiskScore {
			v.RiskScore = w
		}
		if op.Class != "" && op.Confidence >= bestClassConf {
			bestClassConf = op.Confidence
			v.Class = op.Class
			v.Confidence = op.Confidence
		}
	}
	return v
}

// Members returns a read-only view of the seats, for the control API.
func (p *Panel) Members() []MemberInfo {
	out := make([]MemberInfo, 0, len(p.seats))
	for _, s := range p.seats {
		out = append(out, MemberInfo{Name: s.m.Name(), Version: s.m.Version(), Shadow: s.shadow, Weight: s.weight})
	}
	return out
}
