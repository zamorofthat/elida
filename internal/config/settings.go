package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// SettingsLayer identifies the source of settings
type SettingsLayer string

const (
	LayerDefault SettingsLayer = "default" // Built-in, read-only
	LayerLocal   SettingsLayer = "local"   // User customizations
)

// Settings represents all user-configurable settings
type Settings struct {
	Policy   PolicySettings   `json:"policy" yaml:"policy"`
	Failover FailoverSettings `json:"failover" yaml:"failover"`
	Capture  CaptureSettings  `json:"capture" yaml:"capture"`
	Decision DecisionSettings `json:"decision" yaml:"decision"`
}

// PolicySettings holds policy-related settings
type PolicySettings struct {
	Enabled              *bool                         `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Mode                 *string                       `json:"mode,omitempty" yaml:"mode,omitempty"`     // "enforce" or "audit"
	Preset               *string                       `json:"preset,omitempty" yaml:"preset,omitempty"` // "minimal", "standard", "strict"
	RiskLadder           *RiskLadderSettings           `json:"risk_ladder,omitempty" yaml:"risk_ladder,omitempty"`
	InstructionIntegrity *InstructionIntegritySettings `json:"instruction_integrity,omitempty" yaml:"instruction_integrity,omitempty"`
	DisabledRules        []string                      `json:"disabled_rules,omitempty" yaml:"disabled_rules,omitempty"` // Rules to skip
	CustomRules          []CustomRule                  `json:"custom_rules,omitempty" yaml:"custom_rules,omitempty"`     // User-defined rules
}

// InstructionIntegritySettings holds instruction file integrity settings
type InstructionIntegritySettings struct {
	Enabled                  *bool    `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	TrackedTypes             []string `json:"tracked_types,omitempty" yaml:"tracked_types,omitempty"`
	ShapeDetection           *bool    `json:"shape_detection,omitempty" yaml:"shape_detection,omitempty"`
	ShapeConfidenceThreshold *float64 `json:"shape_confidence_threshold,omitempty" yaml:"shape_confidence_threshold,omitempty"`
}

// CustomRule represents a user-defined policy rule
type CustomRule struct {
	Name           string   `json:"name" yaml:"name"`
	Type           string   `json:"type" yaml:"type"`               // content_match, bytes_out, bytes_in, request_count, duration, requests_per_minute, rate_anomaly, content_entropy
	Target         string   `json:"target,omitempty" yaml:"target"` // request, response, both (default: both)
	Patterns       []string `json:"patterns,omitempty" yaml:"patterns,omitempty"`
	Threshold      int64    `json:"threshold,omitempty" yaml:"threshold,omitempty"`
	ThresholdFloat float64  `json:"threshold_float,omitempty" yaml:"threshold_float,omitempty"`
	MinSamples     int      `json:"min_samples,omitempty" yaml:"min_samples,omitempty"`
	Severity       string   `json:"severity" yaml:"severity"`       // info, warning, critical
	Action         string   `json:"action,omitempty" yaml:"action"` // flag, block, terminate
	Description    string   `json:"description,omitempty" yaml:"description,omitempty"`
}

// RiskLadderSettings holds risk ladder thresholds
type RiskLadderSettings struct {
	Enabled        *bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	WarnScore      *int  `json:"warn_score,omitempty" yaml:"warn_score,omitempty"`
	ThrottleScore  *int  `json:"throttle_score,omitempty" yaml:"throttle_score,omitempty"`
	BlockScore     *int  `json:"block_score,omitempty" yaml:"block_score,omitempty"`
	TerminateScore *int  `json:"terminate_score,omitempty" yaml:"terminate_score,omitempty"`
}

// FailoverSettings holds failover-related settings
type FailoverSettings struct {
	Enabled       *bool    `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	FallbackOrder []string `json:"fallback_order,omitempty" yaml:"fallback_order,omitempty"`
	MaxRetries    *int     `json:"max_retries,omitempty" yaml:"max_retries,omitempty"`
	AutoSelect    *bool    `json:"auto_select,omitempty" yaml:"auto_select,omitempty"` // Auto-select best match
}

// CaptureSettings holds capture-related settings
type CaptureSettings struct {
	Mode           *string `json:"mode,omitempty" yaml:"mode,omitempty"` // "flagged_only" or "all"
	MaxCaptureSize *int    `json:"max_capture_size,omitempty" yaml:"max_capture_size,omitempty"`
	MaxPerSession  *int    `json:"max_per_session,omitempty" yaml:"max_per_session,omitempty"`
}

// DecisionSettings holds the runtime-editable subset of DecisionConfig.
//
// Deliberately absent: enabled, required, require_inline, provider,
// model_path and endpoint are restart-required (they load model assets,
// gate startup, or move the network boundary) and live only in elida.yaml;
// inline_queue_wait is absent because Phase 1 requires it to be zero, so it
// is read-only and reported at /control/decision. Thresholds themselves are
// never edited here — threshold_set selects an installed versioned artifact.
type DecisionSettings struct {
	Mode              *string                        `json:"mode,omitempty" yaml:"mode,omitempty"`
	ThresholdSet      *string                        `json:"threshold_set,omitempty" yaml:"threshold_set,omitempty"`
	ElevatedThreshold *float64                       `json:"elevated_threshold,omitempty" yaml:"elevated_threshold,omitempty"`
	InlineTimeoutMs   *int                           `json:"inline_timeout_ms,omitempty" yaml:"inline_timeout_ms,omitempty"`
	MaxConcurrency    *int                           `json:"max_concurrency,omitempty" yaml:"max_concurrency,omitempty"`
	MaxInlineTokens   *int                           `json:"max_inline_tokens,omitempty" yaml:"max_inline_tokens,omitempty"`
	MaxInlineWindows  *int                           `json:"max_inline_windows,omitempty" yaml:"max_inline_windows,omitempty"`
	MaxAsyncWindows   *int                           `json:"max_async_windows,omitempty" yaml:"max_async_windows,omitempty"`
	AsyncQueueSize    *int                           `json:"async_queue_size,omitempty" yaml:"async_queue_size,omitempty"`
	Preprocessing     *DecisionPreprocessingSettings `json:"preprocessing,omitempty" yaml:"preprocessing,omitempty"`
}

// DecisionPreprocessingSettings holds the runtime-editable preprocessing bounds.
type DecisionPreprocessingSettings struct {
	MaxInputBytes      *int `json:"max_input_bytes,omitempty" yaml:"max_input_bytes,omitempty"`
	MaxAnalysisBytes   *int `json:"max_analysis_bytes,omitempty" yaml:"max_analysis_bytes,omitempty"`
	MaxRepresentations *int `json:"max_representations,omitempty" yaml:"max_representations,omitempty"`
	MaxDecodeDepth     *int `json:"max_decode_depth,omitempty" yaml:"max_decode_depth,omitempty"`
	MaxExpansionRatio  *int `json:"max_expansion_ratio,omitempty" yaml:"max_expansion_ratio,omitempty"`
}

// ApplyTo folds the edited settings onto a base DecisionConfig and returns
// the effective config plus a reason for every edit that was refused.
//
// A refused edit leaves the base value in place: the config this returns is
// always within its ceilings, so a dashboard edit cannot loosen a bound or
// enable enforcement that policy.mode forbids. policyMode is the effective
// policy mode ("enforce" or "audit").
func (s DecisionSettings) ApplyTo(d DecisionConfig, policyMode string) (DecisionConfig, []string) {
	var rejected []string

	applyInt := func(dst *int, v *int, field string, min, ceiling int) {
		if v == nil {
			return
		}
		switch {
		case *v < min:
			rejected = append(rejected, fmt.Sprintf("decision.%s: %d is below the minimum of %d", field, *v, min))
		case *v > ceiling:
			rejected = append(rejected, fmt.Sprintf("decision.%s: %d exceeds the ceiling of %d", field, *v, ceiling))
		default:
			*dst = *v
		}
	}

	if s.Mode != nil {
		switch *s.Mode {
		case DecisionModeDisabled, DecisionModeShadow, DecisionModeAudit:
			d.Mode = *s.Mode
		case DecisionModeEnforce:
			if policyMode == "audit" {
				d.Mode = DecisionModeAudit
				rejected = append(rejected, "decision.mode: enforce capped to audit because policy.mode is audit")
			} else {
				d.Mode = DecisionModeEnforce
			}
		default:
			rejected = append(rejected, fmt.Sprintf("decision.mode: unknown mode %q", *s.Mode))
		}
	}
	if s.ThresholdSet != nil {
		if *s.ThresholdSet == "" {
			rejected = append(rejected, "decision.threshold_set: must name an installed threshold-set version")
		} else {
			d.ThresholdSet = *s.ThresholdSet
		}
	}
	if s.ElevatedThreshold != nil {
		if *s.ElevatedThreshold < 0 || *s.ElevatedThreshold > 1 {
			rejected = append(rejected, fmt.Sprintf("decision.elevated_threshold: %v is outside 0..1", *s.ElevatedThreshold))
		} else {
			d.ElevatedThreshold = *s.ElevatedThreshold
		}
	}
	if s.InlineTimeoutMs != nil {
		if *s.InlineTimeoutMs <= 0 {
			rejected = append(rejected, fmt.Sprintf("decision.inline_timeout_ms: %d must be greater than zero", *s.InlineTimeoutMs))
		} else {
			d.InlineTimeout = time.Duration(*s.InlineTimeoutMs) * time.Millisecond
		}
	}
	applyInt(&d.MaxConcurrency, s.MaxConcurrency, "max_concurrency", 1, MaxConcurrencyCeiling)
	applyInt(&d.MaxInlineTokens, s.MaxInlineTokens, "max_inline_tokens", 1, MaxInlineTokensCeiling)
	applyInt(&d.MaxInlineWindows, s.MaxInlineWindows, "max_inline_windows", 1, MaxInlineWindowsCeiling)
	applyInt(&d.MaxAsyncWindows, s.MaxAsyncWindows, "max_async_windows", 0, MaxAsyncWindowsCeiling)
	applyInt(&d.AsyncQueueSize, s.AsyncQueueSize, "async_queue_size", 1, AsyncQueueSizeCeiling)

	if p := s.Preprocessing; p != nil {
		applyInt(&d.Preprocessing.MaxInputBytes, p.MaxInputBytes, "preprocessing.max_input_bytes", 1, MaxInputBytesCeiling)
		applyInt(&d.Preprocessing.MaxAnalysisBytes, p.MaxAnalysisBytes, "preprocessing.max_analysis_bytes", 1, MaxAnalysisBytesCeiling)
		applyInt(&d.Preprocessing.MaxRepresentations, p.MaxRepresentations, "preprocessing.max_representations", 1, MaxRepresentationsCeiling)
		applyInt(&d.Preprocessing.MaxDecodeDepth, p.MaxDecodeDepth, "preprocessing.max_decode_depth", 0, MaxDecodeDepthCeiling)
		applyInt(&d.Preprocessing.MaxExpansionRatio, p.MaxExpansionRatio, "preprocessing.max_expansion_ratio", 1, MaxExpansionRatioCeiling)
	}

	// Phase 1 invariant: inline inference never waits for worker capacity.
	// Enforced here as well as in validation, so a settings edit cannot
	// reach a nonzero value by any route.
	d.InlineQueueWait = 0
	return d, rejected
}

// SettingsStore manages settings with layered configuration
type SettingsStore struct {
	mu       sync.RWMutex
	defaults Settings
	local    Settings
	path     string // Path to local settings file
}

// NewSettingsStore creates a new settings store with hardcoded defaults.
// Prefer NewSettingsStoreFromConfig to use Config-based defaults (yaml → env → settings.yaml).
func NewSettingsStore(dataDir string) (*SettingsStore, error) {
	store := &SettingsStore{
		defaults: getDefaultSettings(),
		path:     filepath.Join(dataDir, "settings.yaml"),
	}

	// Load local settings if they exist
	if err := store.loadLocal(); err != nil {
		// Not an error if file doesn't exist
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to load local settings: %w", err)
		}
	}

	return store, nil
}

// NewSettingsStoreFromConfig creates a settings store with defaults from loaded Config.
// This implements the hierarchy: elida.yaml → ENV vars → settings.yaml (UI).
// The Config should already have YAML and ENV overrides applied via config.Load().
func NewSettingsStoreFromConfig(cfg *Config, dataDir string) (*SettingsStore, error) {
	store := &SettingsStore{
		defaults: settingsFromConfig(cfg),
		path:     filepath.Join(dataDir, "settings.yaml"),
	}

	// Load local settings (UI overrides) if they exist
	if err := store.loadLocal(); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to load local settings: %w", err)
		}
	}

	return store, nil
}

// settingsFromConfig extracts Settings from a loaded Config.
// Fields not present in Config use sensible defaults.
func settingsFromConfig(cfg *Config) Settings {
	// Start with hardcoded defaults for fields not in Config
	settings := getDefaultSettings()

	// Override with Config values where they exist

	// Policy settings
	settings.Policy.Enabled = &cfg.Policy.Enabled
	if cfg.Policy.Mode != "" {
		settings.Policy.Mode = &cfg.Policy.Mode
	}
	if cfg.Policy.Preset != "" {
		settings.Policy.Preset = &cfg.Policy.Preset
	}

	// Risk ladder from Config thresholds
	if cfg.Policy.RiskLadder.Enabled {
		enabled := cfg.Policy.RiskLadder.Enabled
		settings.Policy.RiskLadder.Enabled = &enabled
	}
	// Extract thresholds from Config's array format to Settings' named fields
	for _, t := range cfg.Policy.RiskLadder.Thresholds {
		score := int(t.Score)
		switch t.Action {
		case "warn":
			settings.Policy.RiskLadder.WarnScore = &score
		case "throttle":
			settings.Policy.RiskLadder.ThrottleScore = &score
		case "block":
			settings.Policy.RiskLadder.BlockScore = &score
		case "terminate":
			settings.Policy.RiskLadder.TerminateScore = &score
		}
	}

	// Instruction integrity settings from Config
	iiCfg := cfg.Policy.InstructionIntegrity
	settings.Policy.InstructionIntegrity = &InstructionIntegritySettings{
		Enabled:                  &iiCfg.Enabled,
		TrackedTypes:             iiCfg.TrackedTypes,
		ShapeDetection:           &iiCfg.ShapeDetection,
		ShapeConfidenceThreshold: &iiCfg.ShapeConfidenceThreshold,
	}

	// Capture settings from Storage config
	if cfg.Storage.CaptureMode != "" {
		settings.Capture.Mode = &cfg.Storage.CaptureMode
	}
	if cfg.Storage.MaxCaptureSize > 0 {
		settings.Capture.MaxCaptureSize = &cfg.Storage.MaxCaptureSize
	}
	if cfg.Storage.MaxCapturedPerSession > 0 {
		settings.Capture.MaxPerSession = &cfg.Storage.MaxCapturedPerSession
	}

	// Failover settings - not directly in Config yet, use defaults
	// Future: could add failover section to Config

	// Decision settings — the runtime-editable subset mirrors Config so the
	// dashboard shows the values elida.yaml actually loaded.
	settings.Decision = decisionSettingsFromConfig(cfg.Decision)

	return settings
}

// decisionSettingsFromConfig projects a DecisionConfig onto the
// runtime-editable settings surface.
func decisionSettingsFromConfig(d DecisionConfig) DecisionSettings {
	mode := d.Mode
	thresholdSet := d.ThresholdSet
	elevated := d.ElevatedThreshold
	inlineMs := int(d.InlineTimeout / time.Millisecond)
	maxConc := d.MaxConcurrency
	inlineTokens := d.MaxInlineTokens
	inlineWindows := d.MaxInlineWindows
	asyncWindows := d.MaxAsyncWindows
	queueSize := d.AsyncQueueSize
	inputBytes := d.Preprocessing.MaxInputBytes
	analysisBytes := d.Preprocessing.MaxAnalysisBytes
	reps := d.Preprocessing.MaxRepresentations
	depth := d.Preprocessing.MaxDecodeDepth
	ratio := d.Preprocessing.MaxExpansionRatio

	return DecisionSettings{
		Mode:              &mode,
		ThresholdSet:      &thresholdSet,
		ElevatedThreshold: &elevated,
		InlineTimeoutMs:   &inlineMs,
		MaxConcurrency:    &maxConc,
		MaxInlineTokens:   &inlineTokens,
		MaxInlineWindows:  &inlineWindows,
		MaxAsyncWindows:   &asyncWindows,
		AsyncQueueSize:    &queueSize,
		Preprocessing: &DecisionPreprocessingSettings{
			MaxInputBytes:      &inputBytes,
			MaxAnalysisBytes:   &analysisBytes,
			MaxRepresentations: &reps,
			MaxDecodeDepth:     &depth,
			MaxExpansionRatio:  &ratio,
		},
	}
}

// getDefaultSettings returns ELIDA's built-in defaults
func getDefaultSettings() Settings {
	enabled := true
	disabled := false
	enforce := "enforce"
	standard := "standard"
	flaggedOnly := "flagged_only"

	warnScore := 5
	throttleScore := 15
	blockScore := 30
	terminateScore := 50

	maxRetries := 2
	maxCaptureSize := 10000
	maxPerSession := 100

	shapeDetection := true
	shapeThreshold := 0.7

	return Settings{
		Policy: PolicySettings{
			Enabled: &enabled,
			Mode:    &enforce,
			Preset:  &standard,
			RiskLadder: &RiskLadderSettings{
				Enabled:        &enabled,
				WarnScore:      &warnScore,
				ThrottleScore:  &throttleScore,
				BlockScore:     &blockScore,
				TerminateScore: &terminateScore,
			},
			InstructionIntegrity: &InstructionIntegritySettings{
				Enabled:                  &enabled,
				TrackedTypes:             []string{"claude_md", "cursorrules", "cursor_rules", "agents_md", "windsurfrules"},
				ShapeDetection:           &shapeDetection,
				ShapeConfidenceThreshold: &shapeThreshold,
			},
			DisabledRules: []string{},
		},
		Failover: FailoverSettings{
			Enabled:       &disabled,
			FallbackOrder: []string{},
			MaxRetries:    &maxRetries,
			AutoSelect:    &enabled,
		},
		Capture: CaptureSettings{
			Mode:           &flaggedOnly,
			MaxCaptureSize: &maxCaptureSize,
			MaxPerSession:  &maxPerSession,
		},
		Decision: decisionSettingsFromConfig(defaults().Decision),
	}
}

// GetDefaults returns the built-in default settings (read-only)
func (s *SettingsStore) GetDefaults() Settings {
	return s.defaults
}

// GetLocal returns only the user's customizations
func (s *SettingsStore) GetLocal() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.local
}

// GetMerged returns settings with local overriding defaults
func (s *SettingsStore) GetMerged() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return mergeSettings(s.defaults, s.local)
}

// SaveLocal saves user customizations
func (s *SettingsStore) SaveLocal(settings Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.local = settings

	// Ensure directory exists
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("failed to create settings directory: %w", err)
	}

	// Write to file as YAML (consistent with elida.yaml)
	data, err := yaml.Marshal(settings)
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	if err := os.WriteFile(s.path, data, 0600); err != nil {
		return fmt.Errorf("failed to write settings file: %w", err)
	}

	return nil
}

// ResetToDefault removes all local customizations
func (s *SettingsStore) ResetToDefault() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.local = Settings{}

	// Remove the settings file if it exists
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove settings file: %w", err)
	}

	return nil
}

// loadLocal loads local settings from file
func (s *SettingsStore) loadLocal() error {
	if strings.Contains(s.path, "..") {
		return fmt.Errorf("invalid file path")
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}

	if err := yaml.Unmarshal(data, &s.local); err != nil {
		return fmt.Errorf("failed to parse settings file: %w", err)
	}

	return nil
}

// GetDiff returns which settings differ from defaults
func (s *SettingsStore) GetDiff() map[string]SettingDiff {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return diffSettings(s.defaults, s.local)
}

// SettingDiff represents a difference from default
type SettingDiff struct {
	Path         string `json:"path"`
	DefaultValue any    `json:"default_value"`
	LocalValue   any    `json:"local_value"`
}

// diffSettings compares local settings against defaults
func diffSettings(defaults, local Settings) map[string]SettingDiff {
	diffs := make(map[string]SettingDiff)

	// Policy diffs
	if local.Policy.Enabled != nil && *local.Policy.Enabled != *defaults.Policy.Enabled {
		diffs["policy.enabled"] = SettingDiff{
			Path:         "policy.enabled",
			DefaultValue: *defaults.Policy.Enabled,
			LocalValue:   *local.Policy.Enabled,
		}
	}
	if local.Policy.Mode != nil && *local.Policy.Mode != *defaults.Policy.Mode {
		diffs["policy.mode"] = SettingDiff{
			Path:         "policy.mode",
			DefaultValue: *defaults.Policy.Mode,
			LocalValue:   *local.Policy.Mode,
		}
	}
	if local.Policy.Preset != nil && *local.Policy.Preset != *defaults.Policy.Preset {
		diffs["policy.preset"] = SettingDiff{
			Path:         "policy.preset",
			DefaultValue: *defaults.Policy.Preset,
			LocalValue:   *local.Policy.Preset,
		}
	}

	// Risk ladder diffs
	if local.Policy.RiskLadder != nil && defaults.Policy.RiskLadder != nil {
		lr := local.Policy.RiskLadder
		dr := defaults.Policy.RiskLadder

		if lr.WarnScore != nil && *lr.WarnScore != *dr.WarnScore {
			diffs["policy.risk_ladder.warn_score"] = SettingDiff{
				Path:         "policy.risk_ladder.warn_score",
				DefaultValue: *dr.WarnScore,
				LocalValue:   *lr.WarnScore,
			}
		}
		if lr.ThrottleScore != nil && *lr.ThrottleScore != *dr.ThrottleScore {
			diffs["policy.risk_ladder.throttle_score"] = SettingDiff{
				Path:         "policy.risk_ladder.throttle_score",
				DefaultValue: *dr.ThrottleScore,
				LocalValue:   *lr.ThrottleScore,
			}
		}
		if lr.BlockScore != nil && *lr.BlockScore != *dr.BlockScore {
			diffs["policy.risk_ladder.block_score"] = SettingDiff{
				Path:         "policy.risk_ladder.block_score",
				DefaultValue: *dr.BlockScore,
				LocalValue:   *lr.BlockScore,
			}
		}
	}

	// Instruction integrity diffs
	if local.Policy.InstructionIntegrity != nil && defaults.Policy.InstructionIntegrity != nil {
		li := local.Policy.InstructionIntegrity
		di := defaults.Policy.InstructionIntegrity
		if li.Enabled != nil && di.Enabled != nil && *li.Enabled != *di.Enabled {
			diffs["policy.instruction_integrity.enabled"] = SettingDiff{
				Path:         "policy.instruction_integrity.enabled",
				DefaultValue: *di.Enabled,
				LocalValue:   *li.Enabled,
			}
		}
		if li.ShapeDetection != nil && di.ShapeDetection != nil && *li.ShapeDetection != *di.ShapeDetection {
			diffs["policy.instruction_integrity.shape_detection"] = SettingDiff{
				Path:         "policy.instruction_integrity.shape_detection",
				DefaultValue: *di.ShapeDetection,
				LocalValue:   *li.ShapeDetection,
			}
		}
		if li.ShapeConfidenceThreshold != nil && di.ShapeConfidenceThreshold != nil && *li.ShapeConfidenceThreshold != *di.ShapeConfidenceThreshold {
			diffs["policy.instruction_integrity.shape_confidence_threshold"] = SettingDiff{
				Path:         "policy.instruction_integrity.shape_confidence_threshold",
				DefaultValue: *di.ShapeConfidenceThreshold,
				LocalValue:   *li.ShapeConfidenceThreshold,
			}
		}
	}

	// Failover diffs
	if local.Failover.Enabled != nil && defaults.Failover.Enabled != nil {
		if *local.Failover.Enabled != *defaults.Failover.Enabled {
			diffs["failover.enabled"] = SettingDiff{
				Path:         "failover.enabled",
				DefaultValue: *defaults.Failover.Enabled,
				LocalValue:   *local.Failover.Enabled,
			}
		}
	}
	if len(local.Failover.FallbackOrder) > 0 {
		diffs["failover.fallback_order"] = SettingDiff{
			Path:         "failover.fallback_order",
			DefaultValue: defaults.Failover.FallbackOrder,
			LocalValue:   local.Failover.FallbackOrder,
		}
	}

	// Capture diffs
	if local.Capture.Mode != nil && *local.Capture.Mode != *defaults.Capture.Mode {
		diffs["capture.mode"] = SettingDiff{
			Path:         "capture.mode",
			DefaultValue: *defaults.Capture.Mode,
			LocalValue:   *local.Capture.Mode,
		}
	}

	// Decision diffs
	if local.Decision.Mode != nil && *local.Decision.Mode != *defaults.Decision.Mode {
		diffs["decision.mode"] = SettingDiff{
			Path:         "decision.mode",
			DefaultValue: *defaults.Decision.Mode,
			LocalValue:   *local.Decision.Mode,
		}
	}
	if local.Decision.ThresholdSet != nil && *local.Decision.ThresholdSet != *defaults.Decision.ThresholdSet {
		diffs["decision.threshold_set"] = SettingDiff{
			Path:         "decision.threshold_set",
			DefaultValue: *defaults.Decision.ThresholdSet,
			LocalValue:   *local.Decision.ThresholdSet,
		}
	}
	if local.Decision.ElevatedThreshold != nil && *local.Decision.ElevatedThreshold != *defaults.Decision.ElevatedThreshold {
		diffs["decision.elevated_threshold"] = SettingDiff{
			Path:         "decision.elevated_threshold",
			DefaultValue: *defaults.Decision.ElevatedThreshold,
			LocalValue:   *local.Decision.ElevatedThreshold,
		}
	}
	if local.Decision.InlineTimeoutMs != nil && *local.Decision.InlineTimeoutMs != *defaults.Decision.InlineTimeoutMs {
		diffs["decision.inline_timeout_ms"] = SettingDiff{
			Path:         "decision.inline_timeout_ms",
			DefaultValue: *defaults.Decision.InlineTimeoutMs,
			LocalValue:   *local.Decision.InlineTimeoutMs,
		}
	}
	if local.Decision.MaxConcurrency != nil && *local.Decision.MaxConcurrency != *defaults.Decision.MaxConcurrency {
		diffs["decision.max_concurrency"] = SettingDiff{
			Path:         "decision.max_concurrency",
			DefaultValue: *defaults.Decision.MaxConcurrency,
			LocalValue:   *local.Decision.MaxConcurrency,
		}
	}
	if local.Decision.MaxInlineTokens != nil && *local.Decision.MaxInlineTokens != *defaults.Decision.MaxInlineTokens {
		diffs["decision.max_inline_tokens"] = SettingDiff{
			Path:         "decision.max_inline_tokens",
			DefaultValue: *defaults.Decision.MaxInlineTokens,
			LocalValue:   *local.Decision.MaxInlineTokens,
		}
	}
	if local.Decision.MaxInlineWindows != nil && *local.Decision.MaxInlineWindows != *defaults.Decision.MaxInlineWindows {
		diffs["decision.max_inline_windows"] = SettingDiff{
			Path:         "decision.max_inline_windows",
			DefaultValue: *defaults.Decision.MaxInlineWindows,
			LocalValue:   *local.Decision.MaxInlineWindows,
		}
	}
	if local.Decision.MaxAsyncWindows != nil && *local.Decision.MaxAsyncWindows != *defaults.Decision.MaxAsyncWindows {
		diffs["decision.max_async_windows"] = SettingDiff{
			Path:         "decision.max_async_windows",
			DefaultValue: *defaults.Decision.MaxAsyncWindows,
			LocalValue:   *local.Decision.MaxAsyncWindows,
		}
	}
	if local.Decision.AsyncQueueSize != nil && *local.Decision.AsyncQueueSize != *defaults.Decision.AsyncQueueSize {
		diffs["decision.async_queue_size"] = SettingDiff{
			Path:         "decision.async_queue_size",
			DefaultValue: *defaults.Decision.AsyncQueueSize,
			LocalValue:   *local.Decision.AsyncQueueSize,
		}
	}
	if local.Decision.Preprocessing != nil && defaults.Decision.Preprocessing != nil {
		lp := local.Decision.Preprocessing
		dp := defaults.Decision.Preprocessing
		if lp.MaxInputBytes != nil && dp.MaxInputBytes != nil && *lp.MaxInputBytes != *dp.MaxInputBytes {
			diffs["decision.preprocessing.max_input_bytes"] = SettingDiff{
				Path:         "decision.preprocessing.max_input_bytes",
				DefaultValue: *dp.MaxInputBytes,
				LocalValue:   *lp.MaxInputBytes,
			}
		}
		if lp.MaxAnalysisBytes != nil && dp.MaxAnalysisBytes != nil && *lp.MaxAnalysisBytes != *dp.MaxAnalysisBytes {
			diffs["decision.preprocessing.max_analysis_bytes"] = SettingDiff{
				Path:         "decision.preprocessing.max_analysis_bytes",
				DefaultValue: *dp.MaxAnalysisBytes,
				LocalValue:   *lp.MaxAnalysisBytes,
			}
		}
		if lp.MaxRepresentations != nil && dp.MaxRepresentations != nil && *lp.MaxRepresentations != *dp.MaxRepresentations {
			diffs["decision.preprocessing.max_representations"] = SettingDiff{
				Path:         "decision.preprocessing.max_representations",
				DefaultValue: *dp.MaxRepresentations,
				LocalValue:   *lp.MaxRepresentations,
			}
		}
		if lp.MaxDecodeDepth != nil && dp.MaxDecodeDepth != nil && *lp.MaxDecodeDepth != *dp.MaxDecodeDepth {
			diffs["decision.preprocessing.max_decode_depth"] = SettingDiff{
				Path:         "decision.preprocessing.max_decode_depth",
				DefaultValue: *dp.MaxDecodeDepth,
				LocalValue:   *lp.MaxDecodeDepth,
			}
		}
		if lp.MaxExpansionRatio != nil && dp.MaxExpansionRatio != nil && *lp.MaxExpansionRatio != *dp.MaxExpansionRatio {
			diffs["decision.preprocessing.max_expansion_ratio"] = SettingDiff{
				Path:         "decision.preprocessing.max_expansion_ratio",
				DefaultValue: *dp.MaxExpansionRatio,
				LocalValue:   *lp.MaxExpansionRatio,
			}
		}
	}

	return diffs
}

// mergeSettings merges local settings over defaults
func mergeSettings(defaults, local Settings) Settings {
	merged := defaults

	// Merge policy settings
	if local.Policy.Enabled != nil {
		merged.Policy.Enabled = local.Policy.Enabled
	}
	if local.Policy.Mode != nil {
		merged.Policy.Mode = local.Policy.Mode
	}
	if local.Policy.Preset != nil {
		merged.Policy.Preset = local.Policy.Preset
	}
	if len(local.Policy.DisabledRules) > 0 {
		merged.Policy.DisabledRules = local.Policy.DisabledRules
	}
	if len(local.Policy.CustomRules) > 0 {
		merged.Policy.CustomRules = local.Policy.CustomRules
	}

	// Merge risk ladder
	if local.Policy.RiskLadder != nil {
		if merged.Policy.RiskLadder == nil {
			merged.Policy.RiskLadder = &RiskLadderSettings{}
		}
		lr := local.Policy.RiskLadder
		if lr.Enabled != nil {
			merged.Policy.RiskLadder.Enabled = lr.Enabled
		}
		if lr.WarnScore != nil {
			merged.Policy.RiskLadder.WarnScore = lr.WarnScore
		}
		if lr.ThrottleScore != nil {
			merged.Policy.RiskLadder.ThrottleScore = lr.ThrottleScore
		}
		if lr.BlockScore != nil {
			merged.Policy.RiskLadder.BlockScore = lr.BlockScore
		}
		if lr.TerminateScore != nil {
			merged.Policy.RiskLadder.TerminateScore = lr.TerminateScore
		}
	}

	// Merge instruction integrity settings
	if local.Policy.InstructionIntegrity != nil {
		if merged.Policy.InstructionIntegrity == nil {
			merged.Policy.InstructionIntegrity = &InstructionIntegritySettings{}
		}
		ii := local.Policy.InstructionIntegrity
		if ii.Enabled != nil {
			merged.Policy.InstructionIntegrity.Enabled = ii.Enabled
		}
		if len(ii.TrackedTypes) > 0 {
			merged.Policy.InstructionIntegrity.TrackedTypes = ii.TrackedTypes
		}
		if ii.ShapeDetection != nil {
			merged.Policy.InstructionIntegrity.ShapeDetection = ii.ShapeDetection
		}
		if ii.ShapeConfidenceThreshold != nil {
			merged.Policy.InstructionIntegrity.ShapeConfidenceThreshold = ii.ShapeConfidenceThreshold
		}
	}

	// Merge failover settings
	if local.Failover.Enabled != nil {
		merged.Failover.Enabled = local.Failover.Enabled
	}
	if len(local.Failover.FallbackOrder) > 0 {
		merged.Failover.FallbackOrder = local.Failover.FallbackOrder
	}
	if local.Failover.MaxRetries != nil {
		merged.Failover.MaxRetries = local.Failover.MaxRetries
	}
	if local.Failover.AutoSelect != nil {
		merged.Failover.AutoSelect = local.Failover.AutoSelect
	}

	// Merge capture settings
	if local.Capture.Mode != nil {
		merged.Capture.Mode = local.Capture.Mode
	}
	if local.Capture.MaxCaptureSize != nil {
		merged.Capture.MaxCaptureSize = local.Capture.MaxCaptureSize
	}
	if local.Capture.MaxPerSession != nil {
		merged.Capture.MaxPerSession = local.Capture.MaxPerSession
	}

	// Merge decision settings
	if local.Decision.Mode != nil {
		merged.Decision.Mode = local.Decision.Mode
	}
	if local.Decision.ThresholdSet != nil {
		merged.Decision.ThresholdSet = local.Decision.ThresholdSet
	}
	if local.Decision.ElevatedThreshold != nil {
		merged.Decision.ElevatedThreshold = local.Decision.ElevatedThreshold
	}
	if local.Decision.InlineTimeoutMs != nil {
		merged.Decision.InlineTimeoutMs = local.Decision.InlineTimeoutMs
	}
	if local.Decision.MaxConcurrency != nil {
		merged.Decision.MaxConcurrency = local.Decision.MaxConcurrency
	}
	if local.Decision.MaxInlineTokens != nil {
		merged.Decision.MaxInlineTokens = local.Decision.MaxInlineTokens
	}
	if local.Decision.MaxInlineWindows != nil {
		merged.Decision.MaxInlineWindows = local.Decision.MaxInlineWindows
	}
	if local.Decision.MaxAsyncWindows != nil {
		merged.Decision.MaxAsyncWindows = local.Decision.MaxAsyncWindows
	}
	if local.Decision.AsyncQueueSize != nil {
		merged.Decision.AsyncQueueSize = local.Decision.AsyncQueueSize
	}
	if lp := local.Decision.Preprocessing; lp != nil {
		if merged.Decision.Preprocessing != nil {
			cp := *merged.Decision.Preprocessing
			merged.Decision.Preprocessing = &cp
		}
		if merged.Decision.Preprocessing == nil {
			merged.Decision.Preprocessing = &DecisionPreprocessingSettings{}
		}
		mp := merged.Decision.Preprocessing
		if lp.MaxInputBytes != nil {
			mp.MaxInputBytes = lp.MaxInputBytes
		}
		if lp.MaxAnalysisBytes != nil {
			mp.MaxAnalysisBytes = lp.MaxAnalysisBytes
		}
		if lp.MaxRepresentations != nil {
			mp.MaxRepresentations = lp.MaxRepresentations
		}
		if lp.MaxDecodeDepth != nil {
			mp.MaxDecodeDepth = lp.MaxDecodeDepth
		}
		if lp.MaxExpansionRatio != nil {
			mp.MaxExpansionRatio = lp.MaxExpansionRatio
		}
	}

	return merged
}
