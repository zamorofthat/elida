package unit

import (
	"encoding/base64"
	"math"
	"strings"
	"testing"

	"elida/internal/policy"
)

func TestPoissonSurvival_KnownValues(t *testing.T) {
	// For Poisson(lambda=5), P(X >= 10) should be approximately 0.0318
	p := policy.PoissonSurvival(5.0, 10)
	if math.Abs(p-0.0318) > 0.005 {
		t.Errorf("PoissonSurvival(5, 10) = %f, want ~0.0318", p)
	}

	// For Poisson(lambda=10), P(X >= 5) should be high (~0.9707)
	p = policy.PoissonSurvival(10.0, 5)
	if p < 0.95 || p > 0.99 {
		t.Errorf("PoissonSurvival(10, 5) = %f, want ~0.97", p)
	}

	// For Poisson(lambda=1), P(X >= 0) = 1.0
	p = policy.PoissonSurvival(1.0, 0)
	if p != 1.0 {
		t.Errorf("PoissonSurvival(1, 0) = %f, want 1.0", p)
	}

	// For Poisson(lambda=1), P(X >= 1) = 1 - e^-1 ~ 0.6321
	p = policy.PoissonSurvival(1.0, 1)
	if math.Abs(p-0.6321) > 0.005 {
		t.Errorf("PoissonSurvival(1, 1) = %f, want ~0.6321", p)
	}
}

func TestPoissonSurvival_ZeroLambda(t *testing.T) {
	if p := policy.PoissonSurvival(0, 0); p != 1.0 {
		t.Errorf("PoissonSurvival(0, 0) = %f, want 1.0", p)
	}
	if p := policy.PoissonSurvival(0, 1); p != 0.0 {
		t.Errorf("PoissonSurvival(0, 1) = %f, want 0.0", p)
	}
}

func TestPoissonSurvival_LargeLambda(t *testing.T) {
	p := policy.PoissonSurvival(100.0, 200)
	if p < 0 || p > 1 || math.IsNaN(p) || math.IsInf(p, 0) {
		t.Errorf("PoissonSurvival(100, 200) = %f, expected valid probability", p)
	}
	if p > 0.01 {
		t.Errorf("PoissonSurvival(100, 200) = %f, expected very small value", p)
	}
}

func TestShannonEntropy_Uniform(t *testing.T) {
	data := make([]byte, 200)
	for i := range data {
		data[i] = 'A'
	}
	e := policy.ShannonEntropy(data)
	if e != 0.0 {
		t.Errorf("ShannonEntropy(uniform) = %f, want 0.0", e)
	}
}

func TestShannonEntropy_English(t *testing.T) {
	text := []byte("The quick brown fox jumps over the lazy dog. " +
		"This is a sample of natural English text that should have moderate entropy. " +
		"We expect values in the range of about four to five bits per byte.")
	e := policy.ShannonEntropy(text)
	if e < 3.5 || e > 5.0 {
		t.Errorf("ShannonEntropy(english) = %f, want 3.5-5.0", e)
	}
}

func TestShannonEntropy_Base64(t *testing.T) {
	raw := strings.Repeat("Hello World! This is test data for base64 encoding. ", 10)
	b64 := []byte(base64.StdEncoding.EncodeToString([]byte(raw)))
	e := policy.ShannonEntropy(b64)
	if e < 5.0 || e > 6.5 {
		t.Errorf("ShannonEntropy(base64) = %f, want 5.0-6.5", e)
	}
}

func TestShannonEntropy_Empty(t *testing.T) {
	if e := policy.ShannonEntropy(nil); e != 0.0 {
		t.Errorf("ShannonEntropy(nil) = %f, want 0.0", e)
	}
	if e := policy.ShannonEntropy([]byte{}); e != 0.0 {
		t.Errorf("ShannonEntropy(empty) = %f, want 0.0", e)
	}
}

func TestShannonEntropy_MaxEntropy(t *testing.T) {
	data := make([]byte, 256*100)
	for i := range data {
		data[i] = byte(i % 256)
	}
	e := policy.ShannonEntropy(data)
	if math.Abs(e-8.0) > 0.01 {
		t.Errorf("ShannonEntropy(all_bytes) = %f, want ~8.0", e)
	}
}
