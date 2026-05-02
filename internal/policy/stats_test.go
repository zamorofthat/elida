package policy

import (
	"encoding/base64"
	"math"
	"strings"
	"testing"
)

func TestPoissonSurvival_KnownValues(t *testing.T) {
	// For Poisson(lambda=5), P(X >= 10) should be approximately 0.0318
	p := poissonSurvival(5.0, 10)
	if math.Abs(p-0.0318) > 0.005 {
		t.Errorf("poissonSurvival(5, 10) = %f, want ~0.0318", p)
	}

	// For Poisson(lambda=10), P(X >= 5) should be high (~0.9707)
	p = poissonSurvival(10.0, 5)
	if p < 0.95 || p > 0.99 {
		t.Errorf("poissonSurvival(10, 5) = %f, want ~0.97", p)
	}

	// For Poisson(lambda=1), P(X >= 0) = 1.0
	p = poissonSurvival(1.0, 0)
	if p != 1.0 {
		t.Errorf("poissonSurvival(1, 0) = %f, want 1.0", p)
	}

	// For Poisson(lambda=1), P(X >= 1) = 1 - e^-1 ≈ 0.6321
	p = poissonSurvival(1.0, 1)
	if math.Abs(p-0.6321) > 0.005 {
		t.Errorf("poissonSurvival(1, 1) = %f, want ~0.6321", p)
	}
}

func TestPoissonSurvival_ZeroLambda(t *testing.T) {
	// With lambda=0, P(X >= 0) = 1.0
	if p := poissonSurvival(0, 0); p != 1.0 {
		t.Errorf("poissonSurvival(0, 0) = %f, want 1.0", p)
	}
	// With lambda=0, P(X >= 1) = 0.0
	if p := poissonSurvival(0, 1); p != 0.0 {
		t.Errorf("poissonSurvival(0, 1) = %f, want 0.0", p)
	}
}

func TestPoissonSurvival_LargeLambda(t *testing.T) {
	// Should not overflow or panic for large values
	p := poissonSurvival(100.0, 200)
	if p < 0 || p > 1 || math.IsNaN(p) || math.IsInf(p, 0) {
		t.Errorf("poissonSurvival(100, 200) = %f, expected valid probability", p)
	}
	// k=200 is way above lambda=100, so survival should be very small
	if p > 0.01 {
		t.Errorf("poissonSurvival(100, 200) = %f, expected very small value", p)
	}
}

func TestShannonEntropy_Uniform(t *testing.T) {
	// All same byte → entropy = 0
	data := bytes(200, 'A')
	e := shannonEntropy(data)
	if e != 0.0 {
		t.Errorf("shannonEntropy(uniform) = %f, want 0.0", e)
	}
}

func TestShannonEntropy_English(t *testing.T) {
	text := []byte("The quick brown fox jumps over the lazy dog. " +
		"This is a sample of natural English text that should have moderate entropy. " +
		"We expect values in the range of about four to five bits per byte.")
	e := shannonEntropy(text)
	if e < 3.5 || e > 5.0 {
		t.Errorf("shannonEntropy(english) = %f, want 3.5-5.0", e)
	}
}

func TestShannonEntropy_Base64(t *testing.T) {
	// Generate base64 content from repetitive source to get predictable entropy
	raw := strings.Repeat("Hello World! This is test data for base64 encoding. ", 10)
	b64 := []byte(base64.StdEncoding.EncodeToString([]byte(raw)))
	e := shannonEntropy(b64)
	if e < 5.0 || e > 6.5 {
		t.Errorf("shannonEntropy(base64) = %f, want 5.0-6.5", e)
	}
}

func TestShannonEntropy_Empty(t *testing.T) {
	if e := shannonEntropy(nil); e != 0.0 {
		t.Errorf("shannonEntropy(nil) = %f, want 0.0", e)
	}
	if e := shannonEntropy([]byte{}); e != 0.0 {
		t.Errorf("shannonEntropy(empty) = %f, want 0.0", e)
	}
}

func TestShannonEntropy_MaxEntropy(t *testing.T) {
	// All 256 byte values equally distributed → max entropy = 8.0
	data := make([]byte, 256*100)
	for i := range data {
		data[i] = byte(i % 256)
	}
	e := shannonEntropy(data)
	if math.Abs(e-8.0) > 0.01 {
		t.Errorf("shannonEntropy(all_bytes) = %f, want ~8.0", e)
	}
}

// bytes creates a slice of n copies of b
func bytes(n int, b byte) []byte {
	data := make([]byte, n)
	for i := range data {
		data[i] = b
	}
	return data
}
