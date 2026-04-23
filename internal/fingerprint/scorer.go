package fingerprint

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"elida/internal/session"
)

// Score bucket thresholds (Mahalanobis distance)
const (
	ThresholdMinor     = 3.3
	ThresholdNotable   = 4.1
	ThresholdAnomalous = 5.0
	ThresholdSevere    = 6.0
)

// Score bucket names
const (
	BucketNormal    = "normal"
	BucketMinor     = "minor"
	BucketNotable   = "notable"
	BucketAnomalous = "anomalous"
	BucketSevere    = "severe"
	BucketWarmUp    = "warm_up" // not enough data
)

// Risk points per bucket
const (
	RiskNormal    = 0
	RiskMinor     = 2
	RiskNotable   = 5
	RiskAnomalous = 10
	RiskSevere    = 20
)

// AnomalyScorer scores sessions for behavioral anomalies.
type AnomalyScorer interface {
	Score(snap *session.Session) (distance float64, bucket string, features map[string]float64, err error)
	Ingest(snap *session.Session) error
	Close() error
}

// M3LiteScorer implements AnomalyScorer using Mahalanobis distance over structural features.
type M3LiteScorer struct {
	mu            sync.RWMutex
	baselines     map[string]*Baseline
	store         BaselineStore
	shadow        bool
	cfg           BaselineConfig
	flushInterval time.Duration
	dirty         bool // set true on Ingest(), cleared on flush
}

// NewM3LiteScorer creates a new M3-lite anomaly scorer.
func NewM3LiteScorer(store BaselineStore, shadow bool, cfg BaselineConfig) (*M3LiteScorer, error) {
	return NewM3LiteScorerWithFlush(store, shadow, cfg, 5*time.Minute)
}

// NewM3LiteScorerWithFlush creates a new M3-lite anomaly scorer with a custom flush interval.
func NewM3LiteScorerWithFlush(store BaselineStore, shadow bool, cfg BaselineConfig, flushInterval time.Duration) (*M3LiteScorer, error) {
	baselines, err := store.Load(cfg)
	if err != nil {
		slog.Warn("failed to load baselines, starting fresh", "error", err)
		baselines = make(map[string]*Baseline)
	}

	return &M3LiteScorer{
		baselines:     baselines,
		store:         store,
		shadow:        shadow,
		cfg:           cfg,
		flushInterval: flushInterval,
	}, nil
}

// Score computes the Mahalanobis distance for a session.
// In shadow mode, returns immediately without scoring.
func (s *M3LiteScorer) Score(snap *session.Session) (float64, string, map[string]float64, error) {
	if s.shadow {
		return 0, BucketWarmUp, nil, nil
	}

	fv := Extract(snap)
	class := SessionClass(snap)

	s.mu.RLock()
	baseline, distance, bucket, contributions := s.scoreAgainstBaseline(class, fv)
	s.mu.RUnlock()

	if baseline == nil {
		return 0, BucketWarmUp, nil, nil
	}

	// Build feature contribution map for interpretability
	features := make(map[string]float64, NumFeatures)
	for i := 0; i < NumFeatures; i++ {
		features[FeatureNames[i]] = contributions[i]
	}

	return distance, bucket, features, nil
}

// scoreAgainstBaseline tries class → parent → global fallback.
// Must be called with s.mu held for reading.
func (s *M3LiteScorer) scoreAgainstBaseline(class string, fv FeatureVector) (*Baseline, float64, string, [NumFeatures]float64) {
	// Try exact class first, then parent, then global
	for c := class; c != ""; c = ParentClass(c) {
		if b, ok := s.baselines[c]; ok && b.IsWarm() {
			distance, contributions := s.computeDistance(b, fv)
			bucket := distanceToBucket(distance)
			return b, distance, bucket, contributions
		}
	}
	return nil, 0, BucketWarmUp, [NumFeatures]float64{}
}

// computeDistance computes Mahalanobis distance against a baseline.
func (s *M3LiteScorer) computeDistance(b *Baseline, fv FeatureVector) (float64, [NumFeatures]float64) {
	mean := b.GetMean()
	regCov := b.RegularizedCovariance()

	L, ok := Cholesky7(regCov)
	if !ok {
		slog.Warn("Cholesky decomposition failed for baseline", "class", b.Class)
		return 0, [NumFeatures]float64{}
	}

	var diff [7]float64
	for i := 0; i < NumFeatures; i++ {
		diff[i] = fv[i] - mean[i]
	}

	distance := MahalanobisCholesky(L, diff)
	contributions := FeatureContributions(L, diff)
	return distance, contributions
}

// Ingest updates baselines for all applicable class levels.
func (s *M3LiteScorer) Ingest(snap *session.Session) error {
	fv := Extract(snap)
	class := SessionClass(snap)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Update at all class levels: specific → parent → global
	for c := class; c != ""; c = ParentClass(c) {
		b, ok := s.baselines[c]
		if !ok {
			b = NewBaseline(c, s.cfg)
			s.baselines[c] = b
		}
		b.Update(fv)
	}

	s.dirty = true
	return nil
}

// Run starts a background loop that periodically flushes dirty baselines to the store.
// It follows the same pattern as session.Manager.Run(ctx).
func (s *M3LiteScorer) Run(ctx context.Context) {
	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.flush()
			slog.Info("fingerprint flush loop stopped")
			return
		case <-ticker.C:
			s.flush()
		}
	}
}

// flush persists baselines if any have been updated since the last flush.
// Takes a write lock for the check+clear, then holds RLock during Save to
// prevent concurrent map mutation from Ingest.
func (s *M3LiteScorer) flush() {
	s.mu.RLock()
	if !s.dirty {
		s.mu.RUnlock()
		return
	}
	s.mu.RUnlock()

	s.mu.Lock()
	// Re-check under write lock (another goroutine may have flushed).
	if !s.dirty {
		s.mu.Unlock()
		return
	}
	s.dirty = false
	// Save under write lock — blocks Ingest but prevents map races.
	// Save is fast (SQLite tx, runs every flushInterval) so contention is minimal.
	err := s.store.Save(s.baselines)
	if err != nil {
		s.dirty = true // retry next tick
	}
	s.mu.Unlock()

	if err != nil {
		slog.Error("periodic baseline flush failed", "error", err)
	}
}

// Close persists baselines and releases resources.
func (s *M3LiteScorer) Close() error {
	s.mu.Lock()
	err := s.store.Save(s.baselines)
	s.mu.Unlock()

	if err != nil {
		return fmt.Errorf("failed to save baselines on close: %w", err)
	}
	return s.store.Close()
}

// IsShadow returns whether the scorer is in shadow mode.
func (s *M3LiteScorer) IsShadow() bool {
	return s.shadow
}

// distanceToBucket maps a Mahalanobis distance to a risk bucket.
func distanceToBucket(d float64) string {
	switch {
	case d >= ThresholdSevere:
		return BucketSevere
	case d >= ThresholdAnomalous:
		return BucketAnomalous
	case d >= ThresholdNotable:
		return BucketNotable
	case d >= ThresholdMinor:
		return BucketMinor
	default:
		return BucketNormal
	}
}

// BucketRiskPoints returns the risk points for a given bucket.
func BucketRiskPoints(bucket string) int {
	switch bucket {
	case BucketSevere:
		return RiskSevere
	case BucketAnomalous:
		return RiskAnomalous
	case BucketNotable:
		return RiskNotable
	case BucketMinor:
		return RiskMinor
	default:
		return RiskNormal
	}
}
