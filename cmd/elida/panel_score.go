package main

import (
	"math"

	"elida/internal/config"
	"elida/internal/fingerprint"
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

// anomalyFindingRung is the weighted anomaly a live member must reach to
// earn its own OCSF finding: the risk-notable rung of the ladder on the 0..1
// scale (5/20 = 0.25).
//
// For the only live member today that is exactly today's emit condition. A
// live M3-lite seat (weight 1.0) reports Anomaly =
// BucketRiskPoints(bucket)/RiskSevere — 0, 0.10, 0.25, 0.50, 1.00 for
// normal/minor/notable/anomalous/severe — so weight*Anomaly >= 0.25 holds
// for exactly the notable, anomalous and severe buckets: the same set as
// `bucket != normal && bucket != minor`. Every value is a ratio of small
// powers of two, so the comparison is exact, not approximate.
const anomalyFindingRung = float64(fingerprint.RiskNotable) / float64(panel.MaxRiskPoints)

// anomalyFindingMembers names the members that earn an OCSF anomaly finding
// for this verdict, in seat order: live seats whose weighted anomaly reaches
// the notable rung. Shadow seats are excluded — they are observed and
// persisted, never reported — and so is any member below the rung.
//
// It is the panel's reporting rule, not the risk rule: every qualifying
// member is named, not just the one that drove RiskScore.
func anomalyFindingMembers(v panel.Verdict) []string {
	var out []string
	for _, m := range v.Members {
		if m.Shadow {
			continue
		}
		if m.Weight*m.Anomaly >= anomalyFindingRung {
			out = append(out, m.Member)
		}
	}
	return out
}

// opinionFor returns the named member's opinion, or the zero opinion (nil
// Detail) when that member did not sit or did not report.
func opinionFor(v panel.Verdict, name string) panel.MemberOpinion {
	for i := range v.Members {
		if v.Members[i].Member == name {
			return v.Members[i]
		}
	}
	return panel.MemberOpinion{}
}
