package fingerprint

import (
	"math"
	"strings"

	"elida/internal/session"
)

// NumFeatures is the number of features in the behavioral fingerprint.
const NumFeatures = 7

// Feature indices
const (
	FeatTurnCount         = 0
	FeatToolCallRatio     = 1
	FeatToolCallEntropy   = 2
	FeatCadenceMedian     = 3
	FeatCadenceCV         = 4
	FeatTokenRatio        = 5
	FeatBackendContinuity = 6
)

// FeatureNames maps feature indices to human-readable names.
var FeatureNames = [NumFeatures]string{
	"turn_count",
	"tool_call_ratio",
	"tool_call_entropy",
	"cadence_median",
	"cadence_cv",
	"token_ratio",
	"backend_continuity",
}

// FeatureVector holds the transformed feature values for a session.
type FeatureVector [NumFeatures]float64

// Extract computes the transformed feature vector from a session snapshot.
// The snapshot should be taken at session end (all fields finalized).
func Extract(snap *session.Session) FeatureVector {
	var fv FeatureVector

	// Feature 1: Turn count — log(1+x)
	fv[FeatTurnCount] = math.Log1p(float64(snap.RequestCount))

	// Feature 2: Tool call ratio — ToolCalls / RequestCount (0-1)
	if snap.RequestCount > 0 {
		fv[FeatToolCallRatio] = float64(snap.ToolCalls) / float64(snap.RequestCount)
	}

	// Feature 3: Tool call entropy — Shannon entropy over per-tool counts
	fv[FeatToolCallEntropy] = toolCallEntropy(snap.ToolCallCounts, snap.ToolCalls)

	// Feature 4 & 5: Turn cadence median and CV from message timestamps
	median, cv := cadenceStats(snap.Messages)
	fv[FeatCadenceMedian] = math.Log1p(median)
	fv[FeatCadenceCV] = math.Log1p(cv)

	// Feature 6: Token ratio — log(TokensIn / TokensOut)
	if snap.TokensOut > 0 && snap.TokensIn > 0 {
		fv[FeatTokenRatio] = math.Log(float64(snap.TokensIn) / float64(snap.TokensOut))
	}

	// Feature 7: Backend continuity — fraction of requests on primary backend
	fv[FeatBackendContinuity] = backendContinuity(snap.BackendsUsed)

	return fv
}

// SessionClass returns the classification key for a session.
// Format: "backend/model-family" → fallback "backend" → "global".
func SessionClass(snap *session.Session) string {
	backend := snap.Backend
	if backend == "" {
		return "global"
	}

	// Try to extract model family from metadata
	model := snap.Metadata["model"]
	if model == "" {
		return backend
	}

	// Extract model family: take up to first dash-separated version segment
	family := modelFamily(model)
	if family != "" {
		return backend + "/" + family
	}
	return backend
}

// ParentClass returns the parent class for fallback scoring.
// "backend/model-family" → "backend", "backend" → "global".
func ParentClass(class string) string {
	if idx := strings.LastIndex(class, "/"); idx >= 0 {
		return class[:idx]
	}
	if class != "global" {
		return "global"
	}
	return ""
}

// toolCallEntropy computes Shannon entropy over per-tool call counts.
func toolCallEntropy(counts map[string]int, total int) float64 {
	if total == 0 || len(counts) <= 1 {
		return 0
	}
	var entropy float64
	for _, count := range counts {
		if count == 0 {
			continue
		}
		p := float64(count) / float64(total)
		entropy -= p * math.Log2(p)
	}
	return entropy
}

// cadenceStats computes the median and coefficient of variation (CV) of
// inter-message gaps in milliseconds.
func cadenceStats(messages []session.Message) (median, cv float64) {
	if len(messages) < 2 {
		return 0, 0
	}

	gaps := make([]float64, 0, len(messages)-1)
	for i := 1; i < len(messages); i++ {
		gap := messages[i].Timestamp.Sub(messages[i-1].Timestamp).Seconds() * 1000 // ms
		if gap >= 0 {
			gaps = append(gaps, gap)
		}
	}

	if len(gaps) == 0 {
		return 0, 0
	}

	// Median via partial sort
	median = selectMedian(gaps)

	// CV = stddev / mean
	var sum float64
	for _, g := range gaps {
		sum += g
	}
	mean := sum / float64(len(gaps))
	if mean == 0 {
		return median, 0
	}

	var variance float64
	for _, g := range gaps {
		d := g - mean
		variance += d * d
	}
	variance /= float64(len(gaps))
	cv = math.Sqrt(variance) / mean

	return median, cv
}

// selectMedian returns the median of a slice (modifies the slice).
func selectMedian(data []float64) float64 {
	n := len(data)
	if n == 0 {
		return 0
	}
	// Simple sort for small slices (messages capped at 100)
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if data[j] < data[i] {
				data[i], data[j] = data[j], data[i]
			}
		}
	}
	if n%2 == 0 {
		return (data[n/2-1] + data[n/2]) / 2
	}
	return data[n/2]
}

// backendContinuity returns the fraction of total requests handled by the most-used backend.
func backendContinuity(backendsUsed map[string]int) float64 {
	if len(backendsUsed) == 0 {
		return 1.0 // single backend assumed
	}
	var maxCount, total int
	for _, count := range backendsUsed {
		total += count
		if count > maxCount {
			maxCount = count
		}
	}
	if total == 0 {
		return 1.0
	}
	return float64(maxCount) / float64(total)
}

// modelFamily extracts a model family from a model name.
// e.g., "claude-3-opus-20240229" → "claude-3-opus"
// e.g., "gpt-4o-mini" → "gpt-4o"
// e.g., "mistral-large-latest" → "mistral-large"
func modelFamily(model string) string {
	model = strings.ToLower(model)

	// Strip common version suffixes
	for _, suffix := range []string{"-latest", "-preview"} {
		model = strings.TrimSuffix(model, suffix)
	}

	// Strip date suffixes like -20240229
	parts := strings.Split(model, "-")
	if len(parts) > 1 {
		last := parts[len(parts)-1]
		if len(last) >= 8 && isAllDigits(last) {
			parts = parts[:len(parts)-1]
		}
	}

	// Strip minor version numbers (keep major model class)
	// e.g., "gpt-4o-mini-2024" → "gpt-4o-mini"
	return strings.Join(parts, "-")
}

func isAllDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
