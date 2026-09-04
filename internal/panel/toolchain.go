package panel

import "math"

type toolChainMember struct{ a *ToolChainArtifact }

// NewToolChainMember builds a panel member from a loaded tool-chain artifact.
func NewToolChainMember(a *ToolChainArtifact) Member { return toolChainMember{a: a} }

func (m toolChainMember) Name() string    { return "tool-chain" }
func (m toolChainMember) Version() string { return m.a.GeneratedBy }

func (m toolChainMember) Assess(f SessionFeatures) MemberOpinion {
	op := MemberOpinion{Member: "tool-chain"}
	if len(f.Trajectory) < 2 {
		op.Detail = map[string]any{"reason": "insufficient-trajectory"}
		return op
	}
	chain, classUsed, ok := m.chainFor(f.Class)
	if !ok {
		op.Detail = map[string]any{"reason": "no-chain"}
		return op
	}
	idx := indexOf(chain.States)
	// sum of -ln P over transitions; first tool uses Start
	first := probAt(chain.Start, idx, f.Trajectory[0].Tool, chain.OOVFloor)
	sumLn := -math.Log(first)
	nTrans := 1
	for k := 1; k < len(f.Trajectory); k++ {
		from, okFrom := idx[f.Trajectory[k-1].Tool]
		var p float64
		if !okFrom {
			p = chain.OOVFloor
		} else {
			p = probAt(chain.Matrix[from], idx, f.Trajectory[k].Tool, chain.OOVFloor)
		}
		sumLn += -math.Log(p)
		nTrans++
	}
	perplexity := math.Exp(sumLn / float64(nTrans))
	pct := percentile(chain.Calibration, perplexity)
	op.Anomaly = pct
	op.Detail = map[string]any{"perplexity": perplexity, "percentile": pct, "class_used": classUsed}
	return op
}

func (m toolChainMember) chainFor(class string) (ClassChain, string, bool) {
	if c, ok := m.a.Classes[class]; ok {
		return c, class, true
	}
	if c, ok := m.a.Classes["global"]; ok {
		return c, "global", true
	}
	return ClassChain{}, "", false
}

func indexOf(states []string) map[string]int {
	idx := make(map[string]int, len(states))
	for i, s := range states {
		idx[s] = i
	}
	return idx
}

func probAt(row []float64, idx map[string]int, tool string, floor float64) float64 {
	j, ok := idx[tool]
	if !ok || j >= len(row) || row[j] <= 0 {
		return floor
	}
	return row[j]
}

// percentile maps perplexity to [0,1] by interpolating the calibration table.
func percentile(c Calibration, x float64) float64 {
	pp := c.Perplexity
	if x <= pp[0] {
		return c.Percentiles[0] / 100.0
	}
	if x >= pp[len(pp)-1] {
		return c.Percentiles[len(pp)-1] / 100.0
	}
	for i := 1; i < len(pp); i++ {
		if x <= pp[i] {
			frac := (x - pp[i-1]) / (pp[i] - pp[i-1])
			p := c.Percentiles[i-1] + frac*(c.Percentiles[i]-c.Percentiles[i-1])
			return p / 100.0
		}
	}
	return c.Percentiles[len(c.Percentiles)-1] / 100.0
}
