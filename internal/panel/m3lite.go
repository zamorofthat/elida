package panel

import (
	"elida/internal/fingerprint"
	"elida/internal/session"
)

// Scorer is the subset of *fingerprint.M3LiteScorer this member needs.
type Scorer interface {
	Score(*session.Session) (float64, string, map[string]float64, error)
	IsShadow() bool
}

type m3liteMember struct{ s Scorer }

// NewM3LiteMember wraps the existing M3-lite scorer as a panel Member.
func NewM3LiteMember(s Scorer) Member { return m3liteMember{s: s} }

func (m m3liteMember) Name() string    { return "m3-lite" }
func (m m3liteMember) Version() string { return "1" }

// Assess maps the M3-lite bucket to a 0..1 anomaly that encodes today's risk
// points: Anomaly = BucketRiskPoints(bucket) / RiskNotable. Warm-up → 0, no
// Detail (nothing to persist yet). Shadow mode still computes and reports a
// real score (Detail is populated, with "shadow": true) so callers can
// persist distance/bucket/class as before, but Anomaly stays 0 so shadow
// scores never contribute risk points or trip the panel's aggregate.
func (m m3liteMember) Assess(f SessionFeatures) MemberOpinion {
	snap, _ := f.snapshot()
	op := MemberOpinion{Member: "m3-lite"}
	dist, bucket, _, err := m.s.Score(snap)
	if err != nil || bucket == fingerprint.BucketWarmUp {
		return op
	}
	shadow := m.s.IsShadow()
	op.Detail = map[string]any{"bucket": bucket, "distance": dist, "shadow": shadow}
	if !shadow {
		op.Anomaly = float64(fingerprint.BucketRiskPoints(bucket)) / float64(fingerprint.RiskNotable)
	}
	return op
}
