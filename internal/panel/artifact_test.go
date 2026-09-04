package panel

import (
	"os"
	"testing"
)

func TestLoadToolChainArtifact_Golden(t *testing.T) {
	a, err := LoadToolChainArtifact("testdata/tool-chain.golden.json")
	if err != nil {
		t.Fatalf("load golden: %v", err)
	}
	if a.SchemaVersion != "1" || a.MemberType != "tool-chain" {
		t.Fatalf("meta = %q/%q", a.SchemaVersion, a.MemberType)
	}
	g, ok := a.Classes["global"]
	if !ok || len(g.States) != 3 || len(g.Matrix) != 3 || g.OOVFloor <= 0 {
		t.Fatalf("bad global class: %+v", g)
	}
}

func TestLoadToolChainArtifact_RejectsSchemaMismatch(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/bad.json"
	if err := os.WriteFile(p, []byte(`{"schema_version":"2","member_type":"tool-chain","classes":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadToolChainArtifact(p); err == nil {
		t.Fatal("expected schema-version error, got nil")
	}
}

func TestLoadToolChainArtifact_RejectsNonNormalizedRow(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/bad.json"
	// matrix row sums to 0.5, not ~1
	body := `{"schema_version":"1","member_type":"tool-chain","classes":{"global":{
	  "states":["a"],"start":[1.0],"matrix":[[0.5]],"oov_floor":0.001,
	  "calibration":{"percentiles":[1,99],"perplexity":[1.0,2.0]}}}}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadToolChainArtifact(p); err == nil {
		t.Fatal("expected prob-row-sum error, got nil")
	}
}
