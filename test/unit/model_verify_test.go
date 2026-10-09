package unit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeVerifiedArtifact builds a hermetic copy of scripts/models/verify.sh
// next to a pins.env that pins a small fake artifact, and returns the script
// path and the artifact directory. verify.sh reads pins.env from its own
// directory, so the real pins and the real model are never involved.
func fakeVerifiedArtifact(t *testing.T) (script, dir string) {
	t.Helper()
	for _, bin := range []string{"bash", "python3"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available: %v", bin, err)
		}
	}
	root := t.TempDir()
	scriptDir := filepath.Join(root, "scripts")
	dir = filepath.Join(root, "model")
	for _, d := range []string{scriptDir, dir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	src, err := os.ReadFile("../../scripts/models/verify.sh")
	if err != nil {
		t.Fatalf("read verify.sh: %v", err)
	}
	script = filepath.Join(scriptDir, "verify.sh")
	if err = os.WriteFile(script, src, 0o755); err != nil {
		t.Fatal(err)
	}

	digest := func(b []byte) string {
		sum := sha256.Sum256(b)
		return hex.EncodeToString(sum[:])
	}
	shipped := map[string][]byte{
		"model.onnx":     []byte("fake onnx graph"),
		"tokenizer.json": []byte(`{"fake":true}`),
		"MODEL_CARD.md":  []byte("# fake card\n"),
	}
	files := map[string]string{}
	for name, body := range shipped {
		if err = os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
		files[name] = digest(body)
	}
	manifest, err := json.Marshal(map[string]any{"files": files})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	pins := "PINNED_DEFENDER_COMMIT=" + strings.Repeat("a", 40) + "\n" +
		"MODEL_ONNX_SHA256=" + files["model.onnx"] + "\n" +
		"MANIFEST_SHA256=" + digest(manifest) + "\n"
	if err := os.WriteFile(filepath.Join(scriptDir, "pins.env"), []byte(pins), 0o644); err != nil {
		t.Fatal(err)
	}
	return script, dir
}

// TestModelVerify_RejectsUnlistedFiles guards the release path: the CI model
// artifact is unpacked into every archive, so verify.sh must fail on a file
// the pinned manifest does not list, not only on listed files that differ.
func TestModelVerify_RejectsUnlistedFiles(t *testing.T) {
	script, dir := fakeVerifiedArtifact(t)

	out, err := exec.Command("bash", script, dir).CombinedOutput()
	if err != nil {
		t.Fatalf("a clean artifact must verify: %v\n%s", err, out)
	}

	if err = os.WriteFile(filepath.Join(dir, "extra.onnx"), []byte("second graph"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = exec.Command("bash", script, dir).CombinedOutput()
	if err == nil {
		t.Fatalf("an unlisted extra.onnx must fail verification:\n%s", out)
	}
	if !strings.Contains(string(out), "does not list") || !strings.Contains(string(out), "extra.onnx") {
		t.Fatalf("the failure must name the unlisted file:\n%s", out)
	}
}
