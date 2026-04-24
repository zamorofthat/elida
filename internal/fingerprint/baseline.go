package fingerprint

import (
	"sync"
	"time"
)

// DefaultNEff is the default effective window size for EWMA.
// 900 ≈ 90 days × 10 sessions/day.
const DefaultNEff = 900

// DefaultRidgeLambda is the ridge regularization parameter.
const DefaultRidgeLambda = 1e-6

// DefaultWarmUp is the minimum number of sessions before producing real scores.
const DefaultWarmUp = 100

// Baseline holds the streaming statistics for a session class.
type Baseline struct {
	mu sync.RWMutex

	Class      string                   `json:"class"`
	Count      int                      `json:"count"`
	Mean       FeatureVector            `json:"mean"`
	Covariance [7][7]float64            `json:"covariance"`
	Low        [NumFeatures]*P2Quantile `json:"low"`  // 1st percentile per feature
	High       [NumFeatures]*P2Quantile `json:"high"` // 99th percentile per feature
	UpdatedAt  time.Time                `json:"updated_at"`

	nEff        int
	ridgeLambda float64
	warmUp      int
}

// BaselineConfig holds tuning parameters for baselines.
type BaselineConfig struct {
	NEff        int
	RidgeLambda float64
	WarmUp      int
}

// DefaultBaselineConfig returns the default configuration.
func DefaultBaselineConfig() BaselineConfig {
	return BaselineConfig{
		NEff:        DefaultNEff,
		RidgeLambda: DefaultRidgeLambda,
		WarmUp:      DefaultWarmUp,
	}
}

// NewBaseline creates a new baseline for a session class.
func NewBaseline(class string, cfg BaselineConfig) *Baseline {
	b := &Baseline{
		Class:       class,
		nEff:        cfg.NEff,
		ridgeLambda: cfg.RidgeLambda,
		warmUp:      cfg.WarmUp,
	}
	for i := 0; i < NumFeatures; i++ {
		b.Low[i] = NewP2Quantile(0.01)
		b.High[i] = NewP2Quantile(0.99)
	}
	return b
}

// Update ingests a new feature vector using EWMA for mean and covariance.
func (b *Baseline) Update(fv FeatureVector) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Update quantile estimators with raw values
	for i := 0; i < NumFeatures; i++ {
		b.Low[i].Add(fv[i])
		b.High[i].Add(fv[i])
	}

	// Winsorize before updating mean/covariance
	w := b.winsorize(fv)

	b.Count++
	alpha := b.alpha()

	if b.Count == 1 {
		b.Mean = w
		// Covariance stays zero for first sample
		b.UpdatedAt = time.Now()
		return
	}

	// EWMA update for mean and covariance
	var diff [NumFeatures]float64
	for i := 0; i < NumFeatures; i++ {
		diff[i] = w[i] - b.Mean[i]
	}

	// Update mean: μ = (1-α)·μ + α·x
	for i := 0; i < NumFeatures; i++ {
		b.Mean[i] = (1-alpha)*b.Mean[i] + alpha*w[i]
	}

	// Update covariance: M2 = (1-α)·M2 + α·outer(delta, x-μ_new)
	var newDiff [NumFeatures]float64
	for i := 0; i < NumFeatures; i++ {
		newDiff[i] = w[i] - b.Mean[i]
	}
	for i := 0; i < NumFeatures; i++ {
		for j := 0; j < NumFeatures; j++ {
			b.Covariance[i][j] = (1-alpha)*b.Covariance[i][j] + alpha*diff[i]*newDiff[j]
		}
	}

	b.UpdatedAt = time.Now()
}

// IsWarm returns true when the baseline has enough samples for reliable scoring.
func (b *Baseline) IsWarm() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.Count >= b.warmUp
}

// GetCount returns the current sample count.
func (b *Baseline) GetCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.Count
}

// RegularizedCovariance returns Σ + λI for Cholesky decomposition.
func (b *Baseline) RegularizedCovariance() [7][7]float64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	var reg [7][7]float64
	for i := 0; i < 7; i++ {
		for j := 0; j < 7; j++ {
			reg[i][j] = b.Covariance[i][j]
		}
		reg[i][i] += b.ridgeLambda
	}
	return reg
}

// GetMean returns a copy of the current mean vector.
func (b *Baseline) GetMean() FeatureVector {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.Mean
}

// Winsorize clips a feature vector to the baseline's current 1st/99th percentiles.
func (b *Baseline) Winsorize(fv FeatureVector) FeatureVector {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.winsorize(fv)
}

// winsorize must be called with mu held.
func (b *Baseline) winsorize(fv FeatureVector) FeatureVector {
	var w FeatureVector
	for i := 0; i < NumFeatures; i++ {
		lo := b.Low[i].Estimate()
		hi := b.High[i].Estimate()
		v := fv[i]
		if b.Count >= 5 { // need enough samples for meaningful bounds
			if v < lo {
				v = lo
			}
			if v > hi {
				v = hi
			}
		}
		w[i] = v
	}
	return w
}

// alpha returns the EWMA smoothing factor.
func (b *Baseline) alpha() float64 {
	return 2.0 / (float64(b.nEff) + 1.0)
}
