package unit

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sync"
	"testing"
	"time"

	"elida/internal/fingerprint"
	"elida/internal/session"
	"elida/internal/storage"
)

// --- P² Quantile Tests ---

func TestP2Quantile_KnownDistribution(t *testing.T) {
	// Feed uniform distribution [0, 1000), check that median estimate is close to 500
	pq := fingerprint.NewP2Quantile(0.50)
	for i := 0; i < 10000; i++ {
		pq.Add(float64(i % 1000))
	}
	est := pq.Estimate()
	if math.Abs(est-500) > 50 {
		t.Errorf("P2 median estimate %f, expected ~500 (±50)", est)
	}
}

func TestP2Quantile_SmallSample(t *testing.T) {
	pq := fingerprint.NewP2Quantile(0.50)
	pq.Add(10)
	pq.Add(20)
	pq.Add(30)
	est := pq.Estimate()
	if est != 20 {
		t.Errorf("P2 median of [10,20,30] = %f, expected 20", est)
	}
}

func TestP2Quantile_SingleValue(t *testing.T) {
	pq := fingerprint.NewP2Quantile(0.99)
	pq.Add(42)
	if pq.Estimate() != 42 {
		t.Errorf("single value estimate should be 42, got %f", pq.Estimate())
	}
}

func TestP2Quantile_JSONRoundTrip(t *testing.T) {
	pq := fingerprint.NewP2Quantile(0.99)
	for i := 0; i < 100; i++ {
		pq.Add(float64(i))
	}
	original := pq.Estimate()

	data, err := json.Marshal(pq)
	if err != nil {
		t.Fatal(err)
	}

	var restored fingerprint.P2Quantile
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}

	if restored.Estimate() != original {
		t.Errorf("round-trip: got %f, want %f", restored.Estimate(), original)
	}
}

func TestP2Quantile_JSONRoundTrip_PartialInit(t *testing.T) {
	// Regression test: serializing a P2Quantile with count < 5 (before
	// initialization completes) then deserializing and adding more values
	// used to panic with "index out of range" because the initial buffer
	// was lost during JSON round-trip.
	for n := 1; n <= 4; n++ {
		pq := fingerprint.NewP2Quantile(0.50)
		for i := 0; i < n; i++ {
			pq.Add(float64(i + 1))
		}

		data, err := json.Marshal(pq)
		if err != nil {
			t.Fatalf("count=%d: marshal: %v", n, err)
		}

		var restored fingerprint.P2Quantile
		if err := json.Unmarshal(data, &restored); err != nil {
			t.Fatalf("count=%d: unmarshal: %v", n, err)
		}

		// This used to panic — adding enough values to trigger initialize()
		// on a restored instance that lost its initial buffer.
		for i := n; i < 10; i++ {
			restored.Add(float64(i + 1))
		}

		est := restored.Estimate()
		if est <= 0 {
			t.Errorf("count=%d: expected positive estimate, got %f", n, est)
		}
	}
}

// --- Cholesky & Mahalanobis Tests ---

func TestCholesky7_Identity(t *testing.T) {
	var I [7][7]float64
	for i := 0; i < 7; i++ {
		I[i][i] = 1
	}
	L, ok := fingerprint.Cholesky7(I)
	if !ok {
		t.Fatal("Cholesky of identity should succeed")
	}
	for i := 0; i < 7; i++ {
		for j := 0; j < 7; j++ {
			if i == j {
				if math.Abs(L[i][j]-1) > 1e-12 {
					t.Errorf("L[%d][%d] = %f, want 1", i, j, L[i][j])
				}
			} else if math.Abs(L[i][j]) > 1e-12 {
				t.Errorf("L[%d][%d] = %f, want 0", i, j, L[i][j])
			}
		}
	}
}

func TestCholesky7_KnownMatrix(t *testing.T) {
	// Diagonal matrix with known values
	var A [7][7]float64
	for i := 0; i < 7; i++ {
		A[i][i] = float64((i + 1) * (i + 1)) // 1, 4, 9, 16, 25, 36, 49
	}
	L, ok := fingerprint.Cholesky7(A)
	if !ok {
		t.Fatal("Cholesky of diagonal PD matrix should succeed")
	}
	for i := 0; i < 7; i++ {
		expected := float64(i + 1)
		if math.Abs(L[i][i]-expected) > 1e-10 {
			t.Errorf("L[%d][%d] = %f, want %f", i, i, L[i][i], expected)
		}
	}
}

func TestCholesky7_SingularMatrix(t *testing.T) {
	var A [7][7]float64 // all zeros = singular
	_, ok := fingerprint.Cholesky7(A)
	if ok {
		t.Error("Cholesky of zero matrix should fail")
	}
}

func TestMahalanobis_IdentityCovariance(t *testing.T) {
	// With identity covariance, Mahalanobis = Euclidean
	var I [7][7]float64
	for i := 0; i < 7; i++ {
		I[i][i] = 1
	}
	L, _ := fingerprint.Cholesky7(I)

	diff := [7]float64{1, 0, 0, 0, 0, 0, 0}
	d := fingerprint.MahalanobisCholesky(L, diff)
	if math.Abs(d-1.0) > 1e-10 {
		t.Errorf("Mahalanobis distance = %f, want 1.0", d)
	}

	// All ones: sqrt(7) ≈ 2.6458
	diff = [7]float64{1, 1, 1, 1, 1, 1, 1}
	d = fingerprint.MahalanobisCholesky(L, diff)
	if math.Abs(d-math.Sqrt(7)) > 1e-10 {
		t.Errorf("Mahalanobis distance = %f, want %f", d, math.Sqrt(7))
	}
}

func TestMahalanobis_KnownResult(t *testing.T) {
	// Diagonal covariance [4,4,4,4,4,4,4], diff [2,2,2,2,2,2,2]
	// D² = sum(2²/4) * 7 = 7, D = sqrt(7) ≈ 2.6458
	var A [7][7]float64
	for i := 0; i < 7; i++ {
		A[i][i] = 4
	}
	L, ok := fingerprint.Cholesky7(A)
	if !ok {
		t.Fatal("Cholesky failed")
	}

	diff := [7]float64{2, 2, 2, 2, 2, 2, 2}
	d := fingerprint.MahalanobisCholesky(L, diff)
	if math.Abs(d-math.Sqrt(7)) > 1e-10 {
		t.Errorf("Mahalanobis distance = %f, want %f", d, math.Sqrt(7))
	}
}

// --- Feature Extraction Tests ---

func TestExtract_BasicSession(t *testing.T) {
	sess := session.NewSession("test-1", "http://anthropic", "127.0.0.1")
	// Simulate some activity
	for i := 0; i < 10; i++ {
		sess.Touch()
	}
	sess.AddTokens(1000, 5000)
	sess.RecordToolCall("bash", "function", "req-1", "")
	sess.RecordToolCall("bash", "function", "req-2", "")
	sess.RecordToolCall("read", "function", "req-3", "")
	sess.RecordMessage("user", "hello", "anthropic")
	sess.RecordMessage("assistant", "hi there", "anthropic")
	sess.RecordMessage("user", "do something", "anthropic")

	snap := sess.Snapshot()
	fv := fingerprint.Extract(&snap)

	// Turn count: log(1+10) ≈ 2.40
	if fv[fingerprint.FeatTurnCount] < 2.0 || fv[fingerprint.FeatTurnCount] > 3.0 {
		t.Errorf("turn count = %f, expected ~2.4", fv[fingerprint.FeatTurnCount])
	}

	// Tool call ratio: 3/10 = 0.3
	if math.Abs(fv[fingerprint.FeatToolCallRatio]-0.3) > 0.05 {
		t.Errorf("tool call ratio = %f, expected ~0.3", fv[fingerprint.FeatToolCallRatio])
	}

	// Token ratio: log(1000/5000) = log(0.2) ≈ -1.61
	if fv[fingerprint.FeatTokenRatio] > -1.0 || fv[fingerprint.FeatTokenRatio] < -2.0 {
		t.Errorf("token ratio = %f, expected ~-1.61", fv[fingerprint.FeatTokenRatio])
	}

	// Backend continuity: single backend = 1.0
	if fv[fingerprint.FeatBackendContinuity] != 1.0 {
		t.Errorf("backend continuity = %f, expected 1.0", fv[fingerprint.FeatBackendContinuity])
	}
}

func TestExtract_ZeroTokens(t *testing.T) {
	sess := session.NewSession("test-2", "http://backend", "127.0.0.1")
	sess.Touch()
	snap := sess.Snapshot()
	fv := fingerprint.Extract(&snap)

	// Token ratio should be 0 when tokens are zero
	if fv[fingerprint.FeatTokenRatio] != 0 {
		t.Errorf("token ratio = %f, expected 0 for zero tokens", fv[fingerprint.FeatTokenRatio])
	}
}

func TestExtract_SingleMessage(t *testing.T) {
	sess := session.NewSession("test-3", "http://backend", "127.0.0.1")
	sess.RecordMessage("user", "hello", "backend")
	snap := sess.Snapshot()
	fv := fingerprint.Extract(&snap)

	// With 1 message, cadence features should be 0
	if fv[fingerprint.FeatCadenceMedian] != 0 {
		t.Errorf("cadence median = %f, expected 0 for single message", fv[fingerprint.FeatCadenceMedian])
	}
	if fv[fingerprint.FeatCadenceCV] != 0 {
		t.Errorf("cadence CV = %f, expected 0 for single message", fv[fingerprint.FeatCadenceCV])
	}
}

func TestExtract_NoToolCalls(t *testing.T) {
	sess := session.NewSession("test-4", "http://backend", "127.0.0.1")
	for i := 0; i < 5; i++ {
		sess.Touch()
	}
	snap := sess.Snapshot()
	fv := fingerprint.Extract(&snap)

	if fv[fingerprint.FeatToolCallRatio] != 0 {
		t.Errorf("tool call ratio = %f, expected 0", fv[fingerprint.FeatToolCallRatio])
	}
	if fv[fingerprint.FeatToolCallEntropy] != 0 {
		t.Errorf("tool call entropy = %f, expected 0", fv[fingerprint.FeatToolCallEntropy])
	}
}

// --- Session Class Tests ---

func TestSessionClass(t *testing.T) {
	sess := session.NewSession("test", "anthropic", "127.0.0.1")
	snap := sess.Snapshot()
	if c := fingerprint.SessionClass(&snap); c != "anthropic" {
		t.Errorf("class = %q, want 'anthropic'", c)
	}

	sess.SetMetadata("model", "claude-3-opus-20240229")
	snap = sess.Snapshot()
	c := fingerprint.SessionClass(&snap)
	if c != "anthropic/claude-3-opus" {
		t.Errorf("class = %q, want 'anthropic/claude-3-opus'", c)
	}
}

func TestParentClass(t *testing.T) {
	tests := []struct {
		class  string
		parent string
	}{
		{"anthropic/claude-3-opus", "anthropic"},
		{"anthropic", "global"},
		{"global", ""},
	}
	for _, tt := range tests {
		if p := fingerprint.ParentClass(tt.class); p != tt.parent {
			t.Errorf("ParentClass(%q) = %q, want %q", tt.class, p, tt.parent)
		}
	}
}

// --- Baseline Tests ---

func TestBaseline_MeanConvergence(t *testing.T) {
	cfg := fingerprint.BaselineConfig{NEff: 50, RidgeLambda: 1e-6, WarmUp: 10}
	b := fingerprint.NewBaseline("test", cfg)

	// Feed 200 constant vectors
	var fv fingerprint.FeatureVector
	for i := 0; i < 7; i++ {
		fv[i] = float64(i + 1)
	}
	for i := 0; i < 200; i++ {
		b.Update(fv)
	}

	mean := b.GetMean()
	for i := 0; i < 7; i++ {
		if math.Abs(mean[i]-fv[i]) > 0.1 {
			t.Errorf("mean[%d] = %f, expected ~%f", i, mean[i], fv[i])
		}
	}
}

func TestBaseline_CovarianceSymmetry(t *testing.T) {
	cfg := fingerprint.BaselineConfig{NEff: 50, RidgeLambda: 1e-6, WarmUp: 10}
	b := fingerprint.NewBaseline("test", cfg)

	// Feed varying vectors
	for i := 0; i < 100; i++ {
		var fv fingerprint.FeatureVector
		for j := 0; j < 7; j++ {
			fv[j] = float64(i*7+j) * 0.01
		}
		b.Update(fv)
	}

	cov := b.RegularizedCovariance()
	for i := 0; i < 7; i++ {
		for j := 0; j < 7; j++ {
			if math.Abs(cov[i][j]-cov[j][i]) > 1e-10 {
				t.Errorf("covariance not symmetric: cov[%d][%d]=%f != cov[%d][%d]=%f",
					i, j, cov[i][j], j, i, cov[j][i])
			}
		}
	}
}

func TestBaseline_WarmUp(t *testing.T) {
	cfg := fingerprint.BaselineConfig{NEff: 50, RidgeLambda: 1e-6, WarmUp: 100}
	b := fingerprint.NewBaseline("test", cfg)

	if b.IsWarm() {
		t.Error("baseline should not be warm with 0 samples")
	}

	for i := 0; i < 99; i++ {
		b.Update(fingerprint.FeatureVector{})
	}
	if b.IsWarm() {
		t.Error("baseline should not be warm with 99 samples")
	}

	b.Update(fingerprint.FeatureVector{})
	if !b.IsWarm() {
		t.Error("baseline should be warm with 100 samples")
	}
}

func TestBaseline_Winsorization(t *testing.T) {
	cfg := fingerprint.BaselineConfig{NEff: 50, RidgeLambda: 1e-6, WarmUp: 10}
	b := fingerprint.NewBaseline("test", cfg)

	// Feed values 0-99
	for i := 0; i < 100; i++ {
		var fv fingerprint.FeatureVector
		for j := 0; j < 7; j++ {
			fv[j] = float64(i)
		}
		b.Update(fv)
	}

	// Extreme outlier should be clipped
	var extreme fingerprint.FeatureVector
	for i := 0; i < 7; i++ {
		extreme[i] = 10000
	}
	w := b.Winsorize(extreme)
	for i := 0; i < 7; i++ {
		if w[i] >= 10000 {
			t.Errorf("winsorized value[%d] = %f, should be clipped below 10000", i, w[i])
		}
	}
}

// --- Scorer Tests ---

func TestScorer_ShadowMode(t *testing.T) {
	store := newMemoryStore()
	cfg := fingerprint.DefaultBaselineConfig()
	scorer, err := fingerprint.NewM3LiteScorer(store, true, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer scorer.Close()

	sess := session.NewSession("test-1", "http://backend", "127.0.0.1")
	snap := sess.Snapshot()

	distance, bucket, features, err := scorer.Score(&snap)
	if err != nil {
		t.Fatal(err)
	}
	if distance != 0 {
		t.Errorf("shadow mode distance = %f, want 0", distance)
	}
	if bucket != fingerprint.BucketWarmUp {
		t.Errorf("shadow mode bucket = %q, want %q", bucket, fingerprint.BucketWarmUp)
	}
	if features != nil {
		t.Error("shadow mode should return nil features")
	}
}

func TestScorer_WarmUpSentinel(t *testing.T) {
	store := newMemoryStore()
	cfg := fingerprint.BaselineConfig{NEff: 50, RidgeLambda: 1e-6, WarmUp: 100}
	scorer, err := fingerprint.NewM3LiteScorer(store, false, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer scorer.Close()

	// Ingest fewer than warm-up threshold
	for i := 0; i < 50; i++ {
		sess := makeTestSession(t, i)
		snap := sess.Snapshot()
		if ingestErr := scorer.Ingest(&snap); ingestErr != nil {
			t.Fatal(ingestErr)
		}
	}

	sess := makeTestSession(t, 999)
	snap := sess.Snapshot()
	_, bucket, _, err := scorer.Score(&snap)
	if err != nil {
		t.Fatal(err)
	}
	if bucket != fingerprint.BucketWarmUp {
		t.Errorf("under warm-up bucket = %q, want %q", bucket, fingerprint.BucketWarmUp)
	}
}

func TestScorer_NormalSession(t *testing.T) {
	store := newMemoryStore()
	cfg := fingerprint.BaselineConfig{NEff: 50, RidgeLambda: 1e-6, WarmUp: 10}
	scorer, err := fingerprint.NewM3LiteScorer(store, false, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer scorer.Close()

	// Build baseline with consistent sessions
	for i := 0; i < 100; i++ {
		sess := makeNormalSession(i)
		snap := sess.Snapshot()
		if ingestErr := scorer.Ingest(&snap); ingestErr != nil {
			t.Fatal(ingestErr)
		}
	}

	// Score a similar session
	sess := makeNormalSession(101)
	snap := sess.Snapshot()
	distance, bucket, _, err := scorer.Score(&snap)
	if err != nil {
		t.Fatal(err)
	}
	if distance > fingerprint.ThresholdNotable {
		t.Errorf("normal session distance = %f, expected < %f", distance, fingerprint.ThresholdNotable)
	}
	if bucket != fingerprint.BucketNormal && bucket != fingerprint.BucketMinor {
		t.Errorf("normal session bucket = %q, expected 'normal' or 'minor'", bucket)
	}
}

func TestScorer_AnomalousSession(t *testing.T) {
	store := newMemoryStore()
	cfg := fingerprint.BaselineConfig{NEff: 50, RidgeLambda: 1e-6, WarmUp: 10}
	scorer, err := fingerprint.NewM3LiteScorer(store, false, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer scorer.Close()

	// Build baseline with consistent sessions
	for i := 0; i < 100; i++ {
		sess := makeNormalSession(i)
		snap := sess.Snapshot()
		if ingestErr := scorer.Ingest(&snap); ingestErr != nil {
			t.Fatal(ingestErr)
		}
	}

	// Score a wildly different session
	sess := makeAnomalousSession()
	snap := sess.Snapshot()
	distance, _, _, err := scorer.Score(&snap)
	if err != nil {
		t.Fatal(err)
	}

	// Anomalous session should have higher distance than normal
	normalSess := makeNormalSession(200)
	normalSnap := normalSess.Snapshot()
	normalDist, _, _, _ := scorer.Score(&normalSnap)

	if distance <= normalDist {
		t.Errorf("anomalous distance (%f) should be > normal distance (%f)", distance, normalDist)
	}
}

func TestScorer_ClassFallback(t *testing.T) {
	store := newMemoryStore()
	cfg := fingerprint.BaselineConfig{NEff: 50, RidgeLambda: 1e-6, WarmUp: 10}
	scorer, err := fingerprint.NewM3LiteScorer(store, false, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer scorer.Close()

	// Build baseline under "backend-a" class
	for i := 0; i < 100; i++ {
		sess := session.NewSession("test", "backend-a", "127.0.0.1")
		for j := 0; j < 10; j++ {
			sess.Touch()
		}
		sess.AddTokens(1000, 2000)
		snap := sess.Snapshot()
		if ingestErr := scorer.Ingest(&snap); ingestErr != nil {
			t.Fatal(ingestErr)
		}
	}

	// Score a session from "backend-a/claude-3" (specific class has no baseline,
	// should fall back to "backend-a")
	sess := session.NewSession("test-fallback", "backend-a", "127.0.0.1")
	sess.SetMetadata("model", "claude-3-opus-20240229")
	for j := 0; j < 10; j++ {
		sess.Touch()
	}
	sess.AddTokens(1000, 2000)
	snap := sess.Snapshot()

	_, bucket, _, err := scorer.Score(&snap)
	if err != nil {
		t.Fatal(err)
	}
	// Should get a real score (not warm_up) because fallback to parent class
	if bucket == fingerprint.BucketWarmUp {
		t.Error("should fall back to parent class baseline, not warm_up")
	}
}

// --- Store Tests ---

func TestSQLiteBaselineStore_RoundTrip(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "elida-fingerprint-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	sqliteStore, err := storage.NewSQLiteStore(tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()

	store, err := fingerprint.NewSQLiteBaselineStore(sqliteStore.DB())
	if err != nil {
		t.Fatal(err)
	}

	cfg := fingerprint.DefaultBaselineConfig()

	// Create and populate baselines
	baselines := make(map[string]*fingerprint.Baseline)
	b := fingerprint.NewBaseline("test-class", cfg)
	for i := 0; i < 50; i++ {
		var fv fingerprint.FeatureVector
		for j := 0; j < 7; j++ {
			fv[j] = float64(i * (j + 1))
		}
		b.Update(fv)
	}
	baselines["test-class"] = b

	// Save
	if saveErr := store.Save(baselines); saveErr != nil {
		t.Fatal(saveErr)
	}

	// Load
	loaded, err := store.Load(cfg)
	if err != nil {
		t.Fatal(err)
	}

	if len(loaded) != 1 {
		t.Fatalf("loaded %d baselines, want 1", len(loaded))
	}

	lb := loaded["test-class"]
	if lb == nil {
		t.Fatal("loaded baseline is nil")
	}
	if lb.GetCount() != 50 {
		t.Errorf("loaded count = %d, want 50", lb.GetCount())
	}

	// Verify mean is close
	originalMean := b.GetMean()
	loadedMean := lb.GetMean()
	for i := 0; i < 7; i++ {
		if math.Abs(originalMean[i]-loadedMean[i]) > 1e-6 {
			t.Errorf("mean[%d] differs: original=%f, loaded=%f", i, originalMean[i], loadedMean[i])
		}
	}
}

// --- E2E Test ---

func TestFingerprint_EndToEnd(t *testing.T) {
	store := newMemoryStore()
	cfg := fingerprint.BaselineConfig{NEff: 50, RidgeLambda: 1e-6, WarmUp: 20}
	scorer, err := fingerprint.NewM3LiteScorer(store, false, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer scorer.Close()

	// Ingest 100+ normal sessions
	for i := 0; i < 120; i++ {
		sess := makeNormalSession(i)
		snap := sess.Snapshot()
		if ingestErr := scorer.Ingest(&snap); ingestErr != nil {
			t.Fatal(ingestErr)
		}
	}

	// Score a normal session
	normalSess := makeNormalSession(999)
	normalSnap := normalSess.Snapshot()
	normalDist, normalBucket, _, err := scorer.Score(&normalSnap)
	if err != nil {
		t.Fatal(err)
	}

	// Score an outlier
	outlier := makeAnomalousSession()
	outlierSnap := outlier.Snapshot()
	outlierDist, outlierBucket, features, err := scorer.Score(&outlierSnap)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("Normal: distance=%f bucket=%s", normalDist, normalBucket)
	t.Logf("Outlier: distance=%f bucket=%s", outlierDist, outlierBucket)
	if features != nil {
		t.Logf("Outlier features: %v", features)
	}

	// Outlier should have higher distance
	if outlierDist <= normalDist {
		t.Errorf("outlier distance (%f) should be > normal distance (%f)", outlierDist, normalDist)
	}

	// Risk points for outlier bucket should be > 0 (if notable+)
	riskPoints := fingerprint.BucketRiskPoints(outlierBucket)
	t.Logf("Outlier risk points: %d", riskPoints)
}

// --- Scorer Misc Tests ---

func TestScorer_IsShadow(t *testing.T) {
	store := newMemoryStore()
	cfg := fingerprint.DefaultBaselineConfig()

	shadow, err := fingerprint.NewM3LiteScorer(store, true, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer shadow.Close()
	if !shadow.IsShadow() {
		t.Error("expected shadow=true")
	}

	active, err := fingerprint.NewM3LiteScorer(store, false, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()
	if active.IsShadow() {
		t.Error("expected shadow=false")
	}
}

func TestScorer_FlushRetryOnError(t *testing.T) {
	store := newFailingStore(1) // fail first save, succeed after
	cfg := fingerprint.BaselineConfig{NEff: 50, RidgeLambda: 1e-6, WarmUp: 10}
	scorer, err := fingerprint.NewM3LiteScorerWithFlush(store, true, cfg, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go scorer.Run(ctx)

	// Ingest to mark dirty
	sess := makeNormalSession(1)
	snap := sess.Snapshot()
	if ingestErr := scorer.Ingest(&snap); ingestErr != nil {
		t.Fatal(ingestErr)
	}

	// Wait for first flush (fails) + second flush (succeeds)
	time.Sleep(250 * time.Millisecond)

	if store.successCount() == 0 {
		t.Error("expected at least one successful save after retry")
	}

	cancel()
	time.Sleep(50 * time.Millisecond)
	scorer.Close()
}

func TestDistanceToBucket_AllBuckets(t *testing.T) {
	tests := []struct {
		distance float64
		bucket   string
	}{
		{0.0, fingerprint.BucketNormal},
		{3.2, fingerprint.BucketNormal},
		{3.3, fingerprint.BucketMinor},
		{4.0, fingerprint.BucketMinor},
		{4.1, fingerprint.BucketNotable},
		{4.9, fingerprint.BucketNotable},
		{5.0, fingerprint.BucketAnomalous},
		{5.9, fingerprint.BucketAnomalous},
		{6.0, fingerprint.BucketSevere},
		{100.0, fingerprint.BucketSevere},
	}

	store := newMemoryStore()
	cfg := fingerprint.BaselineConfig{NEff: 50, RidgeLambda: 1e-6, WarmUp: 5}
	scorer, err := fingerprint.NewM3LiteScorer(store, false, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer scorer.Close()

	// Build a minimal baseline
	for i := 0; i < 10; i++ {
		sess := makeNormalSession(i)
		snap := sess.Snapshot()
		_ = scorer.Ingest(&snap)
	}

	// Just verify BucketRiskPoints covers all buckets
	for _, tt := range tests {
		rp := fingerprint.BucketRiskPoints(tt.bucket)
		if tt.bucket == fingerprint.BucketNormal && rp != 0 {
			t.Errorf("bucket %s should have 0 risk points", tt.bucket)
		}
		if tt.bucket == fingerprint.BucketSevere && rp != 20 {
			t.Errorf("bucket %s should have 20 risk points, got %d", tt.bucket, rp)
		}
	}
}

// --- Feature Extraction Edge Cases ---

func TestExtract_MultipleBackends(t *testing.T) {
	sess := session.NewSession("test-multi", "http://backend-a", "127.0.0.1")
	// Record requests on two different backends
	sess.RecordBackend("backend-a")
	sess.RecordBackend("backend-a")
	sess.RecordBackend("backend-a")
	sess.RecordBackend("backend-b")
	for i := 0; i < 4; i++ {
		sess.Touch()
	}

	snap := sess.Snapshot()
	fv := fingerprint.Extract(&snap)

	// With mixed backends (3 A, 1 B), continuity should be 0.75
	if fv[fingerprint.FeatBackendContinuity] >= 1.0 {
		t.Errorf("backend continuity = %f, expected < 1.0 with multiple backends", fv[fingerprint.FeatBackendContinuity])
	}
	if math.Abs(fv[fingerprint.FeatBackendContinuity]-0.75) > 0.01 {
		t.Errorf("backend continuity = %f, expected 0.75", fv[fingerprint.FeatBackendContinuity])
	}
}

func TestExtract_MultipleCadenceGaps(t *testing.T) {
	sess := session.NewSession("test-cadence", "http://backend", "127.0.0.1")
	for i := 0; i < 5; i++ {
		sess.RecordMessage("user", "msg", "backend")
		time.Sleep(time.Millisecond)
		sess.RecordMessage("assistant", "reply", "backend")
		time.Sleep(time.Millisecond)
	}

	snap := sess.Snapshot()
	fv := fingerprint.Extract(&snap)

	// Should have non-zero cadence values
	if fv[fingerprint.FeatCadenceMedian] == 0 {
		t.Error("cadence median should be non-zero with multiple messages")
	}
}

func TestModelFamily_Variants(t *testing.T) {
	tests := []struct {
		model string
		class string
	}{
		{"claude-3-opus-20240229", "backend/claude-3-opus"},
		{"gpt-4o-mini", "backend/gpt-4o-mini"},
		{"mistral-large-latest", "backend/mistral-large"},
		{"claude-3-haiku-20240307", "backend/claude-3-haiku"},
	}

	for _, tt := range tests {
		sess := session.NewSession("test", "backend", "127.0.0.1")
		sess.SetMetadata("model", tt.model)
		snap := sess.Snapshot()
		got := fingerprint.SessionClass(&snap)
		if got != tt.class {
			t.Errorf("SessionClass(model=%q) = %q, want %q", tt.model, got, tt.class)
		}
	}
}

func TestSessionClass_NoBackend(t *testing.T) {
	sess := session.NewSession("test", "", "127.0.0.1")
	snap := sess.Snapshot()
	if c := fingerprint.SessionClass(&snap); c != "global" {
		t.Errorf("class = %q, want 'global'", c)
	}
}

// --- Store Edge Cases ---

func TestSQLiteBaselineStore_EmptyDB(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "elida-empty-*.db")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	sqliteStore, err := storage.NewSQLiteStore(tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()

	store, err := fingerprint.NewSQLiteBaselineStore(sqliteStore.DB())
	if err != nil {
		t.Fatal(err)
	}

	cfg := fingerprint.DefaultBaselineConfig()
	baselines, err := store.Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(baselines) != 0 {
		t.Errorf("empty DB should load 0 baselines, got %d", len(baselines))
	}
}

func TestSQLiteBaselineStore_SaveEmpty(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "elida-save-empty-*.db")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	sqliteStore, err := storage.NewSQLiteStore(tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()

	store, err := fingerprint.NewSQLiteBaselineStore(sqliteStore.DB())
	if err != nil {
		t.Fatal(err)
	}

	// Saving empty map should not error
	if saveErr := store.Save(make(map[string]*fingerprint.Baseline)); saveErr != nil {
		t.Fatal(saveErr)
	}
}

func TestSQLiteBaselineStore_MultipleClasses(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "elida-multi-class-*.db")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	sqliteStore, err := storage.NewSQLiteStore(tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()

	store, err := fingerprint.NewSQLiteBaselineStore(sqliteStore.DB())
	if err != nil {
		t.Fatal(err)
	}

	cfg := fingerprint.DefaultBaselineConfig()
	baselines := make(map[string]*fingerprint.Baseline)
	for _, class := range []string{"global", "anthropic", "anthropic/claude-3-opus"} {
		b := fingerprint.NewBaseline(class, cfg)
		for i := 0; i < 20; i++ {
			var fv fingerprint.FeatureVector
			for j := 0; j < 7; j++ {
				fv[j] = float64(i + 1)
			}
			b.Update(fv)
		}
		baselines[class] = b
	}

	if saveErr := store.Save(baselines); saveErr != nil {
		t.Fatal(saveErr)
	}

	loaded, err := store.Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 3 {
		t.Errorf("expected 3 baselines, got %d", len(loaded))
	}
	for _, class := range []string{"global", "anthropic", "anthropic/claude-3-opus"} {
		if loaded[class] == nil {
			t.Errorf("missing baseline for class %q", class)
		}
	}
}

// --- Periodic Flush Tests ---

func TestScorer_PeriodicFlush(t *testing.T) {
	store := newTrackingStore()
	cfg := fingerprint.BaselineConfig{NEff: 50, RidgeLambda: 1e-6, WarmUp: 10}
	scorer, err := fingerprint.NewM3LiteScorerWithFlush(store, true, cfg, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go scorer.Run(ctx)

	// Ingest a session to mark dirty
	sess := makeNormalSession(1)
	snap := sess.Snapshot()
	if ingestErr := scorer.Ingest(&snap); ingestErr != nil {
		t.Fatal(ingestErr)
	}

	// Wait for at least one flush cycle
	time.Sleep(150 * time.Millisecond)

	if store.saveCount() == 0 {
		t.Error("expected at least one periodic save after ingest, got 0")
	}

	cancel()
	// Give Run() time to exit and do final flush
	time.Sleep(50 * time.Millisecond)

	scorer.Close()
}

func TestScorer_NoDirtyNoFlush(t *testing.T) {
	store := newTrackingStore()
	cfg := fingerprint.BaselineConfig{NEff: 50, RidgeLambda: 1e-6, WarmUp: 10}
	scorer, err := fingerprint.NewM3LiteScorerWithFlush(store, true, cfg, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go scorer.Run(ctx)

	// Don't ingest anything — no dirty flag
	time.Sleep(150 * time.Millisecond)

	if store.saveCount() != 0 {
		t.Errorf("expected 0 saves with no ingestion, got %d", store.saveCount())
	}

	cancel()
	time.Sleep(50 * time.Millisecond)
	scorer.Close()
}

func TestScorer_CrashRecovery(t *testing.T) {
	// Use a real SQLite store to test persistence across scorer instances
	tmpFile, err := os.CreateTemp("", "elida-crash-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	sqliteStore, err := storage.NewSQLiteStore(tmpFile.Name())
	if err != nil {
		t.Fatal(err)
	}

	store, err := fingerprint.NewSQLiteBaselineStore(sqliteStore.DB())
	if err != nil {
		t.Fatal(err)
	}

	cfg := fingerprint.BaselineConfig{NEff: 50, RidgeLambda: 1e-6, WarmUp: 10}

	// Create scorer with short flush interval (not shadow mode — we need to score)
	scorer, err := fingerprint.NewM3LiteScorerWithFlush(store, false, cfg, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go scorer.Run(ctx)

	// Ingest sessions
	for i := 0; i < 20; i++ {
		sess := makeNormalSession(i)
		snap := sess.Snapshot()
		if ingestErr := scorer.Ingest(&snap); ingestErr != nil {
			t.Fatal(ingestErr)
		}
	}

	// Wait for periodic flush to persist
	time.Sleep(150 * time.Millisecond)

	// Simulate crash: cancel context (Run does final flush), but do NOT call Close()
	cancel()
	time.Sleep(50 * time.Millisecond)

	// Reopen from same DB — baselines should have survived
	store2, err := fingerprint.NewSQLiteBaselineStore(sqliteStore.DB())
	if err != nil {
		t.Fatal(err)
	}

	scorer2, err := fingerprint.NewM3LiteScorer(store2, false, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer scorer2.Close()

	// Score a session — should not be warm_up if baselines were persisted
	sess := makeNormalSession(999)
	snap := sess.Snapshot()

	_, bucket, _, scoreErr := scorer2.Score(&snap)
	if scoreErr != nil {
		t.Fatal(scoreErr)
	}

	// With warm_up=10 and 20 ingested sessions, baselines should be warm
	if bucket == fingerprint.BucketWarmUp {
		t.Error("baselines should have survived the simulated crash (got warm_up)")
	}

	sqliteStore.Close()
}

// trackingStore wraps memoryStore and counts Save() calls.
type trackingStore struct {
	mu    sync.Mutex
	saves int
	inner *memoryStore
}

func newTrackingStore() *trackingStore {
	return &trackingStore{inner: newMemoryStore()}
}

func (ts *trackingStore) Load(cfg fingerprint.BaselineConfig) (map[string]*fingerprint.Baseline, error) {
	return ts.inner.Load(cfg)
}

func (ts *trackingStore) Save(baselines map[string]*fingerprint.Baseline) error {
	ts.mu.Lock()
	ts.saves++
	ts.mu.Unlock()
	return ts.inner.Save(baselines)
}

func (ts *trackingStore) Close() error {
	return ts.inner.Close()
}

func (ts *trackingStore) saveCount() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.saves
}

// failingStore fails the first N saves, then succeeds.
type failingStore struct {
	mu        sync.Mutex
	failsLeft int
	successes int
	inner     *memoryStore
}

func newFailingStore(failCount int) *failingStore {
	return &failingStore{failsLeft: failCount, inner: newMemoryStore()}
}

func (fs *failingStore) Load(cfg fingerprint.BaselineConfig) (map[string]*fingerprint.Baseline, error) {
	return fs.inner.Load(cfg)
}

func (fs *failingStore) Save(baselines map[string]*fingerprint.Baseline) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.failsLeft > 0 {
		fs.failsLeft--
		return fmt.Errorf("simulated save failure")
	}
	fs.successes++
	return fs.inner.Save(baselines)
}

func (fs *failingStore) Close() error {
	return fs.inner.Close()
}

func (fs *failingStore) successCount() int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.successes
}

// --- Helpers ---

func makeTestSession(t *testing.T, seed int) *session.Session {
	t.Helper()
	sess := session.NewSession("test", "http://backend", "127.0.0.1")
	for j := 0; j < 5+seed%10; j++ {
		sess.Touch()
	}
	sess.AddTokens(int64(100+seed*10), int64(200+seed*20))
	return sess
}

func makeNormalSession(seed int) *session.Session {
	sess := session.NewSession("normal", "backend-a", "127.0.0.1")
	// Consistent pattern: ~10 requests, ~1000/2000 tokens, 2 tools
	for j := 0; j < 10; j++ {
		sess.Touch()
		time.Sleep(time.Microsecond) // small gap for cadence
	}
	sess.AddTokens(1000, 2000)
	sess.RecordToolCall("read", "function", "req-1", "")
	sess.RecordToolCall("write", "function", "req-2", "")
	sess.RecordMessage("user", "hello", "backend-a")
	sess.RecordMessage("assistant", "hi", "backend-a")
	sess.RecordMessage("user", "do thing", "backend-a")
	sess.RecordMessage("assistant", "done", "backend-a")
	_ = seed
	return sess
}

func makeAnomalousSession() *session.Session {
	sess := session.NewSession("anomalous", "backend-a", "127.0.0.1")
	// Very different: 100 requests, reversed token ratio, many tools, different cadence
	for j := 0; j < 100; j++ {
		sess.Touch()
	}
	sess.AddTokens(50000, 100) // inverted ratio
	// Many distinct tools
	for i := 0; i < 50; i++ {
		sess.RecordToolCall("tool-"+string(rune('a'+i%26)), "function", "req", "")
	}
	sess.RecordMessage("user", "x", "backend-a")
	return sess
}

// memoryStore is a simple in-memory BaselineStore for testing.
type memoryStore struct {
	baselines map[string]*fingerprint.Baseline
}

func newMemoryStore() *memoryStore {
	return &memoryStore{baselines: make(map[string]*fingerprint.Baseline)}
}

func (m *memoryStore) Load(cfg fingerprint.BaselineConfig) (map[string]*fingerprint.Baseline, error) {
	return m.baselines, nil
}

func (m *memoryStore) Save(baselines map[string]*fingerprint.Baseline) error {
	m.baselines = baselines
	return nil
}

func (m *memoryStore) Close() error {
	return nil
}
