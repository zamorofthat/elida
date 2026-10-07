package unit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elida/internal/decision"
	"elida/internal/decision/embedded"
)

func copyFixture(t *testing.T) string {
	t.Helper()
	src := filepath.Join("..", "..", "internal", "decision", "embedded", "testdata", "good")
	dst := t.TempDir()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("read fixture dir: %v", err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), b, 0o600); err != nil {
			t.Fatalf("write %s: %v", e.Name(), err)
		}
	}
	return dst
}

func rewriteManifest(t *testing.T, dir string, mutate func(m map[string]any)) {
	t.Helper()
	path := filepath.Join(dir, "manifest.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m map[string]any
	if unmarshalErr := json.Unmarshal(b, &m); unmarshalErr != nil {
		t.Fatalf("unmarshal manifest: %v", unmarshalErr)
	}
	mutate(m)
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

func TestEmbeddedLoad_GoodFixture(t *testing.T) {
	dir := copyFixture(t)
	m, err := embedded.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m.Name != "minilm-multihead" {
		t.Errorf("Name = %q", m.Name)
	}
	if len(m.HeadOrder) != 2 || m.HeadOrder[0] != "injection" || m.HeadOrder[1] != "human_directed" {
		t.Fatalf("HeadOrder = %v, want [injection human_directed]", m.HeadOrder)
	}
	if m.Calibration.Temperature != 2.41 {
		t.Errorf("Temperature = %v, want 2.41", m.Calibration.Temperature)
	}
	if m.Calibration.MainThreshold != 0.5 || m.Calibration.AuxThreshold != 0.64 {
		t.Errorf("thresholds = %v/%v, want the multi-head pair 0.5/0.64", m.Calibration.MainThreshold, m.Calibration.AuxThreshold)
	}
	if m.Calibration.SingleHeadThreshold != 0.4 {
		t.Errorf("SingleHeadThreshold = %v, want 0.4", m.Calibration.SingleHeadThreshold)
	}
	if m.Calibration.ThresholdSet != "v1" {
		t.Errorf("ThresholdSet = %q, want v1", m.Calibration.ThresholdSet)
	}
	if m.License != "Apache-2.0" {
		t.Errorf("License = %q", m.License)
	}
	if len(m.Signals) != 2 {
		t.Fatalf("Signals = %v", m.Signals)
	}
	if m.Signals[0] != decision.SignalInjection {
		t.Errorf("Signals[0] = %q", m.Signals[0])
	}
	cs := m.Checksum()
	if len(cs) != 64 {
		t.Fatalf("Checksum() = %q, want 64 hex chars", cs)
	}
	m2, _ := embedded.Load(dir)
	if m2.Checksum() != cs {
		t.Fatal("Checksum() is not stable across loads")
	}
}

func TestEmbeddedLoad_MissingManifest(t *testing.T) {
	dir := copyFixture(t)
	if err := os.Remove(filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	_, err := embedded.Load(dir)
	if !errors.Is(err, embedded.ErrManifestMissing) {
		t.Fatalf("Load error = %v, want ErrManifestMissing", err)
	}
}

func TestEmbeddedLoad_MissingDirectory(t *testing.T) {
	_, err := embedded.Load(filepath.Join(t.TempDir(), "nope"))
	if err == nil {
		t.Fatal("expected an error for a missing directory")
	}
}

func TestEmbeddedLoad_CorruptedModelFile(t *testing.T) {
	dir := copyFixture(t)
	if err := os.WriteFile(filepath.Join(dir, "model.onnx"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := embedded.Load(dir)
	if !errors.Is(err, embedded.ErrChecksumMismatch) {
		t.Fatalf("Load error = %v, want ErrChecksumMismatch", err)
	}
	if got := err.Error(); !containsAll(got, "model.onnx") {
		t.Fatalf("error %q does not name the offending file", got)
	}
}

func TestEmbeddedLoad_MissingListedFile(t *testing.T) {
	dir := copyFixture(t)
	if err := os.Remove(filepath.Join(dir, "tokenizer.json")); err != nil {
		t.Fatal(err)
	}
	_, err := embedded.Load(dir)
	if err == nil {
		t.Fatal("a file listed in the manifest but absent must fail verification")
	}
	if got := err.Error(); !containsAll(got, "tokenizer.json") {
		t.Fatalf("error %q does not name the missing file", got)
	}
}

func TestEmbeddedLoad_WrongHeadOrder(t *testing.T) {
	dir := copyFixture(t)
	rewriteManifest(t, dir, func(m map[string]any) {
		m["head_order"] = []any{"human_directed", "injection"}
	})
	fixDigests(t, dir)
	_, err := embedded.Load(dir)
	if !errors.Is(err, embedded.ErrHeadOrder) {
		t.Fatalf("Load error = %v, want ErrHeadOrder", err)
	}
}

func TestEmbeddedLoad_RejectsPathTraversalInFiles(t *testing.T) {
	dir := copyFixture(t)
	rewriteManifest(t, dir, func(m map[string]any) {
		files, ok := m["files"].(map[string]any)
		if !ok {
			t.Fatal("manifest files is not an object")
		}
		files["../../../etc/passwd"] = "0000000000000000000000000000000000000000000000000000000000000000"
	})
	fixDigests(t, dir)
	_, err := embedded.Load(dir)
	if err == nil {
		t.Fatal("a manifest entry escaping the model directory must be rejected")
	}
	// The entry would also fail a checksum comparison, so assert the
	// confinement error specifically.
	if !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("error must come from the confinement check, got: %v", err)
	}
}

func TestEmbeddedLoad_RequiresCalibrationAndLicense(t *testing.T) {
	for _, tc := range []struct {
		name  string
		drop  func(m map[string]any)
		field string
	}{
		{"no license", func(m map[string]any) { delete(m, "license") }, "license"},
		{"no name", func(m map[string]any) { delete(m, "name") }, "name"},
		{"no version", func(m map[string]any) { delete(m, "version") }, "version"},
		{"zero temperature", func(m map[string]any) {
			calibration, ok := m["calibration"].(map[string]any)
			if !ok {
				t.Fatal("manifest calibration is not an object")
			}
			calibration["temperature"] = 0.0
		}, "temperature"},
		{"no threshold set", func(m map[string]any) {
			calibration, ok := m["calibration"].(map[string]any)
			if !ok {
				t.Fatal("manifest calibration is not an object")
			}
			calibration["threshold_set"] = ""
		}, "threshold_set"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := copyFixture(t)
			rewriteManifest(t, dir, tc.drop)
			fixDigests(t, dir)
			_, err := embedded.Load(dir)
			if err == nil {
				t.Fatalf("missing %s must fail verification", tc.field)
			}
			if !containsAll(err.Error(), tc.field) {
				t.Fatalf("error %q does not name %q", err.Error(), tc.field)
			}
		})
	}
}

func fixDigests(t *testing.T, dir string) {
	t.Helper()
	rewriteManifest(t, dir, func(m map[string]any) {
		files, _ := m["files"].(map[string]any)
		if files == nil {
			return
		}
		for name := range files {
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			sum := sha256.Sum256(b)
			files[name] = hex.EncodeToString(sum[:])
		}
	})
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
