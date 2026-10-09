package embedded

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestFixtureDigestsAreCurrent(t *testing.T) {
	dir := filepath.Join("testdata", "good")
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	for name, want := range m.Files {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		sum := sha256.Sum256(b)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("fixture %s digest is stale:\n  manifest: %s\n  actual:   %s\nupdate testdata/good/manifest.json", name, want, got)
		}
	}
}

func TestSortStrings(t *testing.T) {
	values := []string{"tokenizer.json", "config.json", "model.onnx"}
	sortStrings(values)
	want := []string{"config.json", "model.onnx", "tokenizer.json"}
	for i := range want {
		if values[i] != want[i] {
			t.Fatalf("sortStrings = %v, want %v", values, want)
		}
	}
}
