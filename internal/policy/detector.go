package policy

import (
	"math"
	"time"
)

// Default parameters for compound anomaly detection.
// Tuned for agentic traffic (bursty, phased execution patterns).
const (
	DefaultHalfLife          = 5.0  // seconds — EMA tracks within-burst rate changes
	DefaultCUSUMSlack        = 3.0  // tolerates 3 req/s above baseline before accumulating
	DefaultCUSUMThreshold    = 10.0 // ~4 seconds of sustained anomaly to alarm
	DefaultGapThreshold      = 2.0  // seconds — gap longer than this = new burst
	DefaultWarmupRequests    = 5    // skip first N requests per burst
	DefaultCompoundThreshold = 0.15 // alarm when rate*entropy score exceeds this
	DefaultEntropyBaseline   = 4.5  // below this = normal structured content
	DefaultEntropyRange      = 3.5  // entropy scale: (H - baseline) / range
	MaxBurstHistory          = 8    // ring buffer size for burst summaries
)

// CompoundAnomalyConfig holds tunable parameters for the detector.
// Zero values fall back to defaults.
type CompoundAnomalyConfig struct {
	HalfLife          float64 `yaml:"half_life" json:"half_life,omitempty"`
	CUSUMSlack        float64 `yaml:"cusum_slack" json:"cusum_slack,omitempty"`
	CUSUMThreshold    float64 `yaml:"cusum_threshold" json:"cusum_threshold,omitempty"`
	GapThreshold      float64 `yaml:"gap_threshold" json:"gap_threshold,omitempty"`
	WarmupRequests    int     `yaml:"warmup_requests" json:"warmup_requests,omitempty"`
	CompoundThreshold float64 `yaml:"compound_threshold" json:"compound_threshold,omitempty"`
	EntropyBaseline   float64 `yaml:"entropy_baseline" json:"entropy_baseline,omitempty"`
}

// BurstSummary records statistics for a completed burst.
type BurstSummary struct {
	Duration    time.Duration
	Count       int
	MeanEntropy float64
}

// SessionDetector tracks per-session state for adaptive CUSUM + compound scoring.
// All updates are O(1) time. Total state is ~2KB per session.
type SessionDetector struct {
	// Rate tracking (adaptive CUSUM)
	emaRate    float64
	cusumHigh  float64
	lastTime   time.Time
	burstCount int
	burstStart time.Time
	initialized bool

	// Incremental entropy for current burst
	byteFreq  [256]int64
	byteTotal int64

	// Burst history (ring buffer)
	bursts    [MaxBurstHistory]BurstSummary
	burstIdx  int
	burstFill int

	// Config
	cfg CompoundAnomalyConfig
}

// NewSessionDetector creates a detector with the given config.
func NewSessionDetector(cfg CompoundAnomalyConfig) *SessionDetector {
	return &SessionDetector{cfg: cfg}
}

func (d *SessionDetector) halfLife() float64 {
	if d.cfg.HalfLife > 0 {
		return d.cfg.HalfLife
	}
	return DefaultHalfLife
}

func (d *SessionDetector) cusumSlack() float64 {
	if d.cfg.CUSUMSlack > 0 {
		return d.cfg.CUSUMSlack
	}
	return DefaultCUSUMSlack
}

func (d *SessionDetector) cusumThreshold() float64 {
	if d.cfg.CUSUMThreshold > 0 {
		return d.cfg.CUSUMThreshold
	}
	return DefaultCUSUMThreshold
}

func (d *SessionDetector) gapThreshold() float64 {
	if d.cfg.GapThreshold > 0 {
		return d.cfg.GapThreshold
	}
	return DefaultGapThreshold
}

func (d *SessionDetector) warmupRequests() int {
	if d.cfg.WarmupRequests > 0 {
		return d.cfg.WarmupRequests
	}
	return DefaultWarmupRequests
}

func (d *SessionDetector) compoundThreshold() float64 {
	if d.cfg.CompoundThreshold > 0 {
		return d.cfg.CompoundThreshold
	}
	return DefaultCompoundThreshold
}

func (d *SessionDetector) entropyBaseline() float64 {
	if d.cfg.EntropyBaseline > 0 {
		return d.cfg.EntropyBaseline
	}
	return DefaultEntropyBaseline
}

// Update processes a new request and returns the compound anomaly score.
// Returns 0 during warmup or if no anomaly detected.
// contentBytes is the request/response body for entropy tracking (can be nil).
func (d *SessionDetector) Update(now time.Time, contentBytes []byte) float64 {
	if !d.initialized {
		d.initialized = true
		d.lastTime = now
		d.burstStart = now
		d.addBytes(contentBytes)
		d.burstCount = 1
		return 0
	}

	gap := now.Sub(d.lastTime).Seconds()
	if gap <= 0 {
		gap = 0.001 // floor to avoid division by zero
	}

	// Detect burst boundary
	if gap > d.gapThreshold() {
		d.finalizeBurst()
		d.cusumHigh = 0
		d.burstCount = 0
		d.burstStart = now
		d.resetEntropy()
	}

	// Time-weighted EMA rate update
	instantRate := 1.0 / gap
	decayFactor := math.Exp(-gap / d.halfLife())
	d.emaRate = decayFactor*d.emaRate + (1-decayFactor)*instantRate

	// CUSUM accumulation: deviation above EMA baseline minus slack
	deviation := instantRate - d.emaRate - d.cusumSlack()
	d.cusumHigh = math.Max(0, d.cusumHigh+deviation)

	// Incremental entropy
	d.addBytes(contentBytes)

	d.burstCount++
	d.lastTime = now

	// No scoring during warmup
	if d.burstCount < d.warmupRequests() {
		return 0
	}

	return d.compoundScore()
}

// compoundScore returns the multiplicative fusion of rate and entropy signals.
func (d *SessionDetector) compoundScore() float64 {
	rateScore := Clamp(d.cusumHigh/d.cusumThreshold(), 0, 1)

	entropyScore := 0.0
	if d.byteTotal > 0 {
		h := d.entropy()
		entropyScore = Clamp((h-d.entropyBaseline())/DefaultEntropyRange, 0, 1)
	}

	return rateScore * entropyScore
}

// RateScore returns the current normalized CUSUM score (0-1).
func (d *SessionDetector) RateScore() float64 {
	return Clamp(d.cusumHigh/d.cusumThreshold(), 0, 1)
}

// EntropyScore returns the current normalized entropy score (0-1).
func (d *SessionDetector) EntropyScore() float64 {
	if d.byteTotal == 0 {
		return 0
	}
	h := d.entropy()
	return Clamp((h-d.entropyBaseline())/DefaultEntropyRange, 0, 1)
}

// Entropy returns the current burst entropy in bits per byte.
func (d *SessionDetector) Entropy() float64 {
	return d.entropy()
}

// BurstCount returns the number of requests in the current burst.
func (d *SessionDetector) BurstCount() int {
	return d.burstCount
}

// CUSUMHigh returns the current upper CUSUM statistic.
func (d *SessionDetector) CUSUMHigh() float64 {
	return d.cusumHigh
}

// BurstHistoryLen returns the number of completed bursts in the ring buffer.
func (d *SessionDetector) BurstHistoryLen() int {
	return d.burstFill
}

// addBytes updates the incremental byte frequency table.
func (d *SessionDetector) addBytes(data []byte) {
	for _, b := range data {
		d.byteFreq[b]++
	}
	d.byteTotal += int64(len(data))
}

// entropy computes Shannon entropy from the running byte frequency table. O(256).
func (d *SessionDetector) entropy() float64 {
	if d.byteTotal == 0 {
		return 0
	}
	n := float64(d.byteTotal)
	var h float64
	for _, count := range d.byteFreq {
		if count == 0 {
			continue
		}
		p := float64(count) / n
		h -= p * math.Log2(p)
	}
	return h
}

// resetEntropy clears the byte frequency table for a new burst.
func (d *SessionDetector) resetEntropy() {
	d.byteFreq = [256]int64{}
	d.byteTotal = 0
}

// finalizeBurst saves a summary of the completed burst to the ring buffer.
func (d *SessionDetector) finalizeBurst() {
	if d.burstCount == 0 {
		return
	}
	summary := BurstSummary{
		Duration:    d.lastTime.Sub(d.burstStart),
		Count:       d.burstCount,
		MeanEntropy: d.entropy(),
	}
	d.bursts[d.burstIdx] = summary
	d.burstIdx = (d.burstIdx + 1) % MaxBurstHistory
	if d.burstFill < MaxBurstHistory {
		d.burstFill++
	}
}

// Clamp restricts v to [lo, hi].
func Clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
