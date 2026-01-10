package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all configuration for ELIDA
type Config struct {
	Listen  string        `yaml:"listen"`
	Backend string        `yaml:"backend"`
	Session SessionConfig `yaml:"session"`
	Control ControlConfig `yaml:"control"`
	Logging LoggingConfig `yaml:"logging"`
}

// SessionConfig holds session-related configuration
type SessionConfig struct {
	Timeout           time.Duration `yaml:"timeout"`
	Header            string        `yaml:"header"`
	GenerateIfMissing bool          `yaml:"generate_if_missing"`
}

// ControlConfig holds control API configuration
type ControlConfig struct {
	Listen  string `yaml:"listen"`
	Enabled bool   `yaml:"enabled"`
}

// LoggingConfig holds logging configuration
type LoggingConfig struct {
	Format string `yaml:"format"`
	Level  string `yaml:"level"`
}

// Load reads and parses the configuration file
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		// Return defaults if config file doesn't exist
		if os.IsNotExist(err) {
			return defaults(), nil
		}
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	cfg := defaults()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	// Override with environment variables
	cfg.applyEnvOverrides()

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	return cfg, nil
}

// defaults returns a Config with sensible default values
func defaults() *Config {
	return &Config{
		Listen:  ":8080",
		Backend: "http://localhost:11434",
		Session: SessionConfig{
			Timeout:           5 * time.Minute,
			Header:            "X-Session-ID",
			GenerateIfMissing: true,
		},
		Control: ControlConfig{
			Listen:  ":9090",
			Enabled: true,
		},
		Logging: LoggingConfig{
			Format: "json",
			Level:  "info",
		},
	}
}

// applyEnvOverrides applies environment variable overrides
func (c *Config) applyEnvOverrides() {
	if v := os.Getenv("ELIDA_LISTEN"); v != "" {
		c.Listen = v
	}
	if v := os.Getenv("ELIDA_BACKEND"); v != "" {
		c.Backend = v
	}
	if v := os.Getenv("ELIDA_CONTROL_LISTEN"); v != "" {
		c.Control.Listen = v
	}
	if v := os.Getenv("ELIDA_LOG_LEVEL"); v != "" {
		c.Logging.Level = v
	}
}

// validate checks that the configuration is valid
func (c *Config) validate() error {
	if c.Listen == "" {
		return fmt.Errorf("listen address is required")
	}
	if c.Backend == "" {
		return fmt.Errorf("backend URL is required")
	}
	if c.Session.Timeout <= 0 {
		return fmt.Errorf("session timeout must be positive")
	}
	return nil
}
