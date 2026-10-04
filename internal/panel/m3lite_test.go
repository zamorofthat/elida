package panel

import (
	"math"
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
	op := m.Assess(SessionFeatures{})
	if op.Anomaly != 0 {
		t.Fatalf("Anomaly = %v, want 0 for warm-up", op.Anomaly)
	}
	if op.Detail != nil {
		t.Fatalf("Detail = %v, want nil for warm-up (nothing to persist)", op.Detail)
	}
}

// Shadow mode still computes a real score (distance/bucket surface in Detail
// so callers can persist them, matching the pre-panel scoreFingerprint
// behavior of "scored=true but not enforced"), but must never contribute
// risk points via Anomaly.
func TestM3LiteMember_ShadowReportsDetailButZeroAnomaly(t *testing.T) {
	m := NewM3LiteMember(fakeScorer{dist: 4.5, bucket: fingerprint.BucketNotable, shadow: true})
	op := m.Assess(SessionFeatures{})
	if op.Anomaly != 0 {
		t.Fatalf("Anomaly = %v, want 0 for shadow mode (never enforced)", op.Anomaly)
	}
	if op.Detail["bucket"] != fingerprint.BucketNotable {
		t.Fatalf("Detail[bucket] = %v, want %q", op.Detail["bucket"], fingerprint.BucketNotable)
	}
	if op.Detail["distance"] != 4.5 {
		t.Fatalf("Detail[distance] = %v, want 4.5", op.Detail["distance"])
	}
	if op.Detail["shadow"] != true {
		t.Fatalf("Detail[shadow] = %v, want true", op.Detail["shadow"])
	}
}

// TestPanelOfOne_PreservesRiskPoints is the behavior-preservation golden test
// for Task 4 (cmd/elida/main.go panel wiring). It proves that a panel seated
// with only the M3-lite member reconstructs, via
// round(RiskScore * fingerprint.RiskNotable), exactly the same integer risk
// points that the pre-panel scoreFingerprint computed directly from
// fingerprint.BucketRiskPoints(bucket). If this test fails, the Task 2/3
// encoding (Anomaly = BucketRiskPoints(bucket) / RiskNotable) is wrong —
// fix the mapping, not this test.
func TestPanelOfOne_PreservesRiskPoints(t *testing.T) {
	cases := []struct {
		bucket string
		want   int
	}{
		{fingerprint.BucketNormal, fingerprint.BucketRiskPoints(fingerprint.BucketNormal)},
		{fingerprint.BucketMinor, fingerprint.BucketRiskPoints(fingerprint.BucketMinor)},
		{fingerprint.BucketNotable, fingerprint.BucketRiskPoints(fingerprint.BucketNotable)},
		{fingerprint.BucketAnomalous, fingerprint.BucketRiskPoints(fingerprint.BucketAnomalous)},
		{fingerprint.BucketSevere, fingerprint.BucketRiskPoints(fingerprint.BucketSevere)},
	}
	for _, c := range cases {
		p := NewPanel()
		p.Seat(NewM3LiteMember(fakeScorer{bucket: c.bucket}), false, 1.0)
		v := p.Assess(SessionFeatures{})
		got := int(math.Round(v.RiskScore * float64(fingerprint.RiskNotable)))
		if got != c.want {
			t.Fatalf("bucket %s: reconstructed points = %d, want %d", c.bucket, got, c.want)
		}
	}
}
