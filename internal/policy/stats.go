package policy

import (
	"math"
)

// PoissonSurvival returns P(X >= k) for Poisson(lambda).
// Computes 1 - CDF(k-1) via log-space summation to avoid overflow for large k.
func PoissonSurvival(lambda float64, k int) float64 {
	if lambda <= 0 {
		if k <= 0 {
			return 1.0
		}
		return 0.0
	}
	if k <= 0 {
		return 1.0
	}

	// P(X >= k) = 1 - sum_{i=0}^{k-1} e^{-lambda} * lambda^i / i!
	// Compute each term in log-space: log(P(X=i)) = -lambda + i*log(lambda) - lgamma(i+1)
	var cdf float64
	for i := 0; i < k; i++ {
		logPMF := -lambda + float64(i)*math.Log(lambda) - lgammaInt(i)
		cdf += math.Exp(logPMF)
	}

	survival := 1.0 - cdf
	if survival < 0 {
		return 0.0
	}
	return survival
}

// lgammaInt returns log(n!) = lgamma(n+1) for non-negative integers.
func lgammaInt(n int) float64 {
	v, _ := math.Lgamma(float64(n + 1))
	return v
}

// ShannonEntropy returns bits-per-byte entropy of data.
// Natural language ~4.0-4.5, base64 ~5.9-6.0, random ~7.9-8.0.
// Returns 0 for empty input.
func ShannonEntropy(data []byte) float64 {
	if len(data) == 0 {
		return 0.0
	}

	var freq [256]int
	for _, b := range data {
		freq[b]++
	}

	n := float64(len(data))
	var entropy float64
	for _, count := range freq {
		if count == 0 {
			continue
		}
		p := float64(count) / n
		entropy -= p * math.Log2(p)
	}

	return entropy
}
