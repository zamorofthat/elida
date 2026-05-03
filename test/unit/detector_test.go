package unit

import (
	"testing"
	"time"

	"elida/internal/policy"
)

func TestSessionDetector_Warmup(t *testing.T) {
	det := policy.NewSessionDetector(policy.CompoundAnomalyConfig{})
	now := time.Now()

	for i := 0; i < policy.DefaultWarmupRequests; i++ {
		score := det.Update(now.Add(time.Duration(i)*100*time.Millisecond), []byte("data"))
		if score != 0 {
			t.Errorf("request %d during warmup: score=%f, want 0", i, score)
		}
	}
}

func TestSessionDetector_SteadyRate_NoAlarm(t *testing.T) {
	det := policy.NewSessionDetector(policy.CompoundAnomalyConfig{})
	now := time.Now()

	lowEntropy := []byte(`{"role":"user","content":"normal request"}`)
	for i := 0; i < 40; i++ {
		score := det.Update(now.Add(time.Duration(i)*500*time.Millisecond), lowEntropy)
		if score > policy.DefaultCompoundThreshold {
			t.Errorf("steady rate should not alarm: request %d, score=%f", i, score)
		}
	}
}

func TestSessionDetector_BurstLowEntropy_NoAlarm(t *testing.T) {
	det := policy.NewSessionDetector(policy.CompoundAnomalyConfig{})
	now := time.Now()

	lowEntropy := []byte(`{"tool":"read_file","path":"/src/main.go"}`)
	for i := 0; i < 10; i++ {
		det.Update(now.Add(time.Duration(i)*time.Second), lowEntropy)
	}

	burstStart := now.Add(10 * time.Second)
	for i := 0; i < 30; i++ {
		score := det.Update(burstStart.Add(time.Duration(i)*100*time.Millisecond), lowEntropy)
		if score > policy.DefaultCompoundThreshold {
			t.Errorf("burst with low entropy should not alarm: request %d, score=%f", i+10, score)
		}
	}
}

func TestSessionDetector_BurstHighEntropy_Alarms(t *testing.T) {
	det := policy.NewSessionDetector(policy.CompoundAnomalyConfig{})
	now := time.Now()

	lowEntropy := []byte(`{"tool":"read_file","path":"/src/main.go"}`)
	for i := 0; i < 10; i++ {
		det.Update(now.Add(time.Duration(i)*time.Second), lowEntropy)
	}

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
	det := policy.NewSessionDetector(policy.CompoundAnomalyConfig{})
	now := time.Now()

	for i := 0; i < 10; i++ {
		det.Update(now.Add(time.Duration(i)*100*time.Millisecond), []byte("data"))
	}

	// Gap longer than DefaultGapThreshold (2s)
	gapTime := now.Add(10*100*time.Millisecond + 3*time.Second)
	det.Update(gapTime, []byte("new burst"))

	if det.CUSUMHigh() != 0 {
		t.Errorf("CUSUM should reset after gap, got %f", det.CUSUMHigh())
	}
	if det.BurstCount() != 1 {
		t.Errorf("burst count should restart after gap, got %d", det.BurstCount())
	}
}

func TestSessionDetector_BurstHistory(t *testing.T) {
	det := policy.NewSessionDetector(policy.CompoundAnomalyConfig{})
	now := time.Now()

	offset := time.Duration(0)
	for burst := 0; burst < 3; burst++ {
		for i := 0; i < 8; i++ {
			det.Update(now.Add(offset), []byte("burst data"))
			offset += 100 * time.Millisecond
		}
		offset += 3 * time.Second
	}
	det.Update(now.Add(offset+3*time.Second), []byte("next"))

	if det.BurstHistoryLen() != 3 {
		t.Errorf("expected 3 bursts in history, got %d", det.BurstHistoryLen())
	}
}

func TestSessionDetector_IncrementalEntropy(t *testing.T) {
	det := policy.NewSessionDetector(policy.CompoundAnomalyConfig{})
	now := time.Now()

	// Feed uniform data via Update — should have low entropy
	uniform := make([]byte, 200)
	for i := range uniform {
		uniform[i] = 'A'
	}
	det.Update(now, uniform)
	if det.Entropy() != 0 {
		t.Errorf("uniform data entropy = %f, want 0", det.Entropy())
	}

	// New burst with diverse data
	diverse := make([]byte, 256)
	for i := range diverse {
		diverse[i] = byte(i)
	}
	det.Update(now.Add(3*time.Second), diverse) // gap triggers new burst + entropy reset
	if det.Entropy() < 7.9 {
		t.Errorf("all-byte data entropy = %f, want ~8.0", det.Entropy())
	}
}

func TestSessionDetector_ScoreComponents(t *testing.T) {
	det := policy.NewSessionDetector(policy.CompoundAnomalyConfig{})
	now := time.Now()

	for i := 0; i < 10; i++ {
		det.Update(now.Add(time.Duration(i)*500*time.Millisecond), []byte(`{"normal":"json"}`))
	}

	rate := det.RateScore()
	if rate > 0.5 {
		t.Errorf("steady rate should have low rate score, got %f", rate)
	}

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
		if got := policy.Clamp(tt.v, tt.lo, tt.hi); got != tt.want {
			t.Errorf("Clamp(%f, %f, %f) = %f, want %f", tt.v, tt.lo, tt.hi, got, tt.want)
		}
	}
}
