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
	// The experiment must be DERIVED from buildx's per-platform TARGETARCH.
	// A plain `ARG GOEXPERIMENT=` is inert: ci.yml and release.yml pass only
	// VERSION as a build-arg while building linux/amd64,linux/arm64, so an
	// arg-driven Dockerfile would ship scalar kernels on every amd64 image.
	if !strings.Contains(df, "ARG TARGETARCH") {
		t.Fatal("Dockerfile must declare buildx's automatic ARG TARGETARCH in the builder stage")
	}
	if strings.Contains(df, "ARG GOEXPERIMENT") {
		t.Fatal("Dockerfile must not declare ARG GOEXPERIMENT: no workflow passes it, so it is inert")
	}
	buildLine := regexp.MustCompile(`(?m)^RUN .*go build .*-o elida\b.*$`).FindString(df)
	if buildLine == "" {
		t.Fatalf("Dockerfile has no `go build -o elida` RUN line:\n%s", df)
	}
	for _, want := range []string{"GOEXPERIMENT=", "TARGETARCH", "simd", "CGO_ENABLED=0"} {
		if !strings.Contains(buildLine, want) {
			t.Fatalf("Dockerfile build line must contain %q (CGO_ENABLED=0 is a release invariant; the rest is the per-arch SIMD derivation), got:\n%s", want, buildLine)
		}
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
