package main

import (
	"math"

	"elida/internal/config"
	"elida/internal/panel"
)

// riskPointsFromVerdict maps a verdict to risk-ladder points and the source
// label (the driving member) for the policy engine.
func riskPointsFromVerdict(v panel.Verdict) (int, string) {
	return int(math.Round(v.RiskScore * float64(panel.MaxRiskPoints))), v.Driver
}

// seatToolChain seats the tool-chain member with the configured posture.
// Defaults (shadow, weight 1.0) observe only, reproducing Phase 2a.
func seatToolChain(p *panel.Panel, cfg config.PanelConfig, art *panel.ToolChainArtifact) {
	p.Seat(panel.NewToolChainMember(art), cfg.ToolChainShadow, cfg.ToolChainWeight)
}
