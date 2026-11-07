package main

import (
	"testing"

	"elida/internal/fingerprint"
	"elida/internal/panel"
)

func TestRiskPointsFromVerdict_ReproducesM3LiteLadder(t *testing.T) {
	for _, b := range []string{fingerprint.BucketNormal, fingerprint.BucketMinor, fingerprint.BucketNotable, fingerprint.BucketAnomalous, fingerprint.BucketSevere} {
		v := panel.Verdict{RiskScore: float64(fingerprint.BucketRiskPoints(b)) / float64(fingerprint.RiskSevere), Driver: "m3-lite"}
		got, src := riskPointsFromVerdict(v)
		if got != fingerprint.BucketRiskPoints(b) || src != "m3-lite" {
			t.Fatalf("bucket %s: points=%d src=%q, want %d / m3-lite", b, got, src, fingerprint.BucketRiskPoints(b))
		}
	}
}

func TestRiskPointsFromVerdict_ToolChainDriverAndWeight(t *testing.T) {
	// 0.99 percentile at weight 0.5 contributes 0.495 -> round(0.495*20) = 10 points.
	got, src := riskPointsFromVerdict(panel.Verdict{RiskScore: 0.5 * 0.99, Driver: "tool-chain"})
	if got != 10 || src != "tool-chain" {
		t.Fatalf("points=%d src=%q, want 10 / tool-chain", got, src)
	}
}
