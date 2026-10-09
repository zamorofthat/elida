// Package embedded is the in-process semantic decision provider: a pure-Go
// ONNX text-classification pipeline over a packaged MiniLM-L6 multi-head
// model.
//
// Model assets are not compiled in. The manifest is parsed, every listed
// file's SHA-256 is verified, and the head order is asserted before the
// provider reports itself usable.
package embedded

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"elida/internal/decision"
)

// ManifestName is the file that describes a packaged model.
const ManifestName = "manifest.json"

var (
	ErrManifestMissing  = errors.New("embedded: model manifest not found")
	ErrChecksumMismatch = errors.New("embedded: model file checksum mismatch")
	ErrHeadOrder        = errors.New("embedded: model head order must be [injection, human_directed]")
)

// Calibration is the threshold artifact that travels with a model.
type Calibration struct {
	Temperature         float64 `json:"temperature"`
	MainThreshold       float64 `json:"main_threshold"`
	AuxThreshold        float64 `json:"aux_threshold"`
	SingleHeadThreshold float64 `json:"single_head_threshold,omitempty"`
	ECE                 float64 `json:"ece"`
	ThresholdSet        string  `json:"threshold_set"`
	FittedOn            string  `json:"fitted_on"`
}

// Manifest describes a packaged model and its provenance.
type Manifest struct {
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	HeadOrder    []string          `json:"head_order"`
	Signals      []decision.Signal `json:"signals"`
	Calibration  Calibration       `json:"calibration"`
	Files        map[string]string `json:"files"`
	License      string            `json:"license"`
	Source       string            `json:"source"`
	SourceCommit string            `json:"source_commit"`
	Conversion   string            `json:"conversion"`

	checksum string
}

// Checksum returns the SHA-256 of the manifest bytes as loaded.
func (m *Manifest) Checksum() string { return m.checksum }

// LoadManifest reads and parses dir/manifest.json without verifying files.
func LoadManifest(dir string) (*Manifest, error) {
	path := filepath.Join(dir, ManifestName)
	raw, err := os.ReadFile(path) // #nosec G304 -- operator-selected model directory
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrManifestMissing, path)
		}
		return nil, fmt.Errorf("embedded: reading %s: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("embedded: parsing %s: %w", path, err)
	}
	sum := sha256.Sum256(raw)
	m.checksum = hex.EncodeToString(sum[:])
	return &m, nil
}

// VerifyFiles checks that every listed file is confined to dir and matches
// its recorded SHA-256.
func VerifyFiles(dir string, m *Manifest) error {
	if len(m.Files) == 0 {
		return errors.New("embedded: manifest lists no files")
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("embedded: resolving %s: %w", dir, err)
	}
	realDir, err := filepath.EvalSymlinks(absDir)
	if err != nil {
		return fmt.Errorf("embedded: resolving model directory %s: %w", dir, err)
	}

	names := make([]string, 0, len(m.Files))
	for name := range m.Files {
		names = append(names, name)
	}
	sortStrings(names)

	for _, name := range names {
		want := m.Files[name]
		if len(want) != 64 {
			return fmt.Errorf("embedded: %s: digest %q is not a 64-character sha256", name, want)
		}
		if filepath.IsAbs(name) {
			return fmt.Errorf("embedded: manifest entry %q escapes the model directory", name)
		}
		full := filepath.Join(absDir, name)
		rel, relErr := filepath.Rel(absDir, full)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("embedded: manifest entry %q escapes the model directory", name)
		}
		resolved, err := filepath.EvalSymlinks(full)
		if err != nil {
			return fmt.Errorf("embedded: %s: %w", name, err)
		}
		realRel, relErr := filepath.Rel(realDir, resolved)
		if relErr != nil || realRel == ".." || strings.HasPrefix(realRel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("embedded: manifest entry %q escapes the model directory", name)
		}
		data, err := os.ReadFile(resolved) // #nosec G304 -- confined above
		if err != nil {
			return fmt.Errorf("embedded: %s: %w", name, err)
		}
		sum := sha256.Sum256(data)
		got := hex.EncodeToString(sum[:])
		if got != want {
			return fmt.Errorf("%w: %s (have %s, manifest says %s)", ErrChecksumMismatch, name, got[:16], want[:16])
		}
	}
	return nil
}

func (m *Manifest) validate() error {
	if m.Name == "" {
		return errors.New("embedded: manifest field name is required")
	}
	if m.Version == "" {
		return errors.New("embedded: manifest field version is required")
	}
	if m.License == "" {
		return errors.New("embedded: manifest field license is required: redistribution rights must be recorded")
	}
	if len(m.HeadOrder) != 2 || m.HeadOrder[0] != string(decision.SignalInjection) || m.HeadOrder[1] != string(decision.SignalHumanDirected) {
		return fmt.Errorf("%w: got %v", ErrHeadOrder, m.HeadOrder)
	}
	if len(m.Signals) == 0 {
		return errors.New("embedded: manifest field signals is required")
	}
	for _, signal := range m.Signals {
		switch signal {
		case decision.SignalInjection, decision.SignalHumanDirected:
		default:
			return fmt.Errorf("embedded: manifest declares unsupported signal %q", signal)
		}
	}
	if m.Calibration.Temperature <= 0 {
		return errors.New("embedded: manifest field calibration.temperature must be greater than zero")
	}
	if m.Calibration.MainThreshold <= 0 || m.Calibration.MainThreshold >= 1 {
		return fmt.Errorf("embedded: calibration.main_threshold %v is outside (0,1)", m.Calibration.MainThreshold)
	}
	if m.Calibration.AuxThreshold <= 0 || m.Calibration.AuxThreshold >= 1 {
		return fmt.Errorf("embedded: calibration.aux_threshold %v is outside (0,1)", m.Calibration.AuxThreshold)
	}
	if m.Calibration.ThresholdSet == "" {
		return errors.New("embedded: manifest field calibration.threshold_set is required")
	}
	return nil
}

// Load reads, validates, and verifies a packaged model directory.
func Load(dir string) (*Manifest, error) {
	if st, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("embedded: model path %s: %w", dir, err)
	} else if !st.IsDir() {
		return nil, fmt.Errorf("embedded: model path %s is not a directory", dir)
	}
	m, err := LoadManifest(dir)
	if err != nil {
		return nil, err
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	if err := VerifyFiles(dir, m); err != nil {
		return nil, err
	}
	return m, nil
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
