package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// SettingsLayer identifies the source of settings
type SettingsLayer string

const (
	LayerDefault SettingsLayer = "default" // Built-in, read-only
	LayerLocal   SettingsLayer = "local"   // User customizations
)

// Settings represents all user-configurable settings
type Settings struct {
	Policy   PolicySettings   `json:"policy"`
	Failover FailoverSettings `json:"failover"`
	Capture  CaptureSettings  `json:"capture"`
}

// PolicySettings holds policy-related settings
type PolicySettings struct {
	Enabled       *bool               `json:"enabled,omitempty"`
	Mode          *string             `json:"mode,omitempty"`   // "enforce" or "audit"
	Preset        *string             `json:"preset,omitempty"` // "minimal", "standard", "strict"
	RiskLadder    *RiskLadderSettings `json:"risk_ladder,omitempty"`
	DisabledRules []string            `json:"disabled_rules,omitempty"` // Rules to skip
}

// RiskLadderSettings holds risk ladder thresholds
type RiskLadderSettings struct {
	Enabled        *bool `json:"enabled,omitempty"`
	WarnScore      *int  `json:"warn_score,omitempty"`
	ThrottleScore  *int  `json:"throttle_score,omitempty"`
	BlockScore     *int  `json:"block_score,omitempty"`
	TerminateScore *int  `json:"terminate_score,omitempty"`
}

// FailoverSettings holds failover-related settings
type FailoverSettings struct {
	Enabled       *bool    `json:"enabled,omitempty"`
	FallbackOrder []string `json:"fallback_order,omitempty"`
	MaxRetries    *int     `json:"max_retries,omitempty"`
	AutoSelect    *bool    `json:"auto_select,omitempty"` // Auto-select best match
}

// CaptureSettings holds capture-related settings
type CaptureSettings struct {
	Mode           *string `json:"mode,omitempty"` // "flagged_only" or "all"
	MaxCaptureSize *int    `json:"max_capture_size,omitempty"`
	MaxPerSession  *int    `json:"max_per_session,omitempty"`
}

// SettingsStore manages settings with layered configuration
type SettingsStore struct {
	mu       sync.RWMutex
	defaults Settings
	local    Settings
	path     string // Path to local settings file
}

// NewSettingsStore creates a new settings store
func NewSettingsStore(dataDir string) (*SettingsStore, error) {
	store := &SettingsStore{
		defaults: getDefaultSettings(),
		path:     filepath.Join(dataDir, "settings.json"),
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
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create settings directory: %w", err)
	}

	// Write to file
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	if err := os.WriteFile(s.path, data, 0644); err != nil {
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
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(data, &s.local); err != nil {
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

	return merged
}
