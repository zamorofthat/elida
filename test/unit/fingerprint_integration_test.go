package unit

import (
	"os"
	"testing"
	"time"

	"elida/internal/fingerprint"
	"elida/internal/session"
	"elida/internal/storage"
)

// TestFingerprintIntegration_FullPipeline exercises the complete M3-lite pipeline:
// SQLite store → baseline accumulation → scoring → persistence → reload.
// This is a self-contained test that doesn't need an external backend.
func TestFingerprintIntegration_FullPipeline(t *testing.T) {
	// --- Setup: real SQLite store ---
	tmpFile, err := os.CreateTemp("", "elida-fp-integration-*.db")
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

	baselineStore, err := fingerprint.NewSQLiteBaselineStore(sqliteStore.DB())
	if err != nil {
		t.Fatal(err)
	}

	cfg := fingerprint.BaselineConfig{
		NEff:        50,
		RidgeLambda: 1e-6,
		WarmUp:      10, // low for testing
	}

	scorer, err := fingerprint.NewM3LiteScorer(baselineStore, false, cfg)
	if err != nil {
		t.Fatal(err)
	}

	// --- Phase 1: Ingest normal sessions to build baseline ---
	t.Log("Phase 1: Ingesting 50 normal sessions...")
	for i := 0; i < 50; i++ {
		snap := buildRealisticSession(t, "anthropic", "claude-3-opus-20240229", i, false)
		if ingestErr := scorer.Ingest(snap); ingestErr != nil {
			t.Fatal(ingestErr)
		}
	}

	// --- Phase 2: Score a normal session ---
	t.Log("Phase 2: Scoring normal session...")
	normalSnap := buildRealisticSession(t, "anthropic", "claude-3-opus-20240229", 999, false)
	normalDist, normalBucket, normalFeatures, err := scorer.Score(normalSnap)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("  Normal: distance=%.2f bucket=%s", normalDist, normalBucket)
	if normalFeatures != nil {
		t.Logf("  Features: %v", normalFeatures)
	}

	// --- Phase 3: Score an anomalous session ---
	t.Log("Phase 3: Scoring anomalous session...")
	anomalousSnap := buildRealisticSession(t, "anthropic", "claude-3-opus-20240229", 999, true)
	anomDist, anomBucket, anomFeatures, err := scorer.Score(anomalousSnap)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("  Anomalous: distance=%.2f bucket=%s", anomDist, anomBucket)
	if anomFeatures != nil {
		t.Logf("  Features: %v", anomFeatures)
	}

	// Anomalous should score higher
	if anomDist <= normalDist {
		t.Errorf("anomalous distance (%.2f) should exceed normal (%.2f)", anomDist, normalDist)
	}

	// Risk points check
	normalRisk := fingerprint.BucketRiskPoints(normalBucket)
	anomRisk := fingerprint.BucketRiskPoints(anomBucket)
	t.Logf("  Risk points: normal=%d anomalous=%d", normalRisk, anomRisk)

	// --- Phase 4: Persist and reload ---
	t.Log("Phase 4: Persist baselines, close, reload...")
	if closeErr := scorer.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	// Reload from same DB
	baselineStore2, err := fingerprint.NewSQLiteBaselineStore(sqliteStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	scorer2, err := fingerprint.NewM3LiteScorer(baselineStore2, false, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer scorer2.Close()

	// Score same normal session — should get similar result
	normalDist2, normalBucket2, _, err := scorer2.Score(normalSnap)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("  After reload: distance=%.2f bucket=%s (was %.2f/%s)",
		normalDist2, normalBucket2, normalDist, normalBucket)

	// Distance should be close (not identical due to float precision in JSON round-trip)
	if normalDist2 == 0 && normalDist > 0 {
		t.Error("after reload, scorer returned 0 distance for previously scored session")
	}

	// --- Phase 5: Class fallback ---
	t.Log("Phase 5: Testing class fallback...")
	// Score a session from a different model family (no specific baseline, should fall back)
	fallbackSnap := buildRealisticSession(t, "anthropic", "claude-3-haiku-20240307", 42, false)
	fallbackDist, fallbackBucket, _, err := scorer2.Score(fallbackSnap)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("  Fallback (different model): distance=%.2f bucket=%s", fallbackDist, fallbackBucket)

	// Should get a real score via parent class fallback, not warm_up
	if fallbackBucket == fingerprint.BucketWarmUp {
		t.Error("expected fallback to parent class, got warm_up")
	}

	// --- Phase 6: Shadow mode ---
	t.Log("Phase 6: Testing shadow mode...")
	shadowStore, err := fingerprint.NewSQLiteBaselineStore(sqliteStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	shadowScorer, err := fingerprint.NewM3LiteScorer(shadowStore, true, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer shadowScorer.Close()

	shadowDist, shadowBucket, shadowFeatures, err := shadowScorer.Score(normalSnap)
	if err != nil {
		t.Fatal(err)
	}
	if shadowDist != 0 || shadowBucket != fingerprint.BucketWarmUp || shadowFeatures != nil {
		t.Errorf("shadow mode should return 0/warm_up/nil, got %.2f/%s/%v",
			shadowDist, shadowBucket, shadowFeatures)
	}

	// But ingest should still work
	if ingestErr := shadowScorer.Ingest(normalSnap); ingestErr != nil {
		t.Fatal(ingestErr)
	}
	t.Log("  Shadow mode: ingest OK, scoring skipped as expected")

	t.Log("All phases passed.")
}

// TestFingerprintIntegration_MultiClass verifies that different session classes
// build independent baselines.
func TestFingerprintIntegration_MultiClass(t *testing.T) {
	store := newMemoryStore()
	cfg := fingerprint.BaselineConfig{NEff: 50, RidgeLambda: 1e-6, WarmUp: 10}
	scorer, err := fingerprint.NewM3LiteScorer(store, false, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer scorer.Close()

	// Class A: low turn count, few tools
	for i := 0; i < 30; i++ {
		snap := buildRealisticSession(t, "backend-a", "", i, false)
		if ingestErr := scorer.Ingest(snap); ingestErr != nil {
			t.Fatal(ingestErr)
		}
	}

	// Class B: high turn count, many tools (different behavior pattern)
	for i := 0; i < 30; i++ {
		sess := session.NewSession("multi-b", "backend-b", "127.0.0.1")
		for j := 0; j < 50; j++ {
			sess.Touch()
			time.Sleep(time.Microsecond)
		}
		sess.AddTokens(5000, 500) // inverted ratio vs class A
		for j := 0; j < 20; j++ {
			sess.RecordToolCall("tool-"+string(rune('a'+j%10)), "function", "req")
		}
		sess.RecordMessage("user", "go", "backend-b")
		snap := sess.Snapshot()
		if ingestErr := scorer.Ingest(&snap); ingestErr != nil {
			t.Fatal(ingestErr)
		}
	}

	// A normal class-A session should score well against class A
	normalA := buildRealisticSession(t, "backend-a", "", 999, false)
	distA, bucketA, _, err := scorer.Score(normalA)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Class A normal: distance=%.2f bucket=%s", distA, bucketA)

	// A class-B-shaped session scored against class A's baseline should be anomalous
	// (We create a class-B-like session but force it into class A's backend)
	oddSess := session.NewSession("odd", "backend-a", "127.0.0.1")
	for j := 0; j < 50; j++ {
		oddSess.Touch()
	}
	oddSess.AddTokens(5000, 500) // class B's token pattern
	for j := 0; j < 20; j++ {
		oddSess.RecordToolCall("tool-"+string(rune('a'+j%10)), "function", "req")
	}
	oddSnap := oddSess.Snapshot()
	distOdd, bucketOdd, _, err := scorer.Score(&oddSnap)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Class B pattern on A: distance=%.2f bucket=%s", distOdd, bucketOdd)

	if distOdd <= distA {
		t.Errorf("mismatched class pattern (%.2f) should score higher than normal (%.2f)", distOdd, distA)
	}
}

// buildRealisticSession creates a session snapshot that mimics real LLM agent behavior.
func buildRealisticSession(t *testing.T, backend, model string, seed int, anomalous bool) *session.Session {
	t.Helper()

	sess := session.NewSession("fp-test", backend, "127.0.0.1")
	if model != "" {
		sess.SetMetadata("model", model)
	}

	if anomalous {
		// Anomalous: burst traffic, inverted token ratio, many tools
		for j := 0; j < 80; j++ {
			sess.Touch()
		}
		sess.AddTokens(30000, 200) // huge input, tiny output
		for j := 0; j < 40; j++ {
			sess.RecordToolCall("unusual-tool-"+string(rune('a'+j%26)), "function", "req")
		}
		sess.RecordMessage("user", "execute attack plan", backend)
		snap := sess.Snapshot()
		return &snap
	}

	// Normal: ~10 turns, balanced tokens, 2 tools, regular cadence
	for j := 0; j < 10; j++ {
		sess.Touch()
		time.Sleep(time.Microsecond) // small gap for cadence variance
	}
	sess.AddTokens(1000+int64(seed%200), 2000+int64(seed%400))
	sess.RecordToolCall("read_file", "function", "req-1")
	sess.RecordToolCall("write_file", "function", "req-2")
	if seed%3 == 0 {
		sess.RecordToolCall("search", "function", "req-3")
	}
	sess.RecordMessage("user", "help me with code", backend)
	sess.RecordMessage("assistant", "sure, here's the code", backend)
	sess.RecordMessage("user", "thanks", backend)
	sess.RecordMessage("assistant", "you're welcome", backend)

	snap := sess.Snapshot()
	return &snap
}
