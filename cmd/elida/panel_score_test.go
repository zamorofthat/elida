package main

import (
	"testing"

	"elida/internal/config"
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

func TestSeatToolChain_DefaultsShadowAndConfiguredPosture(t *testing.T) {
	art, err := panel.LoadToolChainArtifact("../../internal/panel/testdata/tool-chain.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		cfg    config.PanelConfig
		shadow bool
		weight float64
	}{
		{config.PanelConfig{ToolChainShadow: true, ToolChainWeight: 1.0}, true, 1.0},
		{config.PanelConfig{ToolChainShadow: false, ToolChainWeight: 0.5}, false, 0.5},
	} {
		p := panel.NewPanel()
		seatToolChain(p, c.cfg, art)
		m := p.Members()[0]
		if m.Name != "tool-chain" || m.Shadow != c.shadow || m.Weight != c.weight {
			t.Fatalf("seated %+v, want shadow=%v weight=%v", m, c.shadow, c.weight)
		}
	}
}
