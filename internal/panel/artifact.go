package panel

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
)

type Calibration struct {
	Percentiles []float64 `json:"percentiles"`
	Perplexity  []float64 `json:"perplexity"`
}

type ClassChain struct {
	States      []string    `json:"states"`
	Start       []float64   `json:"start"`
	Matrix      [][]float64 `json:"matrix"`
	OOVFloor    float64     `json:"oov_floor"`
	Calibration Calibration `json:"calibration"`
}

type ToolChainArtifact struct {
	SchemaVersion string                `json:"schema_version"`
	MemberType    string                `json:"member_type"`
	GeneratedBy   string                `json:"generated_by"`
	Classes       map[string]ClassChain `json:"classes"`
}

const toolChainSchemaVersion = "1"

func LoadToolChainArtifact(path string) (*ToolChainArtifact, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path from trusted config
	if err != nil {
		return nil, fmt.Errorf("read tool-chain artifact: %w", err)
	}
	var a ToolChainArtifact
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, fmt.Errorf("parse tool-chain artifact: %w", err)
	}
	if a.SchemaVersion != toolChainSchemaVersion {
		return nil, fmt.Errorf("tool-chain artifact schema_version %q, want %q", a.SchemaVersion, toolChainSchemaVersion)
	}
	if a.MemberType != "tool-chain" {
		return nil, fmt.Errorf("tool-chain artifact member_type %q, want \"tool-chain\"", a.MemberType)
	}
	for name, c := range a.Classes {
		n := len(c.States)
		if n == 0 || len(c.Start) != n || len(c.Matrix) != n {
			return nil, fmt.Errorf("class %q: states/start/matrix length mismatch", name)
		}
		if c.OOVFloor <= 0 {
			return nil, fmt.Errorf("class %q: oov_floor must be > 0", name)
		}
		if err := checkRow(name, "start", c.Start, n); err != nil {
			return nil, err
		}
		for i, row := range c.Matrix {
			if err := checkRow(name, fmt.Sprintf("matrix[%d]", i), row, n); err != nil {
				return nil, err
			}
		}
		if err := checkCalibration(name, c.Calibration); err != nil {
			return nil, err
		}
	}
	return &a, nil
}

func checkRow(class, which string, row []float64, n int) error {
	if len(row) != n {
		return fmt.Errorf("class %q %s: length %d, want %d", class, which, len(row), n)
	}
	sum := 0.0
	for _, p := range row {
		sum += p
	}
	if math.Abs(sum-1.0) > 1e-6 {
		return fmt.Errorf("class %q %s: probabilities sum to %g, want ~1", class, which, sum)
	}
	return nil
}

func checkCalibration(class string, c Calibration) error {
	if len(c.Percentiles) != len(c.Perplexity) || len(c.Percentiles) == 0 {
		return fmt.Errorf("class %q calibration: percentiles/perplexity length mismatch or empty", class)
	}
	for i := 1; i < len(c.Percentiles); i++ {
		if c.Percentiles[i] <= c.Percentiles[i-1] || c.Perplexity[i] <= c.Perplexity[i-1] {
			return fmt.Errorf("class %q calibration: must be strictly ascending", class)
		}
	}
	return nil
}
