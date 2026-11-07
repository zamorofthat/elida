package main

import (
	"math"

	"elida/internal/config"
	"elida/internal/panel"
	"elida/internal/storage"
)

// riskPointsFromVerdict maps a verdict to risk-ladder points and the source
// label (the driving member) for the policy engine.
func riskPointsFromVerdict(v panel.Verdict) (int, string) {
	return int(math.Round(v.RiskScore * float64(panel.MaxRiskPoints))), v.Driver
}

// panelOpinions projects a verdict's member opinions into their persisted
// form, one entry per seated member in seat order.
//
// A member's Detail is an open map[string]any, so it is never copied
// wholesale: only the keys below are read, each with a checked type
// assertion, and everything else — including anything a future member might
// put there — is dropped. That is what keeps request content out of the
// panel_opinions column and off the fingerprint log line.
func panelOpinions(v panel.Verdict) []storage.PanelOpinion {
	if len(v.Members) == 0 {
		return nil
	}
	out := make([]storage.PanelOpinion, 0, len(v.Members))
	for _, m := range v.Members {
		op := storage.PanelOpinion{
			Member:  m.Member,
			Shadow:  m.Shadow,
			Weight:  m.Weight,
			Anomaly: m.Anomaly,
			Veto:    m.Veto,
		}
		op.Bucket, _ = m.Detail["bucket"].(string)
		op.Distance, _ = m.Detail["distance"].(float64)
		op.Perplexity, _ = m.Detail["perplexity"].(float64)
		op.Percentile, _ = m.Detail["percentile"].(float64)
		op.ClassUsed, _ = m.Detail["class_used"].(string)
		op.Reason, _ = m.Detail["reason"].(string)
		out = append(out, op)
	}
	return out
}

// seatToolChain seats the tool-chain member with the configured posture.
// Defaults (shadow, weight 1.0) observe only, reproducing Phase 2a.
func seatToolChain(p *panel.Panel, cfg config.PanelConfig, art *panel.ToolChainArtifact) {
	p.Seat(panel.NewToolChainMember(art), cfg.ToolChainShadow, cfg.ToolChainWeight)
}
