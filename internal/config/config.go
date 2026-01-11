package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all configuration for ELIDA
type Config struct {
	Listen    string          `yaml:"listen"`
	Backend   string          `yaml:"backend"`
	Session   SessionConfig   `yaml:"session"`
	Control   ControlConfig   `yaml:"control"`
	Logging   LoggingConfig   `yaml:"logging"`
	Telemetry TelemetryConfig `yaml:"telemetry"`
	Storage   StorageConfig   `yaml:"storage"`
}

// StorageConfig holds persistent storage configuration
type StorageConfig struct {
	Enabled       bool   `yaml:"enabled"`
	Path          string `yaml:"path"`           // SQLite database path
	RetentionDays int    `yaml:"retention_days"` // How long to keep history
}

// SessionConfig holds session-related configuration
type SessionConfig struct {
	Timeout           time.Duration `yaml:"timeout"`
	Header            string        `yaml:"header"`
	GenerateIfMissing bool          `yaml:"generate_if_missing"`
	Store             string        `yaml:"store"` // "memory" or "redis"
	Redis             RedisConfig   `yaml:"redis"`
}

// RedisConfig holds Redis connection configuration
type RedisConfig struct {
	Addr      string `yaml:"addr"`
	Password  string `yaml:"password"`
	DB        int    `yaml:"db"`
	KeyPrefix string `yaml:"key_prefix"`
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

// TelemetryConfig holds OpenTelemetry configuration
type TelemetryConfig struct {
	Enabled     bool   `yaml:"enabled"`
	Exporter    string `yaml:"exporter"`     // "otlp", "stdout", or "none"
	Endpoint    string `yaml:"endpoint"`     // OTLP endpoint (e.g., "localhost:4317")
	ServiceName string `yaml:"service_name"`
	Insecure    bool   `yaml:"insecure"` // Use insecure connection for OTLP
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
			Store:             "memory",
			Redis: RedisConfig{
				Addr:      "localhost:6379",
				Password:  "",
				DB:        0,
				KeyPrefix: "elida:session:",
			},
		},
		Control: ControlConfig{
			Listen:  ":9090",
			Enabled: true,
		},
		Logging: LoggingConfig{
			Format: "json",
			Level:  "info",
		},
		Telemetry: TelemetryConfig{
			Enabled:     false,
			Exporter:    "none",
			ServiceName: "elida",
			Endpoint:    "localhost:4317",
			Insecure:    true,
		},
		Storage: StorageConfig{
			Enabled:       false,
			Path:          "data/elida.db",
			RetentionDays: 30,
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
	if v := os.Getenv("ELIDA_SESSION_STORE"); v != "" {
		c.Session.Store = v
	}
	if v := os.Getenv("ELIDA_REDIS_ADDR"); v != "" {
		c.Session.Redis.Addr = v
	}
	if v := os.Getenv("ELIDA_REDIS_PASSWORD"); v != "" {
		c.Session.Redis.Password = v
	}

	// Telemetry overrides
	if os.Getenv("ELIDA_TELEMETRY_ENABLED") == "true" {
		c.Telemetry.Enabled = true
	}
	if v := os.Getenv("ELIDA_TELEMETRY_EXPORTER"); v != "" {
		c.Telemetry.Exporter = v
	}
	if v := os.Getenv("ELIDA_TELEMETRY_ENDPOINT"); v != "" {
		c.Telemetry.Endpoint = v
	}
	if v := os.Getenv("ELIDA_TELEMETRY_SERVICE_NAME"); v != "" {
		c.Telemetry.ServiceName = v
	}
	// Also support standard OTEL env vars
	if v := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); v != "" {
		c.Telemetry.Enabled = true
		c.Telemetry.Exporter = "otlp"
		c.Telemetry.Endpoint = v
	}
	if os.Getenv("OTEL_EXPORTER_OTLP_INSECURE") == "true" {
		c.Telemetry.Insecure = true
	}

	// Storage overrides
	if os.Getenv("ELIDA_STORAGE_ENABLED") == "true" {
		c.Storage.Enabled = true
	}
	if v := os.Getenv("ELIDA_STORAGE_PATH"); v != "" {
		c.Storage.Path = v
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
