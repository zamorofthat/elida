package fingerprint

import "math"

// Cholesky7 computes the Cholesky decomposition of a 7x7 symmetric positive-definite matrix.
// Returns the lower triangular factor L such that A = L * Lᵀ, and ok=true on success.
// Returns ok=false if the matrix is not positive definite.
func Cholesky7(A [7][7]float64) (L [7][7]float64, ok bool) {
	for i := 0; i < 7; i++ {
		for j := 0; j <= i; j++ {
			sum := A[i][j]
			for k := 0; k < j; k++ {
				sum -= L[i][k] * L[j][k]
			}
			if i == j {
				if sum <= 0 {
					return L, false
				}
				L[i][j] = math.Sqrt(sum)
			} else {
				L[i][j] = sum / L[j][j]
			}
		}
	}
	return L, true
}

// MahalanobisCholesky computes the Mahalanobis distance given a Cholesky factor L
// and a difference vector diff = (x - μ).
// It solves L*z = diff by forward substitution and returns ||z||₂ = sqrt(D²).
func MahalanobisCholesky(L [7][7]float64, diff [7]float64) float64 {
	// Forward substitution: solve L*z = diff
	var z [7]float64
	for i := 0; i < 7; i++ {
		sum := diff[i]
		for j := 0; j < i; j++ {
			sum -= L[i][j] * z[j]
		}
		z[i] = sum / L[i][i]
	}

	// ||z||₂
	var d2 float64
	for i := 0; i < 7; i++ {
		d2 += z[i] * z[i]
	}
	return math.Sqrt(d2)
}

// FeatureContributions returns per-feature contributions to D² (z²ᵢ for each feature).
// Useful for interpretability: which features contributed most to the anomaly score.
func FeatureContributions(L [7][7]float64, diff [7]float64) [NumFeatures]float64 {
	var z [7]float64
	for i := 0; i < 7; i++ {
		sum := diff[i]
		for j := 0; j < i; j++ {
			sum -= L[i][j] * z[j]
		}
		z[i] = sum / L[i][i]
	}

	var contributions [NumFeatures]float64
	for i := 0; i < NumFeatures; i++ {
		contributions[i] = z[i] * z[i]
	}
	return contributions
}
