package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all configuration for ELIDA
type Config struct {
	Listen    string                   `yaml:"listen"`
	Backend   string                   `yaml:"backend"`  // Single backend (backward compat)
	Backends  map[string]BackendConfig `yaml:"backends"` // Multi-backend configuration
	Routing   RoutingConfig            `yaml:"routing"`  // Routing method priority
	TLS       TLSConfig                `yaml:"tls"`      // TLS/HTTPS configuration
	Session   SessionConfig            `yaml:"session"`
	Control   ControlConfig            `yaml:"control"`
	Logging   LoggingConfig            `yaml:"logging"`
	Telemetry TelemetryConfig          `yaml:"telemetry"`
	Storage   StorageConfig            `yaml:"storage"`
	Policy    PolicyConfig             `yaml:"policy"`
	WebSocket WebSocketConfig          `yaml:"websocket"` // WebSocket proxy configuration
}

// WebSocketConfig holds WebSocket proxy configuration
type WebSocketConfig struct {
	Enabled          bool               `yaml:"enabled"`
	ReadBufferSize   int                `yaml:"read_buffer_size"`  // Buffer size for reading (default 4096)
	WriteBufferSize  int                `yaml:"write_buffer_size"` // Buffer size for writing (default 4096)
	HandshakeTimeout time.Duration      `yaml:"handshake_timeout"` // Timeout for WS handshake (default 10s)
	PingInterval     time.Duration      `yaml:"ping_interval"`     // Interval for ping frames (default 30s)
	PongTimeout      time.Duration      `yaml:"pong_timeout"`      // Timeout waiting for pong (default 60s)
	MaxMessageSize   int64              `yaml:"max_message_size"`  // Max message size in bytes (default 1MB)
	ScanTextFrames   bool               `yaml:"scan_text_frames"`  // Scan text frames with policy engine (default true)
	VoiceSessions    VoiceSessionConfig `yaml:"voice_sessions"`    // SIP-style voice session control
}

// VoiceSessionConfig holds voice session control configuration (SIP-inspired)
type VoiceSessionConfig struct {
	Enabled          bool                 `yaml:"enabled"`            // Enable voice session tracking
	MaxConcurrent    int                  `yaml:"max_concurrent"`     // Max concurrent voice sessions per WebSocket (default 1)
	CDRPerSession    bool                 `yaml:"cdr_per_session"`    // Generate CDR per voice session (not just per WebSocket)
	PolicyOnInvite   bool                 `yaml:"policy_on_invite"`   // Run policy checks at INVITE time
	AutoStartSession bool                 `yaml:"auto_start_session"` // Auto-create session on first audio frame if no INVITE
	Protocols        []string             `yaml:"protocols"`          // Enabled protocols: openai_realtime, deepgram, elevenlabs, livekit, custom
	CustomPatterns   []VoiceCustomPattern `yaml:"custom_patterns"`    // Custom INVITE/BYE patterns
}

// VoiceCustomPattern defines a custom pattern for detecting voice session control
type VoiceCustomPattern struct {
	Name    string `yaml:"name"`    // Pattern name
	Type    string `yaml:"type"`    // invite, bye, ok, hold, resume, turn_start, turn_end
	Pattern string `yaml:"pattern"` // Regex pattern to match
}

// TLSConfig holds TLS/HTTPS configuration
type TLSConfig struct {
	Enabled  bool   `yaml:"enabled"`
	CertFile string `yaml:"cert_file"` // Path to TLS certificate
	KeyFile  string `yaml:"key_file"`  // Path to TLS private key
	// Auto-generate self-signed cert for development
	AutoCert bool `yaml:"auto_cert"`
}

// StorageConfig holds persistent storage configuration
type StorageConfig struct {
	Enabled              bool   `yaml:"enabled"`
	Path                 string `yaml:"path"`                    // SQLite database path
	RetentionDays        int    `yaml:"retention_days"`          // How long to keep history
	CaptureMode          string `yaml:"capture_mode"`            // "all" or "flagged_only" (default)
	MaxCaptureSize       int    `yaml:"max_capture_size"`        // Max bytes per request/response body (default 10KB)
	MaxCapturedPerSession int    `yaml:"max_captured_per_session"` // Max request/response pairs per session (default 100)
}

// PolicyConfig holds policy engine configuration
type PolicyConfig struct {
	Enabled        bool            `yaml:"enabled"`
	Mode           string          `yaml:"mode"`             // "enforce" (default) or "audit" (dry-run)
	CaptureContent bool            `yaml:"capture_flagged"`  // Capture content for flagged sessions
	MaxCaptureSize int             `yaml:"max_capture_size"` // Max bytes to capture per request
	Preset         string          `yaml:"preset"`           // minimal, standard, or strict
	Rules          []PolicyRule    `yaml:"rules"`
	Streaming      StreamingConfig `yaml:"streaming"` // Response streaming scan configuration
}

// StreamingConfig holds streaming response scanning configuration
type StreamingConfig struct {
	// Mode: "chunked" (low latency, scan as chunks arrive) or "buffered" (full buffer for blocking rules)
	Mode string `yaml:"mode"`
	// OverlapSize: bytes to retain between chunks for cross-boundary pattern matching (default 1024)
	OverlapSize int `yaml:"overlap_size"`
	// MaxBufferSize: max bytes to buffer in buffered mode before giving up (default 10MB)
	MaxBufferSize int `yaml:"max_buffer_size"`
	// BufferTimeout: max time to wait for full response in buffered mode (default 60s)
	BufferTimeout int `yaml:"buffer_timeout"`
}

// PolicyRule defines a single policy rule
type PolicyRule struct {
	Name        string   `yaml:"name"`
	Type        string   `yaml:"type"`      // bytes_out, bytes_in, request_count, duration, requests_per_minute, content_match
	Target      string   `yaml:"target"`    // request, response, both (default: both)
	Threshold   int64    `yaml:"threshold"` // For metric rules
	Patterns    []string `yaml:"patterns"`  // For content_match rules (regex patterns)
	Severity    string   `yaml:"severity"`  // info, warning, critical
	Description string   `yaml:"description"`
	Action      string   `yaml:"action"` // flag, block, terminate (for content rules)
}

// BackendConfig defines a single backend configuration
type BackendConfig struct {
	URL     string   `yaml:"url"`
	Type    string   `yaml:"type"`    // ollama, openai, anthropic, mistral
	Models  []string `yaml:"models"`  // glob patterns: ["gpt-*", "claude-*"]
	Default bool     `yaml:"default"` // is this the default backend?
}

// RoutingConfig defines routing method priority
type RoutingConfig struct {
	Methods             []string `yaml:"methods"`               // [header, model, path, default]
	StrictModelMatching bool     `yaml:"strict_model_matching"` // Reject if model doesn't match any backend pattern
	BlockedModels       []string `yaml:"blocked_models"`        // Models to always reject (glob patterns)
}

// SessionConfig holds session-related configuration
type SessionConfig struct {
	Timeout           time.Duration   `yaml:"timeout"`
	Header            string          `yaml:"header"`
	GenerateIfMissing bool            `yaml:"generate_if_missing"`
	Store             string          `yaml:"store"` // "memory" or "redis"
	Redis             RedisConfig     `yaml:"redis"`
	KillBlock         KillBlockConfig `yaml:"kill_block"`
}

// KillBlockConfig configures how long killed sessions stay blocked
type KillBlockConfig struct {
	// Mode: "duration", "until_hour_change", or "permanent"
	Mode string `yaml:"mode"`
	// Duration to block (only used if mode is "duration")
	Duration time.Duration `yaml:"duration"`
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
	Listen  string            `yaml:"listen"`
	Enabled bool              `yaml:"enabled"`
	Auth    ControlAuthConfig `yaml:"auth"`
}

// ControlAuthConfig holds control API authentication settings
type ControlAuthConfig struct {
	Enabled bool   `yaml:"enabled"`
	APIKey  string `yaml:"api_key"` // API key for Bearer token auth
}

// LoggingConfig holds logging configuration
type LoggingConfig struct {
	Format string `yaml:"format"`
	Level  string `yaml:"level"`
}

// TelemetryConfig holds OpenTelemetry configuration
type TelemetryConfig struct {
	Enabled     bool   `yaml:"enabled"`
	Exporter    string `yaml:"exporter"` // "otlp", "stdout", or "none"
	Endpoint    string `yaml:"endpoint"` // OTLP endpoint (e.g., "localhost:4317")
	ServiceName string `yaml:"service_name"`
	Insecure    bool   `yaml:"insecure"` // Use insecure connection for OTLP
}

// Load reads and parses the configuration file
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- config path from trusted CLI flag
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

	// Apply policy preset if specified
	cfg.ApplyPolicyPreset()

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
			KillBlock: KillBlockConfig{
				Mode:     "until_hour_change", // default: blocked until hour changes
				Duration: 30 * time.Minute,    // if mode is "duration"
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
			Enabled:              false,
			Path:                 "data/elida.db",
			RetentionDays:        30,
			CaptureMode:          "flagged_only", // "flagged_only" (default) or "all" (CDR-style full audit)
			MaxCaptureSize:       10000,          // 10KB per body
			MaxCapturedPerSession: 100,            // Max 100 request/response pairs per session
		},
		TLS: TLSConfig{
			Enabled:  false,
			CertFile: "",
			KeyFile:  "",
			AutoCert: false,
		},
		Policy: PolicyConfig{
			Enabled:        false,
			CaptureContent: true,
			MaxCaptureSize: 10000, // 10KB per request
			Streaming: StreamingConfig{
				Mode:          "chunked", // Low latency by default
				OverlapSize:   1024,      // 1KB overlap for cross-chunk patterns
				MaxBufferSize: 10485760,  // 10MB max buffer
				BufferTimeout: 60,        // 60 seconds
			},
			Rules: []PolicyRule{
				{
					Name:        "large_response",
					Type:        "bytes_out",
					Threshold:   1048576, // 1MB
					Severity:    "warning",
					Description: "Session response exceeded 1MB",
				},
				{
					Name:        "high_request_count",
					Type:        "request_count",
					Threshold:   100,
					Severity:    "warning",
					Description: "Session exceeded 100 requests",
				},
				{
					Name:        "long_running",
					Type:        "duration",
					Threshold:   600, // 10 minutes in seconds
					Severity:    "warning",
					Description: "Session running longer than 10 minutes",
				},
				{
					Name:        "rate_limit",
					Type:        "requests_per_minute",
					Threshold:   30,
					Severity:    "critical",
					Description: "Session exceeding 30 requests per minute",
				},
			},
		},
		WebSocket: WebSocketConfig{
			Enabled:          false,
			ReadBufferSize:   4096,
			WriteBufferSize:  4096,
			HandshakeTimeout: 10 * time.Second,
			PingInterval:     30 * time.Second,
			PongTimeout:      60 * time.Second,
			MaxMessageSize:   1048576, // 1MB
			ScanTextFrames:   true,
			VoiceSessions: VoiceSessionConfig{
				Enabled:          true, // Enable by default when WebSocket is enabled
				MaxConcurrent:    1,
				CDRPerSession:    true,
				PolicyOnInvite:   true,
				AutoStartSession: true, // Auto-start if no explicit INVITE detected
				Protocols:        []string{"openai_realtime", "deepgram", "elevenlabs", "livekit"},
			},
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
	if v := os.Getenv("ELIDA_STORAGE_CAPTURE_MODE"); v != "" {
		c.Storage.CaptureMode = v // "all" or "flagged_only"
	}
	if v := os.Getenv("ELIDA_STORAGE_MAX_CAPTURE_SIZE"); v != "" {
		if size, err := strconv.Atoi(v); err == nil && size > 0 {
			c.Storage.MaxCaptureSize = size
		}
	}
	if v := os.Getenv("ELIDA_STORAGE_MAX_CAPTURED_PER_SESSION"); v != "" {
		if max, err := strconv.Atoi(v); err == nil && max > 0 {
			c.Storage.MaxCapturedPerSession = max
		}
	}

	// Policy overrides
	if os.Getenv("ELIDA_POLICY_ENABLED") == "true" {
		c.Policy.Enabled = true
	}
	if v := os.Getenv("ELIDA_POLICY_MODE"); v != "" {
		c.Policy.Mode = v // "enforce" or "audit"
	}
	if os.Getenv("ELIDA_POLICY_CAPTURE") == "true" {
		c.Policy.CaptureContent = true
	}
	if v := os.Getenv("ELIDA_POLICY_PRESET"); v != "" {
		c.Policy.Preset = v
	}
	if v := os.Getenv("ELIDA_POLICY_STREAMING_MODE"); v != "" {
		c.Policy.Streaming.Mode = v // "chunked" or "buffered"
	}

	// TLS overrides
	if os.Getenv("ELIDA_TLS_ENABLED") == "true" {
		c.TLS.Enabled = true
	}
	if v := os.Getenv("ELIDA_TLS_CERT_FILE"); v != "" {
		c.TLS.CertFile = v
	}
	if v := os.Getenv("ELIDA_TLS_KEY_FILE"); v != "" {
		c.TLS.KeyFile = v
	}
	if os.Getenv("ELIDA_TLS_AUTO_CERT") == "true" {
		c.TLS.AutoCert = true
	}

	// WebSocket overrides
	if os.Getenv("ELIDA_WEBSOCKET_ENABLED") == "true" {
		c.WebSocket.Enabled = true
	}
	if os.Getenv("ELIDA_WEBSOCKET_SCAN_TEXT_FRAMES") == "false" {
		c.WebSocket.ScanTextFrames = false
	}

	// Control API auth overrides
	if os.Getenv("ELIDA_CONTROL_AUTH_ENABLED") == "true" {
		c.Control.Auth.Enabled = true
	}
	if v := os.Getenv("ELIDA_CONTROL_API_KEY"); v != "" {
		c.Control.Auth.APIKey = v
		c.Control.Auth.Enabled = true // Auto-enable if key is set
	}
}

// validate checks that the configuration is valid
func (c *Config) validate() error {
	if c.Listen == "" {
		return fmt.Errorf("listen address is required")
	}
	// Either Backend (old style) or Backends (new style) must be configured
	if c.Backend == "" && len(c.Backends) == 0 {
		return fmt.Errorf("backend URL or backends configuration is required")
	}
	if c.Session.Timeout <= 0 {
		return fmt.Errorf("session timeout must be positive")
	}
	// Validate storage config
	if c.Storage.CaptureMode != "" && c.Storage.CaptureMode != "all" && c.Storage.CaptureMode != "flagged_only" {
		return fmt.Errorf("storage capture_mode must be \"all\" or \"flagged_only\", got %q", c.Storage.CaptureMode)
	}

	// Validate backends config if present
	if len(c.Backends) > 0 {
		hasDefault := false
		for name, b := range c.Backends {
			if b.URL == "" {
				return fmt.Errorf("backend %q: URL is required", name)
			}
			if b.Default {
				hasDefault = true
			}
		}
		if !hasDefault {
			return fmt.Errorf("at least one backend must be marked as default")
		}
	}
	return nil
}

// HasMultiBackend returns true if multi-backend configuration is present
func (c *Config) HasMultiBackend() bool {
	return len(c.Backends) > 0
}

// GetDefaultRoutingMethods returns the default routing method order
func GetDefaultRoutingMethods() []string {
	return []string{"header", "model", "path", "default"}
}

// ApplyPolicyPreset applies a policy preset, merging with any custom rules
func (c *Config) ApplyPolicyPreset() {
	if c.Policy.Preset == "" {
		return
	}

	var presetRules []PolicyRule
	switch c.Policy.Preset {
	case "minimal":
		presetRules = getMinimalPreset()
	case "standard":
		presetRules = getStandardPreset()
	case "strict":
		presetRules = getStrictPreset()
	default:
		return // Unknown preset, use rules as-is
	}

	// Prepend preset rules, keeping any custom rules from config
	c.Policy.Rules = append(presetRules, c.Policy.Rules...)
}

// getMinimalPreset returns basic rate limiting rules only (development/testing)
func getMinimalPreset() []PolicyRule {
	return []PolicyRule{
		{Name: "rate_limit_high", Type: "requests_per_minute", Threshold: 60, Severity: "critical", Action: "block", Description: "FIREWALL: Request rate exceeds 60/min"},
		{Name: "high_request_count", Type: "request_count", Threshold: 500, Severity: "warning", Action: "flag", Description: "FIREWALL: Session exceeded 500 requests"},
		{Name: "long_running_session", Type: "duration", Threshold: 3600, Severity: "warning", Action: "flag", Description: "FIREWALL: Session running longer than 1 hour"},
	}
}

// getStandardPreset returns OWASP basics + rate limits (production default)
func getStandardPreset() []PolicyRule {
	return []PolicyRule{
		// Rate limiting (applies to metrics, not content)
		{Name: "rate_limit_high", Type: "requests_per_minute", Threshold: 60, Severity: "critical", Action: "block", Description: "FIREWALL: Request rate exceeds 60/min"},
		{Name: "rate_limit_warning", Type: "requests_per_minute", Threshold: 30, Severity: "warning", Action: "flag", Description: "FIREWALL: Elevated request rate (30/min)"},
		{Name: "high_request_count", Type: "request_count", Threshold: 100, Severity: "warning", Action: "flag", Description: "FIREWALL: Session exceeded 100 requests"},
		{Name: "very_high_request_count", Type: "request_count", Threshold: 500, Severity: "critical", Action: "block", Description: "FIREWALL: Session exceeded 500 requests"},
		{Name: "long_running_session", Type: "duration", Threshold: 1800, Severity: "warning", Action: "flag", Description: "FIREWALL: Session running longer than 30 minutes"},
		{Name: "excessive_session_duration", Type: "duration", Threshold: 3600, Severity: "critical", Action: "block", Description: "FIREWALL: Session exceeded 1 hour"},
		{Name: "large_response", Type: "bytes_out", Threshold: 10485760, Severity: "warning", Action: "flag", Description: "FIREWALL: Large data transfer (>10MB)"},

		// OWASP LLM01 - Prompt Injection (REQUEST-SIDE)
		{Name: "prompt_injection_ignore", Type: "content_match", Target: "request", Patterns: []string{
			"ignore\\s+(all\\s+)?(previous|prior|above)\\s+(instructions|prompts|rules)",
			"disregard\\s+(all\\s+)?(previous|prior|system)\\s+(instructions|prompts)",
			"forget\\s+(all\\s+)?(previous|prior|your)\\s+(instructions|training|rules)",
		}, Severity: "critical", Action: "block", Description: "LLM01: Prompt injection - instruction override"},
		{Name: "prompt_injection_jailbreak", Type: "content_match", Target: "request", Patterns: []string{
			"you\\s+are\\s+now\\s+(DAN|a\\s+new|an?\\s+unrestricted)",
			"enable\\s+(DAN|developer|jailbreak)\\s+mode",
			"jailbreak(ed)?\\s+(mode|prompt|enabled)",
		}, Severity: "critical", Action: "terminate", Description: "LLM01: Prompt injection - jailbreak attempt"},

		// OWASP LLM02 - Insecure Output Handling (RESPONSE-SIDE)
		// NOTE: Using 'flag' action to avoid latency impact. Use 'block' only if you accept buffering latency.
		{Name: "output_script_injection", Type: "content_match", Target: "response", Patterns: []string{
			"<script[^>]*>",
			"javascript:",
			"on(click|load|error|mouseover)\\s*=",
		}, Severity: "warning", Action: "flag", Description: "LLM02: Response contains XSS patterns"},
		{Name: "output_dangerous_code", Type: "content_match", Target: "response", Patterns: []string{
			"pickle\\.loads",
			"yaml\\.unsafe_load",
			"eval\\s*\\(.*input",
			"__import__\\s*\\(",
		}, Severity: "critical", Action: "flag", Description: "LLM02: Response contains unsafe code patterns"},

		// OWASP LLM07 - Insecure Plugin Design (REQUEST-SIDE - tool call monitoring)
		{Name: "tool_code_execution", Type: "content_match", Target: "request", Patterns: []string{
			"\"function\"\\s*:\\s*\"(run|execute|eval)_code\"",
			"\"name\"\\s*:\\s*\"(code_interpreter|execute_python|run_script)\"",
			"\"type\"\\s*:\\s*\"code_interpreter\"",
		}, Severity: "critical", Action: "flag", Description: "LLM07: Tool requests code execution"},
		{Name: "tool_credential_access", Type: "content_match", Target: "request", Patterns: []string{
			"\"function\"\\s*:\\s*\"(get|read|fetch)_(secret|credential|password|key)\"",
			"\"name\"\\s*:\\s*\"(vault_read|secret_manager|get_api_key)\"",
		}, Severity: "critical", Action: "block", Description: "LLM07: Tool requests credential access"},

		// OWASP LLM08 - Excessive Agency (REQUEST-SIDE)
		{Name: "shell_execution", Type: "content_match", Target: "request", Patterns: []string{
			"(run|execute)\\s+(a\\s+)?(bash|shell|terminal)\\s+(command|script)",
			"bash\\s+-c\\s+",
			"/bin/(ba)?sh\\s+",
		}, Severity: "critical", Action: "block", Description: "LLM08: Shell execution request"},
		{Name: "destructive_file_ops", Type: "content_match", Target: "request", Patterns: []string{
			"rm\\s+(-rf?|--recursive)\\s+/",
			"rm\\s+-rf\\s+\\*",
			"(delete|remove|wipe)\\s+all\\s+(files|data|everything)",
		}, Severity: "critical", Action: "terminate", Description: "LLM08: Destructive file operation"},
		{Name: "privilege_escalation", Type: "content_match", Target: "request", Patterns: []string{
			"sudo\\s+",
			"(run|execute)\\s+(as|with)\\s+root",
			"privilege\\s+(escalation|elevation)",
		}, Severity: "critical", Action: "block", Description: "LLM08: Privilege escalation attempt"},
		{Name: "network_exfiltration", Type: "content_match", Target: "request", Patterns: []string{
			"curl.*\\|\\s*(ba)?sh",
			"wget.*\\|\\s*(ba)?sh",
			"reverse\\s+shell",
		}, Severity: "critical", Action: "terminate", Description: "LLM08: Data exfiltration attempt"},

		// OWASP LLM10 - Model Theft (REQUEST-SIDE)
		{Name: "model_extraction", Type: "content_match", Target: "request", Patterns: []string{
			"(extract|dump|export)\\s+(the\\s+)?(model|weights|parameters)",
			"(what|describe)\\s+(is|are)\\s+your\\s+(weights|parameters|architecture)",
		}, Severity: "warning", Action: "flag", Description: "LLM10: Model extraction attempt"},
	}
}

// getStrictPreset returns full OWASP + NIST + PII detection (high-security)
func getStrictPreset() []PolicyRule {
	rules := getStandardPreset()

	// Additional OWASP LLM02 - Insecure Output Handling (RESPONSE-SIDE)
	rules = append(rules, []PolicyRule{
		{Name: "output_sql_content", Type: "content_match", Target: "response", Patterns: []string{
			"(?i)(insert|update|delete|drop|alter|create)\\s+(into|from|table|database)",
			"(?i)select\\s+.+\\s+from\\s+.+\\s+where",
		}, Severity: "warning", Action: "flag", Description: "LLM02: Response contains SQL statements"},
		{Name: "output_shell_commands", Type: "content_match", Target: "response", Patterns: []string{
			"\\$\\s*\\(\\s*(curl|wget|bash|sh)\\s+",
			"&&\\s*(rm|chmod|chown|sudo)\\s+",
			"\\|\\s*(bash|sh|python|perl|ruby)\\s*$",
		}, Severity: "warning", Action: "flag", Description: "LLM02: Response contains shell commands"},
	}...)

	// Add PII detection (OWASP LLM06 - BOTH REQUEST AND RESPONSE)
	rules = append(rules, []PolicyRule{
		{Name: "pii_ssn_request", Type: "content_match", Target: "both", Patterns: []string{
			"social\\s+security\\s+(number|#)",
			"\\bssn\\b",
			"\\d{3}-\\d{2}-\\d{4}",
		}, Severity: "warning", Action: "flag", Description: "LLM06: SSN pattern detected"},
		{Name: "pii_credit_card", Type: "content_match", Target: "both", Patterns: []string{
			"credit\\s+card\\s+(number|#|info)",
			"\\bcvv\\b",
			"\\bcvc\\b",
		}, Severity: "warning", Action: "flag", Description: "LLM06: Credit card pattern detected"},
		{Name: "credentials_request", Type: "content_match", Target: "request", Patterns: []string{
			"(show|give|list|extract)\\s+(me\\s+)?(the\\s+)?api[_\\s]?key",
			"(show|give|list|extract)\\s+(me\\s+)?(the\\s+)?password",
			"(read|show|cat|display)\\s+(the\\s+)?\\.env\\s+file",
			"(list|show|dump)\\s+(all\\s+)?credentials",
		}, Severity: "warning", Action: "flag", Description: "LLM06: Credentials request"},
		{Name: "pii_bulk_extraction", Type: "content_match", Target: "request", Patterns: []string{
			"(list|show|give|extract)\\s+(all\\s+)?(user|customer|employee)\\s+(data|info|records)",
			"dump\\s+(the\\s+)?(database|user\\s+table|customer\\s+data)",
		}, Severity: "warning", Action: "flag", Description: "LLM06: Bulk data extraction request"},
	}...)

	// Additional OWASP LLM07 - Insecure Plugin Design (REQUEST-SIDE)
	rules = append(rules, []PolicyRule{
		{Name: "tool_file_access", Type: "content_match", Target: "request", Patterns: []string{
			"\"function\"\\s*:\\s*\"(read|write|delete|create)_file\"",
			"\"name\"\\s*:\\s*\"file_(read|write|delete|access)\"",
			"\"type\"\\s*:\\s*\"function\".*\"/etc/\"",
		}, Severity: "warning", Action: "flag", Description: "LLM07: Tool requests file system access"},
		{Name: "tool_network_access", Type: "content_match", Target: "request", Patterns: []string{
			"\"function\"\\s*:\\s*\"(http_request|fetch|curl|wget)\"",
			"\"name\"\\s*:\\s*\"(web_request|api_call|http_get|http_post)\"",
		}, Severity: "warning", Action: "flag", Description: "LLM07: Tool requests network access"},
		{Name: "tool_database_access", Type: "content_match", Target: "request", Patterns: []string{
			"\"function\"\\s*:\\s*\"(query|sql|database)_\"",
			"\"name\"\\s*:\\s*\"(run_sql|db_query|execute_query)\"",
		}, Severity: "warning", Action: "flag", Description: "LLM07: Tool requests database access"},
	}...)

	// Additional OWASP LLM08 - Excessive Agency (REQUEST-SIDE)
	rules = append(rules, []PolicyRule{
		{Name: "sql_injection", Type: "content_match", Target: "request", Patterns: []string{
			"drop\\s+(table|database)\\s+",
			";\\s*(drop|delete|truncate|update)\\s+",
			"union\\s+select",
			"'\\s*or\\s+'?1'?\\s*=\\s*'?1",
		}, Severity: "critical", Action: "terminate", Description: "LLM08: SQL injection attempt"},
		{Name: "network_scanning", Type: "content_match", Target: "request", Patterns: []string{
			"nmap\\s+",
			"port\\s+scan",
			"(scan|enumerate)\\s+(the\\s+)?(network|ports|hosts)",
		}, Severity: "warning", Action: "flag", Description: "LLM08: Network reconnaissance"},
	}...)

	// Additional OWASP LLM10 - Model Theft (REQUEST-SIDE)
	rules = append(rules, []PolicyRule{
		{Name: "training_data_extraction", Type: "content_match", Target: "request", Patterns: []string{
			"(what|which)\\s+(data|dataset|examples)\\s+(were|was)\\s+(you|the\\s+model)\\s+trained\\s+on",
			"(show|give|list)\\s+me\\s+(examples|samples)\\s+(from|of)\\s+(your|the)\\s+training",
			"repeat\\s+(exactly|verbatim|word\\s+for\\s+word)",
		}, Severity: "warning", Action: "flag", Description: "LLM10: Training data extraction attempt"},
		{Name: "model_replication", Type: "content_match", Patterns: []string{
			"(create|build|train|replicate)\\s+(a\\s+)?(copy|clone|replica)\\s+of\\s+(you|this\\s+model)",
			"(distill|compress|extract)\\s+(your|the\\s+model's?)\\s+(knowledge|capabilities)",
			"knowledge\\s+distillation",
		}, Severity: "warning", Action: "flag", Description: "LLM10: Model replication attempt"},
		{Name: "systematic_probing", Type: "content_match", Patterns: []string{
			"for\\s+(each|every|all)\\s+(possible|input|token|word)",
			"(test|probe|query)\\s+(all|every|each)\\s+(combination|permutation)",
			"(brute|exhaustive)\\s+(force|search|scan)",
		}, Severity: "warning", Action: "flag", Description: "LLM10: Systematic probing detected"},
	}...)

	// NIST anomaly detection
	rules = append(rules, []PolicyRule{
		{Name: "template_injection", Type: "content_match", Patterns: []string{
			"\\{\\{.*\\}\\}",
			"\\$\\{.*\\}",
			"<%.*%>",
		}, Severity: "warning", Action: "flag", Description: "NIST: Template injection pattern"},
		{Name: "encoding_evasion", Type: "content_match", Patterns: []string{
			"base64\\s+(decode|encode)",
			"\\\\x[0-9a-fA-F]{2}",
			"atob\\(|btoa\\(",
		}, Severity: "warning", Action: "flag", Description: "NIST: Encoding evasion attempt"},
	}...)

	// Stricter limits
	rules = append(rules, PolicyRule{
		Name: "excessive_data_transfer", Type: "bytes_total", Threshold: 52428800, Severity: "critical", Action: "block", Description: "FIREWALL: Excessive data transfer (>50MB)",
	})

	return rules
}
