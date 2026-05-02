package policy

import (
	"math"
	"testing"
	"time"
)

func TestSessionDetector_Warmup(t *testing.T) {
	det := NewSessionDetector(CompoundAnomalyConfig{})
	now := time.Now()

	// First few requests during warmup should always return 0
	for i := 0; i < defaultWarmupRequests; i++ {
		score := det.Update(now.Add(time.Duration(i)*100*time.Millisecond), []byte("data"))
		if score != 0 {
			t.Errorf("request %d during warmup: score=%f, want 0", i, score)
		}
	}
}

func TestSessionDetector_SteadyRate_NoAlarm(t *testing.T) {
	det := NewSessionDetector(CompoundAnomalyConfig{})
	now := time.Now()

	// Steady rate: 2 req/s for 20 seconds with low-entropy content
	lowEntropy := []byte(`{"role":"user","content":"normal request"}`)
	for i := 0; i < 40; i++ {
		score := det.Update(now.Add(time.Duration(i)*500*time.Millisecond), lowEntropy)
		if score > defaultCompoundThreshold {
			t.Errorf("steady rate should not alarm: request %d, score=%f", i, score)
		}
	}
}

func TestSessionDetector_BurstLowEntropy_NoAlarm(t *testing.T) {
	det := NewSessionDetector(CompoundAnomalyConfig{})
	now := time.Now()

	// Phase 1: slow planning (1 req/s for 10s)
	lowEntropy := []byte(`{"tool":"read_file","path":"/src/main.go"}`)
	for i := 0; i < 10; i++ {
		det.Update(now.Add(time.Duration(i)*time.Second), lowEntropy)
	}

	// Phase 2: burst execution (10 req/s for 3s) — but still low entropy
	burstStart := now.Add(10 * time.Second)
	for i := 0; i < 30; i++ {
		score := det.Update(burstStart.Add(time.Duration(i)*100*time.Millisecond), lowEntropy)
		// Even with high rate, low entropy should keep compound score low
		if score > defaultCompoundThreshold {
			t.Errorf("burst with low entropy should not alarm: request %d, score=%f", i+10, score)
		}
	}
}

func TestSessionDetector_BurstHighEntropy_Alarms(t *testing.T) {
	det := NewSessionDetector(CompoundAnomalyConfig{})
	now := time.Now()

	// Phase 1: slow planning with normal content
	lowEntropy := []byte(`{"tool":"read_file","path":"/src/main.go"}`)
	for i := 0; i < 10; i++ {
		det.Update(now.Add(time.Duration(i)*time.Second), lowEntropy)
	}

	// Phase 2: burst with high-entropy content (simulated exfiltration)
	// Generate high-entropy bytes (pseudo-random)
	highEntropy := make([]byte, 200)
	for i := range highEntropy {
		highEntropy[i] = byte((i*7 + 13*i*i + 37) % 256)
	}

	burstStart := now.Add(10 * time.Second)
	var maxScore float64
	for i := 0; i < 30; i++ {
		score := det.Update(burstStart.Add(time.Duration(i)*50*time.Millisecond), highEntropy)
		if score > maxScore {
			maxScore = score
		}
	}

	if maxScore <= 0 {
		t.Errorf("burst with high entropy should produce positive compound score, got %f", maxScore)
	}
}

func TestSessionDetector_BurstBoundary_Resets(t *testing.T) {
	det := NewSessionDetector(CompoundAnomalyConfig{})
	now := time.Now()

	// First burst
	for i := 0; i < 10; i++ {
		det.Update(now.Add(time.Duration(i)*100*time.Millisecond), []byte("data"))
	}

	// Gap longer than gapThreshold (2s default)
	gapTime := now.Add(10*100*time.Millisecond + 3*time.Second)
	det.Update(gapTime, []byte("new burst"))

	// CUSUM should have been reset
	if det.cusumHigh != 0 {
		t.Errorf("CUSUM should reset after gap, got %f", det.cusumHigh)
	}
	// Burst count should restart
	if det.burstCount != 1 {
		t.Errorf("burst count should restart after gap, got %d", det.burstCount)
	}
}

func TestSessionDetector_BurstHistory(t *testing.T) {
	det := NewSessionDetector(CompoundAnomalyConfig{})
	now := time.Now()

	// Create 3 bursts with gaps between them
	offset := time.Duration(0)
	for burst := 0; burst < 3; burst++ {
		for i := 0; i < 8; i++ {
			det.Update(now.Add(offset), []byte("burst data"))
			offset += 100 * time.Millisecond
		}
		offset += 3 * time.Second // gap between bursts
	}
	// Finalize the last burst by triggering a new one
	det.Update(now.Add(offset+3*time.Second), []byte("next"))

	// Should have 3 completed bursts in history
	if det.burstFill != 3 {
		t.Errorf("expected 3 bursts in history, got %d", det.burstFill)
	}
}

func TestSessionDetector_IncrementalEntropy(t *testing.T) {
	det := NewSessionDetector(CompoundAnomalyConfig{})

	// Feed uniform data — should have 0 entropy
	uniform := make([]byte, 200)
	for i := range uniform {
		uniform[i] = 'A'
	}
	det.addBytes(uniform)
	if e := det.entropy(); e != 0 {
		t.Errorf("uniform data entropy = %f, want 0", e)
	}

	// Reset and feed diverse data
	det.resetEntropy()
	diverse := make([]byte, 256)
	for i := range diverse {
		diverse[i] = byte(i)
	}
	det.addBytes(diverse)
	if e := det.entropy(); math.Abs(e-8.0) > 0.01 {
		t.Errorf("all-byte data entropy = %f, want ~8.0", e)
	}
}

func TestSessionDetector_ScoreComponents(t *testing.T) {
	det := NewSessionDetector(CompoundAnomalyConfig{})
	now := time.Now()

	// Feed enough data to get past warmup with steady rate
	for i := 0; i < 10; i++ {
		det.Update(now.Add(time.Duration(i)*500*time.Millisecond), []byte(`{"normal":"json"}`))
	}

	// Rate score should be low for steady traffic
	rate := det.RateScore()
	if rate > 0.5 {
		t.Errorf("steady rate should have low rate score, got %f", rate)
	}

	// Entropy score should be 0 for structured JSON (entropy ~4.0)
	entropy := det.EntropyScore()
	if entropy > 0.1 {
		t.Errorf("structured JSON should have near-zero entropy score, got %f (H=%f)", entropy, det.Entropy())
	}
}

func TestClamp(t *testing.T) {
	tests := []struct {
		v, lo, hi, want float64
	}{
		{0.5, 0, 1, 0.5},
		{-1, 0, 1, 0},
		{2, 0, 1, 1},
		{0, 0, 0, 0},
	}
	for _, tt := range tests {
		if got := clamp(tt.v, tt.lo, tt.hi); got != tt.want {
			t.Errorf("clamp(%f, %f, %f) = %f, want %f", tt.v, tt.lo, tt.hi, got, tt.want)
		}
	}
}
