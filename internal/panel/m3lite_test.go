package panel

import (
	"testing"

	"elida/internal/fingerprint"
	"elida/internal/session"
)

type fakeScorer struct {
	dist   float64
	bucket string
	shadow bool
}

func (f fakeScorer) Score(*session.Session) (float64, string, map[string]float64, error) {
	return f.dist, f.bucket, map[string]float64{}, nil
}
func (f fakeScorer) IsShadow() bool { return f.shadow }

func TestM3LiteMember_AnomalyEncodesRiskPoints(t *testing.T) {
	// Notable bucket → BucketRiskPoints == RiskNotable → Anomaly == 1.0
	m := NewM3LiteMember(fakeScorer{dist: 4.5, bucket: fingerprint.BucketNotable})
	op := m.Assess(SessionFeatures{})
	if op.Anomaly != 1.0 {
		t.Fatalf("Anomaly = %v, want 1.0 for notable bucket", op.Anomaly)
	}
	if op.Detail["bucket"] != fingerprint.BucketNotable {
		t.Fatalf("Detail[bucket] = %v, want %q", op.Detail["bucket"], fingerprint.BucketNotable)
	}
}

func TestM3LiteMember_NormalBucketIsZeroAnomaly(t *testing.T) {
	m := NewM3LiteMember(fakeScorer{dist: 1.0, bucket: fingerprint.BucketNormal})
	if op := m.Assess(SessionFeatures{}); op.Anomaly != 0 {
		t.Fatalf("Anomaly = %v, want 0 for normal bucket", op.Anomaly)
	}
}

func TestM3LiteMember_WarmUpIsZeroAnomaly(t *testing.T) {
	m := NewM3LiteMember(fakeScorer{dist: 0, bucket: fingerprint.BucketWarmUp})
	if op := m.Assess(SessionFeatures{}); op.Anomaly != 0 {
		t.Fatalf("Anomaly = %v, want 0 for warm-up", op.Anomaly)
	}
}
