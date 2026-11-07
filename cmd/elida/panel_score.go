package main

import (
	"math"

	"elida/internal/panel"
)

// riskPointsFromVerdict maps a verdict to risk-ladder points and the source
// label (the driving member) for the policy engine.
func riskPointsFromVerdict(v panel.Verdict) (int, string) {
	return int(math.Round(v.RiskScore * float64(panel.MaxRiskPoints))), v.Driver
}
