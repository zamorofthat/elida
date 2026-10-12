package unit

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// modelPinsPath is the single source of the injection model pins, relative
// to this package directory (go test runs in it).
const modelPinsPath = "../../scripts/models/pins.env"

var (
	pinKeyRe     = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	pinCommitRe  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	pinSHA256Re  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	composePinRe = regexp.MustCompile(`DEFENDER_COMMIT:-([0-9a-f]+)\}`)
)

// readModelPins parses pins.env the way its consumers do: KEY=value lines,
// '#' comments and blank lines ignored, nothing else allowed.
func readModelPins(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(modelPinsPath)
	if err != nil {
		t.Fatalf("read %s: %v", modelPinsPath, err)
	}
	pins := map[string]string{}
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || !pinKeyRe.MatchString(key) || value == "" || strings.ContainsAny(value, " \t\"'") {
			t.Fatalf("%s:%d: %q is not KEY=value (no quotes or spaces: bash, make and $GITHUB_ENV all read this file)",
				modelPinsPath, i+1, line)
		}
		if _, dup := pins[key]; dup {
			t.Fatalf("%s:%d: %s is set twice", modelPinsPath, i+1, key)
		}
		pins[key] = value
	}
	return pins
}

// TestModelPins_ParseAndAreWellFormed guards scripts/models/pins.env: every
// key is a 40-hex commit or a 64-hex sha256, and the keys the build, the
// Dockerfile and the workflows read are all present. A malformed pin would
// otherwise surface only as a confusing build or CI failure.
func TestModelPins_ParseAndAreWellFormed(t *testing.T) {
	pins := readModelPins(t)
	for _, required := range []string{"PINNED_DEFENDER_COMMIT", "MODEL_ONNX_SHA256", "MANIFEST_SHA256"} {
		if _, ok := pins[required]; !ok {
			t.Errorf("%s is missing %s", modelPinsPath, required)
		}
	}
	for key, value := range pins {
		if !pinCommitRe.MatchString(value) && !pinSHA256Re.MatchString(value) {
			t.Errorf("%s=%q is neither a 40-hex commit nor a 64-hex sha256", key, value)
		}
	}
	if v := pins["PINNED_DEFENDER_COMMIT"]; v != "" && !pinCommitRe.MatchString(v) {
		t.Errorf("PINNED_DEFENDER_COMMIT=%q is not a 40-hex commit", v)
	}
	for _, key := range []string{"MODEL_ONNX_SHA256", "MANIFEST_SHA256"} {
		if v := pins[key]; v != "" && !pinSHA256Re.MatchString(v) {
			t.Errorf("%s=%q is not a 64-hex sha256", key, v)
		}
	}
}

// TestModelPins_ComposeDefaultMatches covers the one place that cannot read
// pins.env: docker-compose build args are interpolated from the shell or a
// .env file only, so docker-compose.yaml carries the commit as a literal
// default. This keeps that literal from drifting.
func TestModelPins_ComposeDefaultMatches(t *testing.T) {
	pins := readModelPins(t)
	raw, err := os.ReadFile("../../docker-compose.yaml")
	if err != nil {
		t.Fatalf("read docker-compose.yaml: %v", err)
	}
	m := composePinRe.FindSubmatch(raw)
	if m == nil {
		t.Fatal("docker-compose.yaml has no DEFENDER_COMMIT:-<commit> build-arg default")
	}
	if got, want := string(m[1]), pins["PINNED_DEFENDER_COMMIT"]; got != want {
		t.Errorf("docker-compose.yaml defaults DEFENDER_COMMIT to %s, but %s pins %s", got, modelPinsPath, want)
	}
}
