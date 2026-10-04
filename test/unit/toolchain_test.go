package unit

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// repoFile reads a file relative to the repository root. Tests in this
// package run with the working directory at test/unit.
func repoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile("../../" + rel)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func TestToolchain_GoVersionIs127(t *testing.T) {
	gomod := repoFile(t, "go.mod")
	m := regexp.MustCompile(`(?m)^go (\d+\.\d+(?:\.\d+)?)$`).FindStringSubmatch(gomod)
	if m == nil {
		t.Fatalf("go.mod has no 'go' directive:\n%s", gomod)
	}
	if !strings.HasPrefix(m[1], "1.27") {
		t.Fatalf("go.mod declares go %s; Hugot v0.8.1 requires 1.27", m[1])
	}

	for _, wf := range []string{".github/workflows/ci.yml", ".github/workflows/release.yml"} {
		body := repoFile(t, wf)
		gm := regexp.MustCompile(`GO_VERSION:\s*"([^"]+)"`).FindStringSubmatch(body)
		if gm == nil {
			t.Fatalf("%s has no GO_VERSION", wf)
		}
		if !strings.HasPrefix(gm[1], "1.27") {
			t.Fatalf("%s pins GO_VERSION %s, want 1.27.x", wf, gm[1])
		}
	}
}

func TestToolchain_SIMDOnLinuxAmd64Releases(t *testing.T) {
	gr := repoFile(t, ".goreleaser.yaml")
	if !strings.Contains(gr, "GOEXPERIMENT=") {
		t.Fatal(".goreleaser.yaml must set GOEXPERIMENT for linux/amd64 builds")
	}
	if !strings.Contains(gr, "CGO_ENABLED=0") {
		t.Fatal(".goreleaser.yaml must keep CGO_ENABLED=0 (release invariant)")
	}

	df := repoFile(t, "Dockerfile")
	if !strings.Contains(df, "GOEXPERIMENT=${GOEXPERIMENT}") {
		t.Fatal("Dockerfile must thread GOEXPERIMENT into the go build command")
	}
	if !strings.Contains(df, "CGO_ENABLED=0") {
		t.Fatal("Dockerfile must keep CGO_ENABLED=0 (release invariant)")
	}

	ci := repoFile(t, ".github/workflows/ci.yml")
	// Assert the functional wiring, not just a comment: a matrix entry whose
	// goexperiment value is "simd" ...
	if !regexp.MustCompile(`goexperiment:\s*"simd"`).MatchString(ci) {
		t.Fatal("ci.yml must define a test matrix entry with goexperiment: \"simd\"")
	}
	// ... actually piped into the test step's environment. Without this
	// line the simd leg would run with GOEXPERIMENT unset and silently test
	// the scalar path twice.
	if !strings.Contains(ci, "GOEXPERIMENT: ${{ matrix.goexperiment }}") {
		t.Fatal("ci.yml must pass matrix.goexperiment through to the test step's GOEXPERIMENT env var")
	}
}
