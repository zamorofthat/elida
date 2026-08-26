package config

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// envRefPattern matches ${IDENTIFIER} references in config values.
var envRefPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// nonAlphanumericPattern matches non-alphanumeric characters for replacement.
var nonAlphanumericPattern = regexp.MustCompile(`[^A-Za-z0-9]`)

// expandEnvRefs expands ${IDENTIFIER} references from the environment in a
// single config string. Field-targeted by the caller — policy regex patterns
// are never passed through here (feedback #9). Unset variables warn and stay
// literal so a missing key is visible, not silently empty.
func expandEnvRefs(s string) string {
	return envRefPattern.ReplaceAllStringFunc(s, func(m string) string {
		name := m[2 : len(m)-1]
		if v, ok := os.LookupEnv(name); ok {
			return v
		}
		slog.Warn("config references unset environment variable", "var", name)
		return m
	})
}

// autoBackendKey returns a key for a backend with an empty api_key:
// <UPPER(NAME)>_API_KEY first (non-alphanumerics -> _), then the
// conventional variable for its type. Empty if neither is set.
func autoBackendKey(name, typ string) (string, string) {
	nameVar := strings.ToUpper(nonAlphanumericPattern.ReplaceAllString(name, "_")) + "_API_KEY"
	if v := os.Getenv(nameVar); v != "" {
		return v, nameVar
	}
	typeVars := map[string]string{"openai": "OPENAI_API_KEY", "anthropic": "ANTHROPIC_API_KEY", "mistral": "MISTRAL_API_KEY"}
	if tv, ok := typeVars[typ]; ok {
		if v := os.Getenv(tv); v != "" {
			return v, tv
		}
	}
	return "", ""
}

// Config holds all configuration for ELIDA
type Config struct {
	Listen          string                   `yaml:"listen"`
	Backend         string                   `yaml:"backend"`  // Single backend (backward compat)
	Backends        map[string]BackendConfig `yaml:"backends"` // Multi-backend configuration
	Routing         RoutingConfig            `yaml:"routing"`  // Routing method priority
	TLS             TLSConfig                `yaml:"tls"`      // TLS/HTTPS configuration
	Session         SessionConfig            `yaml:"session"`
	Control         ControlConfig            `yaml:"control"`
	Proxy           ProxyConfig              `yaml:"proxy"` // Proxy authentication configuration
	Logging         LoggingConfig            `yaml:"logging"`
	Telemetry       TelemetryConfig          `yaml:"telemetry"`
	Storage         StorageConfig            `yaml:"storage"`
	Policy          PolicyConfig             `yaml:"policy"`
	WebSocket       WebSocketConfig          `yaml:"websocket"`        // WebSocket proxy configuration
	OCSF            OCSFConfig               `yaml:"ocsf"`             // OCSF native transport configuration
	Fingerprint     FingerprintConfig        `yaml:"fingerprint"`      // Behavioral fingerprint configuration
	Failover        FailoverConfig           `yaml:"failover"`         // Failover configuration
	ShutdownTimeout time.Duration            `yaml:"shutdown_timeout"` // Graceful shutdown timeout (default 30s)
}

// ProxyConfig holds proxy-level configuration
type ProxyConfig struct {
	Auth ProxyAuthConfig `yaml:"auth"`
}

// ProxyAuthConfig holds proxy authentication settings
type ProxyAuthConfig struct {
	Enabled bool   `yaml:"enabled"`
	APIKey  string `yaml:"api_key"` // API key for Bearer token or X-API-Key header auth
	// TrustedNetworks lists CIDRs whose DIRECT peers skip the API-key check
	// (e.g. loopback + the docker bridge). Lets un-keyed auxiliary agent
	// calls work on a trusted network while the LAN still needs the key.
	// The trust decision never consults X-Forwarded-For.
	TrustedNetworks []string `yaml:"trusted_networks"`
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
	Enabled               bool            `yaml:"enabled"`
	Path                  string          `yaml:"path"`                     // SQLite database path
	RetentionDays         int             `yaml:"retention_days"`           // How long to keep history
	CaptureMode           string          `yaml:"capture_mode"`             // "all" or "flagged_only" (default)
	MaxCaptureSize        int             `yaml:"max_capture_size"`         // Max bytes per request/response body (default 10KB)
	MaxCapturedPerSession int             `yaml:"max_captured_per_session"` // Max request/response pairs per session (default 100)
	MaxCapturedChunks     int             `yaml:"max_captured_chunks"`      // Max streaming chunks to capture per session (default 100); values <= 0 normalize to 100 (0 does not mean unlimited)
	Events                EventsConfig    `yaml:"events"`                   // Immutable event stream config
	Redaction             RedactionConfig `yaml:"redaction"`                // PII redaction config
}

// EventsConfig holds event stream configuration
type EventsConfig struct {
	Enabled       bool `yaml:"enabled"`
	RetentionDays int  `yaml:"retention_days"` // How long to keep events (default: 90)
}

// RedactionConfig holds redaction configuration
type RedactionConfig struct {
	Enabled          bool                     `yaml:"enabled"`
	RedactPrivateIPs bool                     `yaml:"redact_private_ips"` // also redact loopback/RFC1918 IPs (default false)
	CustomPatterns   []RedactionPatternConfig `yaml:"patterns"`
}

// RedactionPatternConfig represents a custom redaction pattern
type RedactionPatternConfig struct {
	Name        string `yaml:"name"`
	Pattern     string `yaml:"pattern"`
	Replacement string `yaml:"replacement"`
}

// PolicyConfig holds policy engine configuration
type PolicyConfig struct {
	Enabled              bool                       `yaml:"enabled"`
	Mode                 string                     `yaml:"mode"`             // "enforce" (default) or "audit" (dry-run)
	CaptureContent       bool                       `yaml:"capture_flagged"`  // Capture content for flagged sessions
	MaxCaptureSize       int                        `yaml:"max_capture_size"` // Max bytes to capture per request
	Preset               string                     `yaml:"preset"`           // minimal, standard, strict, mcp, or coding-agent
	Rules                []PolicyRule               `yaml:"rules"`
	SuppressRules        []string                   `yaml:"suppress_rules"`        // Rule names to suppress after merge (preset, custom, or generated)
	Streaming            StreamingConfig            `yaml:"streaming"`             // Response streaming scan configuration
	RiskLadder           RiskLadderConfig           `yaml:"risk_ladder"`           // Progressive escalation based on risk score
	CircuitBreaker       CircuitBreakerConfig       `yaml:"circuit_breaker"`       // Token and tool call limits
	Trust                TrustConfig                `yaml:"trust"`                 // SBC-style trust configuration
	InstructionIntegrity InstructionIntegrityConfig `yaml:"instruction_integrity"` // Instruction file integrity monitoring
}

// TrustConfig holds SBC-style trust configuration for content scanning
type TrustConfig struct {
	// TrustedTags - content within these XML-style tags is not scanned
	// Example: ["system-reminder"] skips <system-reminder>...</system-reminder>
	TrustedTags []string `yaml:"trusted_tags"`

	// TrustedIPs - client IPs/CIDRs that skip content scanning entirely
	// Example: ["10.0.0.0/8", "192.168.1.100"]
	TrustedIPs []string `yaml:"trusted_ips"`

	// TrustedHashes - SHA256 hashes of known-safe content (system prompts)
	TrustedHashes []string `yaml:"trusted_hashes"`

	// AllowlistedTools - tool names that bypass content scanning on request side
	// Example: ["Bash", "Read", "Glob"] — requests invoking these tools skip request-side rules
	AllowlistedTools []string `yaml:"allowlisted_tools"`
}

// RiskLadderConfig configures progressive escalation based on cumulative risk score
type RiskLadderConfig struct {
	Enabled    bool                  `yaml:"enabled"`
	Thresholds []RiskThresholdConfig `yaml:"thresholds"`
}

// RiskThresholdConfig defines a threshold and action for the risk ladder
type RiskThresholdConfig struct {
	Score        float64 `yaml:"score"`
	Action       string  `yaml:"action"`        // observe, warn, throttle, block, terminate
	ThrottleRate int     `yaml:"throttle_rate"` // Requests per minute when action is throttle
}

// CircuitBreakerConfig configures token and tool call limits
type CircuitBreakerConfig struct {
	Enabled             bool  `yaml:"enabled"`
	TokensPerMinute     int64 `yaml:"tokens_per_minute"`      // Block if token rate exceeds this
	MaxTokensPerSession int64 `yaml:"max_tokens_per_session"` // Block if total tokens exceed this
	MaxToolCalls        int   `yaml:"max_tool_calls"`         // Block if tool calls exceed this
	MaxToolFanout       int   `yaml:"max_tool_fanout"`        // Block if distinct tools exceed this
}

// InstructionIntegrityConfig configures instruction file integrity monitoring.
type InstructionIntegrityConfig struct {
	Enabled                  bool                    `yaml:"enabled"`
	TrackedTypes             []string                `yaml:"tracked_types"`
	ShapeDetection           bool                    `yaml:"shape_detection"`
	ShapeConfidenceThreshold float64                 `yaml:"shape_confidence_threshold"`
	AsyncQueueSize           int                     `yaml:"async_queue_size"`
	Rules                    []InstructionRuleConfig `yaml:"rules"`
}

// InstructionRuleConfig defines a single instruction-specific policy rule.
type InstructionRuleConfig struct {
	Name     string   `yaml:"name"`
	Patterns []string `yaml:"patterns"`
	Severity string   `yaml:"severity"`
	Action   string   `yaml:"action"`
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
	Name           string   `yaml:"name"`
	Type           string   `yaml:"type"`            // bytes_out, bytes_in, request_count, duration, requests_per_minute, content_match, tool_blocked, tool_argument_pattern, rate_anomaly, content_entropy
	Target         string   `yaml:"target"`          // request, response, both (default: both)
	Threshold      int64    `yaml:"threshold"`       // For metric rules
	ThresholdFloat float64  `yaml:"threshold_float"` // For probability thresholds (0-1) or entropy thresholds
	MinSamples     int      `yaml:"min_samples"`     // Minimum data points before evaluating
	Patterns       []string `yaml:"patterns"`        // For content_match (regex), tool_blocked (glob), tool_argument_pattern (regex)
	Severity       string   `yaml:"severity"`        // info, warning, critical
	Description    string   `yaml:"description"`
	Action         string   `yaml:"action"`  // flag, block, terminate (for content/tool rules)
	Observe        bool     `yaml:"observe"` // Observe only: flag + capture, never enforces, contributes 0 to the risk ladder
}

// BackendConfig defines a single backend configuration
type BackendConfig struct {
	URL     string   `yaml:"url"`
	Type    string   `yaml:"type"`    // ollama, openai, anthropic, mistral
	Models  []string `yaml:"models"`  // glob patterns: ["gpt-*", "claude-*"]
	Default bool     `yaml:"default"` // is this the default backend?
	APIKey  string   `yaml:"api_key"` // API key to inject (keeps client keyless)
	Model   string   `yaml:"model"`   // model id to substitute when FAILOVER lands on this backend (feedback #8); normal routing never rewrites
}

// RoutingConfig defines routing method priority
type RoutingConfig struct {
	Methods             []string `yaml:"methods"`               // [header, model, path, default]
	StrictModelMatching bool     `yaml:"strict_model_matching"` // Reject if model doesn't match any backend pattern
	BlockedModels       []string `yaml:"blocked_models"`        // Models to always reject (glob patterns)
}

// SessionConfig holds session-related configuration
type SessionConfig struct {
	Timeout           time.Duration       `yaml:"timeout"`
	Header            string              `yaml:"header"`
	GenerateIfMissing bool                `yaml:"generate_if_missing"`
	DeriveFrom        SessionDeriveConfig `yaml:"derive_from"` // body-derived session identity (feedback #4)
	Store             string              `yaml:"store"`       // "memory" or "redis"
	Redis             RedisConfig         `yaml:"redis"`
	KillBlock         KillBlockConfig     `yaml:"kill_block"`
}

// SessionDeriveConfig controls deriving session identity from the request body
// when no session header is present. Derived IDs contain no backend component,
// so a conversation keeps one session across backend failover.
type SessionDeriveConfig struct {
	OpenAIUser bool   `yaml:"openai_user"` // use the standard OpenAI `user` field (default true)
	BodyPath   string `yaml:"body_path"`   // optional dot-path, e.g. "metadata.conversation_id"; takes precedence over the user field
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
	Enabled       bool   `yaml:"enabled"`
	APIKey        string `yaml:"api_key"`        // API key for Bearer token auth
	AllowInsecure bool   `yaml:"allow_insecure"` // Allow non-loopback without auth
}

// LoggingConfig holds logging configuration
type LoggingConfig struct {
	Format string `yaml:"format"`
	Level  string `yaml:"level"`
}

// TelemetryConfig holds OpenTelemetry configuration
type TelemetryConfig struct {
	Enabled        bool   `yaml:"enabled"`
	Exporter       string `yaml:"exporter"` // "otlp", "stdout", or "none"
	Endpoint       string `yaml:"endpoint"` // OTLP endpoint (e.g., "localhost:4317")
	ServiceName    string `yaml:"service_name"`
	Insecure       bool   `yaml:"insecure"`        // Use insecure connection for OTLP
	CaptureContent string `yaml:"capture_content"` // "none" (default), "flagged", or "all"
	MaxBodySize    int    `yaml:"max_body_size"`   // Truncation limit for bodies (default 4096)
}

// OCSFConfig holds OCSF native transport configuration
type OCSFConfig struct {
	Enabled bool              `yaml:"enabled"`
	Stdout  OCSFStdoutConfig  `yaml:"stdout"`
	Webhook OCSFWebhookConfig `yaml:"webhook"`
	Syslog  OCSFSyslogConfig  `yaml:"syslog"`
}

// OCSFStdoutConfig configures JSONL output to stdout
type OCSFStdoutConfig struct {
	Enabled bool `yaml:"enabled"`
}

// OCSFTLSConfig holds TLS settings shared by webhook and syslog nozzles.
type OCSFTLSConfig struct {
	CAFile             string `yaml:"ca_file"`              // Path to CA bundle PEM
	CertFile           string `yaml:"cert_file"`            // Client cert PEM (mTLS)
	KeyFile            string `yaml:"key_file"`             // Client key PEM (mTLS)
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify"` // Skip server cert verification (dev only)
}

// OCSFWebhookConfig configures HTTP webhook delivery
type OCSFWebhookConfig struct {
	Enabled    bool              `yaml:"enabled"`
	URL        string            `yaml:"url"`
	Headers    map[string]string `yaml:"headers"`
	Timeout    time.Duration     `yaml:"timeout"`
	RetryCount int               `yaml:"retry_count"`
	TLS        OCSFTLSConfig     `yaml:"tls"`
}

// OCSFSyslogConfig configures syslog delivery
type OCSFSyslogConfig struct {
	Enabled  bool          `yaml:"enabled"`
	Addr     string        `yaml:"addr"`
	Protocol string        `yaml:"protocol"` // "udp", "tcp", "tcp+tls"
	Facility string        `yaml:"facility"` // syslog facility (default local0)
	Tag      string        `yaml:"tag"`      // syslog tag (default elida)
	TLS      OCSFTLSConfig `yaml:"tls"`
}

// FingerprintConfig holds behavioral fingerprint configuration
type FingerprintConfig struct {
	Enabled       bool                  `yaml:"enabled"`        // Enable behavioral fingerprinting (default: false)
	Shadow        bool                  `yaml:"shadow"`         // Shadow mode: ingest only, no scoring (default: true)
	NEff          int                   `yaml:"n_eff"`          // EWMA effective window size (default: 900)
	RidgeLambda   float64               `yaml:"ridge_lambda"`   // Ridge regularization parameter (default: 1e-6)
	WarmUp        int                   `yaml:"warm_up"`        // Min sessions before scoring (default: 100)
	FlushInterval time.Duration         `yaml:"flush_interval"` // How often to persist dirty baselines (default: 5m)
	Thresholds    FingerprintThresholds `yaml:"thresholds"`     // Score bucket thresholds (default: 3.3/4.1/5.0/6.0)
}

// FingerprintThresholds holds the bucket boundaries for the fingerprint
// anomaly score (sqrt of Mahalanobis D²). The zero value means "use defaults".
type FingerprintThresholds struct {
	Minor     float64 `yaml:"minor"`
	Notable   float64 `yaml:"notable"`
	Anomalous float64 `yaml:"anomalous"`
	Severe    float64 `yaml:"severe"`
}

// FailoverConfig holds failover configuration
type FailoverConfig struct {
	Enabled       bool          `yaml:"enabled"`        // Enable failover (default: false)
	MaxRetries    int           `yaml:"max_retries"`    // Max retry attempts (default: 2)
	RetryDelay    time.Duration `yaml:"retry_delay"`    // Delay between retries (default: 0)
	FallbackOrder []string      `yaml:"fallback_order"` // Order of backends to try on failure
	PreserveModel bool          `yaml:"preserve_model"` // Preserve model ID across failovers
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
	defaultRules := cfg.Policy.Rules
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	// If the user's YAML has no `policy.rules:` key, yaml.Unmarshal leaves
	// cfg.Policy.Rules pointing at the exact slice defaults() built above.
	// ApplyPolicyPreset() treats any rule already in Policy.Rules as a
	// user-authored override that REPLACES the same-named preset rule, so
	// left untouched those generic built-in defaults (e.g. high_request_count
	// threshold 100) would silently clobber preset-tuned values (e.g. 2000
	// for coding-agent). Detect "still the defaults" via slice identity
	// (same length, same backing array) and clear it so a preset only meets
	// rules the user actually wrote. A user who explicitly writes
	// `rules: []` gets a distinct empty slice from yaml and is unaffected.
	if cfg.Policy.Preset != "" && samePolicyRulesSlice(cfg.Policy.Rules, defaultRules) {
		cfg.Policy.Rules = nil
	}

	// Override with environment variables
	cfg.applyEnvOverrides()

	// Expand environment variable references and auto-load provider keys
	cfg.Backend = expandEnvRefs(cfg.Backend)
	cfg.Proxy.Auth.APIKey = expandEnvRefs(cfg.Proxy.Auth.APIKey)
	cfg.Control.Auth.APIKey = expandEnvRefs(cfg.Control.Auth.APIKey)
	for name, b := range cfg.Backends {
		b.URL = expandEnvRefs(b.URL)
		b.APIKey = expandEnvRefs(b.APIKey)
		if b.APIKey == "" {
			if key, envVar := autoBackendKey(name, b.Type); key != "" {
				b.APIKey = key
				slog.Info("backend api_key loaded from environment", "backend", name, "env_var", envVar)
			}
		} else if strings.Contains(b.APIKey, "${") {
			// expandEnvRefs already warned generically about the unset
			// variable; this backend-specific warning calls out that the
			// unexpanded "${VAR}" literal is about to be sent to the
			// backend as-is - a credential-shaped value, never logged here.
			unsetVar := "unknown"
			if m := envRefPattern.FindStringSubmatch(b.APIKey); len(m) > 1 {
				unsetVar = m[1]
			}
			slog.Warn("backend api_key references unset environment variable and will be sent as a literal credential",
				"backend", name, "var", unsetVar)
		}
		cfg.Backends[name] = b
	}

	// Apply policy preset if specified
	cfg.ApplyPolicyPreset()

	// Normalize storage.max_captured_chunks: values <= 0 become 100
	if cfg.Storage.MaxCapturedChunks <= 0 {
		cfg.Storage.MaxCapturedChunks = 100
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	return cfg, nil
}

// samePolicyRulesSlice reports whether a and b are the same underlying
// slice (identical length and, when non-empty, identical backing array).
// Used to detect whether yaml.Unmarshal left Policy.Rules untouched from
// defaults(), i.e. the user's config had no "policy.rules:" key, as
// opposed to an explicit "rules: []", which yaml allocates as a distinct
// (but also empty) slice.
func samePolicyRulesSlice(a, b []PolicyRule) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return false
	}
	return &a[0] == &b[0]
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
			DeriveFrom:        SessionDeriveConfig{OpenAIUser: true},
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
			Listen:  "127.0.0.1:9090",
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
			Enabled:               false,
			Path:                  "data/elida.db",
			RetentionDays:         30,
			CaptureMode:           "flagged_only", // "flagged_only" (default) or "all" (CDR-style full audit)
			MaxCaptureSize:        10000,          // 10KB per body
			MaxCapturedPerSession: 100,            // Max 100 request/response pairs per session
			MaxCapturedChunks:     100,            // Max 100 streaming chunks per session
			Redaction:             RedactionConfig{Enabled: true},
		},
		OCSF: OCSFConfig{
			Enabled: false,
			Webhook: OCSFWebhookConfig{
				Timeout:    10 * time.Second,
				RetryCount: 2,
			},
			Syslog: OCSFSyslogConfig{
				Facility: "local0",
				Tag:      "elida",
			},
		},
		Fingerprint: FingerprintConfig{
			Enabled:       false,
			Shadow:        true,
			NEff:          900,
			RidgeLambda:   1e-6,
			WarmUp:        100,
			FlushInterval: 5 * time.Minute,
			Thresholds: FingerprintThresholds{
				Minor:     3.3,
				Notable:   4.1,
				Anomalous: 5.0,
				Severe:    6.0,
			},
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
			CircuitBreaker: CircuitBreakerConfig{
				Enabled:             true,
				TokensPerMinute:     50000,   // ~$1/min at Claude pricing
				MaxTokensPerSession: 1000000, // 1M tokens before cutoff
				MaxToolCalls:        500,     // Normal session: 50-200
				MaxToolFanout:       30,      // Most sessions: 5-15 distinct tools
			},
			RiskLadder: RiskLadderConfig{
				Enabled: true,
				Thresholds: []RiskThresholdConfig{
					{Score: 5, Action: "warn"},
					{Score: 15, Action: "throttle", ThrottleRate: 10},
					{Score: 30, Action: "block"},
					{Score: 50, Action: "terminate"},
				},
			},
			Streaming: StreamingConfig{
				Mode:          "chunked", // Low latency by default
				OverlapSize:   1024,      // 1KB overlap for cross-chunk patterns
				MaxBufferSize: 10485760,  // 10MB max buffer
				BufferTimeout: 60,        // 60 seconds
			},
			Trust: TrustConfig{
				// Default trusted tags for Claude Code compatibility
				// Content within <system-reminder>...</system-reminder> is not scanned
				TrustedTags: []string{"system-reminder"},
			},
			InstructionIntegrity: InstructionIntegrityConfig{
				Enabled:                  true,
				TrackedTypes:             []string{"claude_md", "cursorrules", "cursor_rules", "agents_md", "windsurfrules"},
				ShapeDetection:           true,
				ShapeConfidenceThreshold: 0.7,
				AsyncQueueSize:           100,
				Rules: []InstructionRuleConfig{
					{Name: "instruction_shell_exec", Patterns: []string{`curl\s+[^|]*\|\s*(ba)?sh`, `wget\s+[^|]*\|\s*(ba)?sh`, `eval\s*\(`, `exec\s*\(`}, Severity: "critical", Action: "block"},
					{Name: "instruction_prompt_injection", Patterns: []string{`ignore\s+(all\s+)?previous`, `you\s+are\s+now`, `disregard`}, Severity: "critical", Action: "block"},
					{Name: "instruction_permission_escalation", Patterns: []string{`always\s+approve`, `never\s+ask.*confirmation`, `auto.?accept`}, Severity: "high", Action: "flag"},
					{Name: "instruction_hidden_content", Patterns: []string{"[\\x{200B}-\\x{200F}]", "[\\x{202A}-\\x{202E}]"}, Severity: "critical", Action: "block"},
					{Name: "instruction_obfuscation", Patterns: []string{`base64\s*decode`, `[A-Za-z0-9+/]{50,}={0,2}`}, Severity: "high", Action: "flag"},
					{Name: "instruction_tool_manipulation", Patterns: []string{`always\s+use\s+tool`, `redirect.*to`, `prefer\s+tool`}, Severity: "medium", Action: "flag"},
					{Name: "instruction_exfil_urls", Patterns: []string{`https?://[^\s]+\s*\|`, `fetch\s+https?://`, `post\s+to\s+https?://`}, Severity: "high", Action: "flag"},
				},
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
		Failover: FailoverConfig{
			Enabled:    false,
			MaxRetries: 2,
			RetryDelay: 0,
		},
		ShutdownTimeout: 30 * time.Second,
	}
}

// DefaultConfig returns the default configuration. It is the public equivalent of defaults().
func DefaultConfig() *Config {
	return defaults()
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

	// OCSF overrides
	if os.Getenv("ELIDA_OCSF_ENABLED") == "true" {
		c.OCSF.Enabled = true
	}
	if os.Getenv("ELIDA_OCSF_STDOUT_ENABLED") == "true" {
		c.OCSF.Stdout.Enabled = true
	}
	if os.Getenv("ELIDA_OCSF_WEBHOOK_ENABLED") == "true" {
		c.OCSF.Webhook.Enabled = true
	}
	if v := os.Getenv("ELIDA_OCSF_WEBHOOK_URL"); v != "" {
		c.OCSF.Webhook.URL = v
	}
	if os.Getenv("ELIDA_OCSF_SYSLOG_ENABLED") == "true" {
		c.OCSF.Syslog.Enabled = true
	}
	if v := os.Getenv("ELIDA_OCSF_SYSLOG_ADDR"); v != "" {
		c.OCSF.Syslog.Addr = v
	}
	if v := os.Getenv("ELIDA_OCSF_SYSLOG_PROTOCOL"); v != "" {
		c.OCSF.Syslog.Protocol = v
	}

	// OCSF TLS overrides — webhook
	if v := os.Getenv("ELIDA_OCSF_WEBHOOK_CA_FILE"); v != "" {
		c.OCSF.Webhook.TLS.CAFile = v
	}
	if v := os.Getenv("ELIDA_OCSF_WEBHOOK_CERT_FILE"); v != "" {
		c.OCSF.Webhook.TLS.CertFile = v
	}
	if v := os.Getenv("ELIDA_OCSF_WEBHOOK_KEY_FILE"); v != "" {
		c.OCSF.Webhook.TLS.KeyFile = v
	}
	if os.Getenv("ELIDA_OCSF_WEBHOOK_INSECURE") == "true" {
		c.OCSF.Webhook.TLS.InsecureSkipVerify = true
	}
	// OCSF TLS overrides — syslog
	if v := os.Getenv("ELIDA_OCSF_SYSLOG_CA_FILE"); v != "" {
		c.OCSF.Syslog.TLS.CAFile = v
	}
	if v := os.Getenv("ELIDA_OCSF_SYSLOG_CERT_FILE"); v != "" {
		c.OCSF.Syslog.TLS.CertFile = v
	}
	if v := os.Getenv("ELIDA_OCSF_SYSLOG_KEY_FILE"); v != "" {
		c.OCSF.Syslog.TLS.KeyFile = v
	}
	if os.Getenv("ELIDA_OCSF_SYSLOG_INSECURE") == "true" {
		c.OCSF.Syslog.TLS.InsecureSkipVerify = true
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
	if os.Getenv("ELIDA_CONTROL_AUTH_ALLOW_INSECURE") == "true" {
		c.Control.Auth.AllowInsecure = true
	}

	// Proxy auth overrides
	if os.Getenv("ELIDA_PROXY_AUTH_ENABLED") == "true" {
		c.Proxy.Auth.Enabled = true
	}
	if v := os.Getenv("ELIDA_PROXY_API_KEY"); v != "" {
		c.Proxy.Auth.APIKey = v
		c.Proxy.Auth.Enabled = true // Auto-enable if key is set
	}

	// Shutdown timeout override
	if v := os.Getenv("ELIDA_SHUTDOWN_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			c.ShutdownTimeout = d
		}
	}
}

// ValidationError represents a single validation error with context
type ValidationError struct {
	Field   string
	Message string
	Hint    string
}

func (e ValidationError) String() string {
	if e.Hint != "" {
		return fmt.Sprintf("%s: %s (%s)", e.Field, e.Message, e.Hint)
	}
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// ValidationResult holds the result of config validation
type ValidationResult struct {
	Valid   bool
	Errors  []ValidationError
	Summary ConfigSummary
}

// ConfigSummary provides a quick overview of the configuration
type ConfigSummary struct {
	Listen           string
	BackendCount     int
	DefaultBackend   string
	PolicyEnabled    bool
	PolicyPreset     string
	PolicyRules      int
	StorageEnabled   bool
	CaptureMode      string
	TLSEnabled       bool
	WebSocketEnabled bool
}

// Error returns all validation errors as a single error
func (r *ValidationResult) Error() error {
	if r.Valid {
		return nil
	}
	var msgs []string
	for _, e := range r.Errors {
		msgs = append(msgs, e.String())
	}
	return fmt.Errorf("configuration invalid:\n  - %s", strings.Join(msgs, "\n  - "))
}

// validate checks that the configuration is valid
func (c *Config) validate() error {
	result := c.Validate()
	return result.Error()
}

// Validate performs comprehensive configuration validation
func (c *Config) Validate() *ValidationResult {
	result := &ValidationResult{Valid: true}
	var errors []ValidationError

	// Required: listen address
	if c.Listen == "" {
		errors = append(errors, ValidationError{
			Field:   "listen",
			Message: "address is required",
			Hint:    "e.g., :8080 or 0.0.0.0:8080",
		})
	} else if !strings.Contains(c.Listen, ":") {
		errors = append(errors, ValidationError{
			Field:   "listen",
			Message: fmt.Sprintf("%q is not a valid address", c.Listen),
			Hint:    "must include port, e.g., :8080",
		})
	}

	// Required: backend configuration
	if c.Backend == "" && len(c.Backends) == 0 {
		errors = append(errors, ValidationError{
			Field:   "backend/backends",
			Message: "no backend configured",
			Hint:    "set 'backend' URL or configure 'backends' map",
		})
	}

	// Validate single backend URL
	if c.Backend != "" {
		if _, err := url.Parse(c.Backend); err != nil {
			errors = append(errors, ValidationError{
				Field:   "backend",
				Message: fmt.Sprintf("invalid URL %q", c.Backend),
				Hint:    "must be valid URL like https://api.openai.com",
			})
		}
	}

	// Validate multi-backend config
	var defaultBackend string
	if len(c.Backends) > 0 {
		hasDefault := false
		for name, b := range c.Backends {
			if b.URL == "" {
				errors = append(errors, ValidationError{
					Field:   fmt.Sprintf("backends.%s.url", name),
					Message: "URL is required",
				})
			} else if parsed, err := url.Parse(b.URL); err != nil {
				errors = append(errors, ValidationError{
					Field:   fmt.Sprintf("backends.%s.url", name),
					Message: fmt.Sprintf("invalid URL %q", b.URL),
					Hint:    "must start with http:// or https://",
				})
			} else if parsed.Scheme != "http" && parsed.Scheme != "https" {
				errors = append(errors, ValidationError{
					Field:   fmt.Sprintf("backends.%s.url", name),
					Message: fmt.Sprintf("unsupported scheme %q", parsed.Scheme),
					Hint:    "must be http or https",
				})
			}
			if b.Default {
				hasDefault = true
				defaultBackend = name
			}
		}
		if !hasDefault {
			errors = append(errors, ValidationError{
				Field:   "backends",
				Message: "no default backend specified",
				Hint:    "set 'default: true' on one backend",
			})
		}
	}

	// Session config
	if c.Session.Timeout <= 0 {
		errors = append(errors, ValidationError{
			Field:   "session.timeout",
			Message: fmt.Sprintf("%v is invalid", c.Session.Timeout),
			Hint:    "must be positive duration, e.g., 5m",
		})
	}

	// Storage config
	if c.Storage.CaptureMode != "" && c.Storage.CaptureMode != "all" && c.Storage.CaptureMode != "flagged_only" {
		errors = append(errors, ValidationError{
			Field:   "storage.capture_mode",
			Message: fmt.Sprintf("%q is invalid", c.Storage.CaptureMode),
			Hint:    "must be \"all\" or \"flagged_only\"",
		})
	}

	// Fingerprint score bucket thresholds: zero value means "use defaults";
	// otherwise all four must be positive and strictly increasing.
	if ft := c.Fingerprint.Thresholds; ft != (FingerprintThresholds{}) {
		if ft.Minor <= 0 || ft.Notable <= 0 || ft.Anomalous <= 0 || ft.Severe <= 0 {
			errors = append(errors, ValidationError{
				Field:   "fingerprint.thresholds",
				Message: "minor, notable, anomalous, and severe must all be positive when set",
				Hint:    "omit the thresholds block to use the defaults",
			})
		} else if !(ft.Minor < ft.Notable && ft.Notable < ft.Anomalous && ft.Anomalous < ft.Severe) {
			errors = append(errors, ValidationError{
				Field: "fingerprint.thresholds",
				Message: fmt.Sprintf("thresholds must be strictly increasing, got minor=%v notable=%v anomalous=%v severe=%v",
					ft.Minor, ft.Notable, ft.Anomalous, ft.Severe),
				Hint: "minor < notable < anomalous < severe",
			})
		}
	}

	// Policy config
	if c.Policy.Mode != "" && c.Policy.Mode != "enforce" && c.Policy.Mode != "audit" {
		errors = append(errors, ValidationError{
			Field:   "policy.mode",
			Message: fmt.Sprintf("%q is invalid", c.Policy.Mode),
			Hint:    "must be \"enforce\" or \"audit\"",
		})
	}

	// Control API auth
	if c.Control.Auth.Enabled && c.Control.Auth.APIKey == "" {
		errors = append(errors, ValidationError{
			Field:   "control.auth.api_key",
			Message: "API key required when auth is enabled",
			Hint:    "set ELIDA_CONTROL_API_KEY env var",
		})
	}

	// Proxy auth
	if c.Proxy.Auth.Enabled && c.Proxy.Auth.APIKey == "" {
		errors = append(errors, ValidationError{
			Field:   "proxy.auth.api_key",
			Message: "API key required when proxy auth is enabled",
			Hint:    "set ELIDA_PROXY_API_KEY env var",
		})
	}

	for _, cidr := range c.Proxy.Auth.TrustedNetworks {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			errors = append(errors, ValidationError{
				Field:   "proxy.auth.trusted_networks",
				Message: fmt.Sprintf("invalid CIDR %q: %v", cidr, err),
			})
		}
	}

	// OCSF webhook validation
	if c.OCSF.Webhook.Enabled && c.OCSF.Webhook.URL != "" {
		if u, err := url.Parse(c.OCSF.Webhook.URL); err != nil {
			errors = append(errors, ValidationError{
				Field:   "ocsf.webhook.url",
				Message: fmt.Sprintf("invalid URL %q", c.OCSF.Webhook.URL),
			})
		} else if u.Scheme == "http" && !c.OCSF.Webhook.TLS.InsecureSkipVerify {
			errors = append(errors, ValidationError{
				Field:   "ocsf.webhook.url",
				Message: "plain HTTP webhook URL is insecure",
				Hint:    "use https:// or set ocsf.webhook.tls.insecure_skip_verify: true",
			})
		}
	}
	// OCSF TLS cert/key pairing (webhook)
	if (c.OCSF.Webhook.TLS.CertFile != "") != (c.OCSF.Webhook.TLS.KeyFile != "") {
		errors = append(errors, ValidationError{
			Field:   "ocsf.webhook.tls",
			Message: "cert_file and key_file must both be set for mTLS",
		})
	}
	if c.OCSF.Webhook.TLS.CAFile != "" {
		if _, err := os.Stat(c.OCSF.Webhook.TLS.CAFile); err != nil {
			errors = append(errors, ValidationError{
				Field:   "ocsf.webhook.tls.ca_file",
				Message: fmt.Sprintf("CA file not found: %s", c.OCSF.Webhook.TLS.CAFile),
			})
		}
	}
	// OCSF TLS cert/key pairing (syslog)
	if (c.OCSF.Syslog.TLS.CertFile != "") != (c.OCSF.Syslog.TLS.KeyFile != "") {
		errors = append(errors, ValidationError{
			Field:   "ocsf.syslog.tls",
			Message: "cert_file and key_file must both be set for mTLS",
		})
	}
	if c.OCSF.Syslog.TLS.CAFile != "" {
		if _, err := os.Stat(c.OCSF.Syslog.TLS.CAFile); err != nil {
			errors = append(errors, ValidationError{
				Field:   "ocsf.syslog.tls.ca_file",
				Message: fmt.Sprintf("CA file not found: %s", c.OCSF.Syslog.TLS.CAFile),
			})
		}
	}

	// Build result
	result.Errors = errors
	result.Valid = len(errors) == 0

	// Build summary
	result.Summary = ConfigSummary{
		Listen:           c.Listen,
		BackendCount:     len(c.Backends),
		DefaultBackend:   defaultBackend,
		PolicyEnabled:    c.Policy.Enabled,
		PolicyPreset:     c.Policy.Preset,
		PolicyRules:      len(c.Policy.Rules),
		StorageEnabled:   c.Storage.Enabled,
		CaptureMode:      c.Storage.CaptureMode,
		TLSEnabled:       c.TLS.Enabled,
		WebSocketEnabled: c.WebSocket.Enabled,
	}
	if result.Summary.BackendCount == 0 && c.Backend != "" {
		result.Summary.BackendCount = 1
		result.Summary.DefaultBackend = "default"
	}
	if result.Summary.CaptureMode == "" {
		result.Summary.CaptureMode = "flagged_only"
	}

	return result
}

// HasMultiBackend returns true if multi-backend configuration is present
func (c *Config) HasMultiBackend() bool {
	return len(c.Backends) > 0
}

// GetDefaultRoutingMethods returns the default routing method order
func GetDefaultRoutingMethods() []string {
	return []string{"header", "model", "path", "default"}
}

// ApplyPolicyPreset applies a policy preset with local-overrides-default
// layering: a custom rule with the same name as a preset rule REPLACES the
// preset rule (like Splunk/Cribl local vs default configs). Rules named in
// policy.suppress_rules are dropped after the merge — this also covers
// generated circuit-breaker rules.
func (c *Config) ApplyPolicyPreset() {
	var presetRules []PolicyRule
	switch c.Policy.Preset {
	case "":
		// no preset — custom rules only
	case "minimal":
		presetRules = getMinimalPreset()
	case "standard":
		presetRules = getStandardPreset()
	case "strict":
		presetRules = getStrictPreset()
	case "mcp":
		presetRules = getMCPPreset()
	case "coding-agent":
		presetRules = getCodingAgentPreset()
	default:
		slog.Warn("unknown policy preset, using custom rules only", "preset", c.Policy.Preset)
	}

	// Local overrides default: same-named custom rule replaces the preset rule.
	customNames := make(map[string]bool, len(c.Policy.Rules))
	for _, r := range c.Policy.Rules {
		customNames[r.Name] = true
	}
	var overridden []string
	merged := make([]PolicyRule, 0, len(presetRules)+len(c.Policy.Rules))
	for _, pr := range presetRules {
		if customNames[pr.Name] {
			overridden = append(overridden, pr.Name)
			continue
		}
		merged = append(merged, pr)
	}
	merged = append(merged, c.Policy.Rules...)
	c.Policy.Rules = merged

	// Generate rules from circuit breaker config (if enabled)
	if c.Policy.CircuitBreaker.Enabled {
		cb := c.Policy.CircuitBreaker
		if cb.TokensPerMinute > 0 {
			c.Policy.Rules = append(c.Policy.Rules, PolicyRule{
				Name: "circuit_breaker_tokens_per_min", Type: "tokens_per_minute",
				Threshold: cb.TokensPerMinute, Severity: "critical", Action: "block",
				Description: "Circuit breaker: token rate exceeded",
			})
		}
		if cb.MaxTokensPerSession > 0 {
			c.Policy.Rules = append(c.Policy.Rules, PolicyRule{
				Name: "circuit_breaker_max_tokens", Type: "tokens_total",
				Threshold: cb.MaxTokensPerSession, Severity: "critical", Action: "block",
				Description: "Circuit breaker: session token budget exceeded",
			})
		}
		if cb.MaxToolCalls > 0 {
			c.Policy.Rules = append(c.Policy.Rules, PolicyRule{
				Name: "circuit_breaker_tool_calls", Type: "tool_call_count",
				Threshold: int64(cb.MaxToolCalls), Severity: "critical", Action: "block",
				Description: "Circuit breaker: tool call limit exceeded",
			})
		}
		if cb.MaxToolFanout > 0 {
			c.Policy.Rules = append(c.Policy.Rules, PolicyRule{
				Name: "circuit_breaker_tool_fanout", Type: "tool_fanout",
				Threshold: int64(cb.MaxToolFanout), Severity: "warning", Action: "flag",
				Description: "Circuit breaker: distinct tool limit exceeded",
			})
		}
	}

	// Drop rules named in suppress_rules (applies to preset, custom, and
	// generated rules alike).
	if len(c.Policy.SuppressRules) > 0 {
		suppressed := make(map[string]bool, len(c.Policy.SuppressRules))
		for _, name := range c.Policy.SuppressRules {
			suppressed[name] = true
		}
		kept := c.Policy.Rules[:0]
		var suppressedRules []string
		for _, r := range c.Policy.Rules {
			if suppressed[r.Name] {
				suppressedRules = append(suppressedRules, r.Name)
				continue
			}
			kept = append(kept, r)
		}
		c.Policy.Rules = kept
		if len(suppressedRules) > 0 {
			slog.Info("policy rules suppressed by config", "rules", suppressedRules)
		}
	}

	if len(overridden) > 0 {
		slog.Info("preset rules overridden by custom rules", "preset", c.Policy.Preset, "rules", overridden)
	}

	// Observe-only rules never enforce — normalize their action to flag so
	// a misconfigured observe+block rule can never enforce.
	for i := range c.Policy.Rules {
		if c.Policy.Rules[i].Observe {
			c.Policy.Rules[i].Action = "flag"
		}
	}
}

// getMinimalPreset returns basic rate limiting rules only (development/testing)
func getMinimalPreset() []PolicyRule {
	return []PolicyRule{
		{Name: "rate_limit_high", Type: "requests_per_minute", Threshold: 60, Severity: "critical", Action: "block", Description: "FIREWALL: Request rate exceeds 60/min"},
		{Name: "high_request_count", Type: "request_count", Threshold: 500, Severity: "warning", Action: "flag", Description: "FIREWALL: Session exceeded 500 requests"},
		{Name: "long_running_session", Type: "duration", Threshold: 3600, Severity: "warning", Action: "flag", Description: "FIREWALL: Session running longer than 1 hour"},
	}
}

// getCodingAgentPreset returns a policy tuned for trusted coding agents
// (Claude Code, Hermes, Cursor): deterministic structural rules ENFORCE,
// content/statistical heuristics run in OBSERVE — flagged and captured but
// never blocking and never feeding the risk ladder. Rationale: coding
// agents legitimately emit bash -c / sudo / rm -rf / curl|sh in their own
// output, and their rapid tool loops look like high-rate high-entropy
// bursts to anomaly detectors (integration feedback #3).
func getCodingAgentPreset() []PolicyRule {
	return []PolicyRule{
		// ---- Enforced: structural, can't false-fire on agent output ----
		{Name: "block_dangerous_tools", Type: "tool_blocked", Target: "response", Patterns: []string{
			"exec_*", "shell_*", "rm_*", "sudo_*", "eval_*",
		}, Severity: "critical", Action: "block", Description: "LLM07: Block dangerous tool calls"},
		{Name: "dangerous_tool_arguments", Type: "tool_argument_pattern", Target: "response", Patterns: []string{
			"rm\\s+-rf\\s+/",
			"chmod\\s+777\\s+/",
			"curl\\s+[^|]*\\|\\s*(ba)?sh",
		}, Severity: "critical", Action: "block", Description: "LLM08: Dangerous patterns in tool arguments"},
		{Name: "tool_credential_access", Type: "content_match", Target: "request", Patterns: []string{
			"\"function\"\\s*:\\s*\"(get|read|fetch)_(secret|credential|password|key)\"",
			"\"name\"\\s*:\\s*\"(vault_read|secret_manager|get_api_key)\"",
		}, Severity: "critical", Action: "block", Description: "LLM07: Tool requests credential access"},

		// ---- Enforced: generous runaway limits (coding sessions are long) ----
		{Name: "rate_limit_high", Type: "requests_per_minute", Threshold: 120, Severity: "critical", Action: "block", Description: "FIREWALL: Request rate exceeds 120/min"},
		{Name: "high_request_count", Type: "request_count", Threshold: 2000, Severity: "warning", Action: "flag", Description: "FIREWALL: Session exceeded 2000 requests"},

		// ---- Observe: heuristics that false-fire on legitimate agent output ----
		{Name: "shell_execution", Type: "content_match", Target: "both", Observe: true, Patterns: []string{
			"(run|execute)\\s+(a\\s+)?(bash|shell|terminal)\\s+(command|script)",
			"bash\\s+-c\\s+",
			"/bin/(ba)?sh\\s+",
		}, Severity: "warning", Action: "flag", Description: "LLM08: Shell execution pattern (observe)"},
		{Name: "privilege_escalation", Type: "content_match", Target: "both", Observe: true, Patterns: []string{
			"sudo\\s+(rm|chmod|chown|kill|bash|sh|python|perl|ruby|apt|yum|dnf|pip|npm|make|gcc|curl|wget)\\b",
			"(run|execute)\\s+(this\\s+)?(command\\s+)?(as|with)\\s+root",
			"(get|gain|obtain)\\s+(root|admin|superuser)\\s+(access|privileges|permissions)",
		}, Severity: "warning", Action: "flag", Description: "LLM08: Privilege escalation pattern (observe)"},
		{Name: "destructive_file_ops", Type: "content_match", Target: "both", Observe: true, Patterns: []string{
			"rm\\s+(-rf?|--recursive)\\s+/",
			"rm\\s+-rf\\s+\\*",
			"(delete|remove|wipe)\\s+all\\s+(files|data|everything)",
		}, Severity: "warning", Action: "flag", Description: "LLM08: Destructive file operation (observe)"},
		{Name: "network_exfiltration", Type: "content_match", Target: "both", Observe: true, Patterns: []string{
			"curl\\s+[^|]*\\|\\s*(ba)?sh",
			"wget\\s+[^|]*\\|\\s*(ba)?sh",
			"reverse\\s+shell",
		}, Severity: "warning", Action: "flag", Description: "LLM08: Piped-download pattern (observe)"},
		{Name: "prompt_injection_ignore", Type: "content_match", Target: "both", Observe: true, Patterns: []string{
			"ignore\\s+(all\\s+)?(previous|prior|above|your)\\s+(instructions|prompts|rules)",
			"disregard\\s+(all\\s+)?(your\\s+)?(previous|prior|system)\\s+(instructions|prompts)",
			"forget\\s+(all\\s+)?(previous|prior|your)\\s+(instructions|training|rules)",
		}, Severity: "warning", Action: "flag", Description: "LLM01: Prompt injection pattern (observe)"},
		{Name: "pii_ssn_request", Type: "content_match", Target: "both", Observe: true, Patterns: []string{
			"social\\s+security\\s+(number|#)",
			"\\bssn\\b",
			"\\d{3}-\\d{2}-\\d{4}",
		}, Severity: "warning", Action: "flag", Description: "LLM06: SSN pattern (observe)"},
		{Name: "pii_credit_card", Type: "content_match", Target: "both", Observe: true, Patterns: []string{
			"credit\\s+card\\s+(number|#|info)",
			"\\bcvv\\b",
			"\\bcvc\\b",
		}, Severity: "warning", Action: "flag", Description: "LLM06: Credit card pattern (observe)"},

		// ---- Observe: statistical anomalies (measured noise on agent tool loops) ----
		{Name: "rate_anomaly", Type: "rate_anomaly", Observe: true, Severity: "warning", Action: "flag",
			ThresholdFloat: 0.01, MinSamples: 10, Description: "ANOMALY: Request rate statistically abnormal (observe)"},
		{Name: "compound_anomaly", Type: "compound_anomaly", Observe: true, Severity: "warning", Action: "flag",
			ThresholdFloat: 0.15, MinSamples: 5, Description: "ANOMALY: Sustained high-rate + high-entropy burst (elevated rate/entropy signal, observe)"},
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

		// Statistical anomaly detection
		{Name: "rate_anomaly", Type: "rate_anomaly", Severity: "warning", Description: "ANOMALY: Request rate statistically abnormal (p<0.01)", Action: "flag", ThresholdFloat: 0.01, MinSamples: 10},
		{Name: "compound_anomaly", Type: "compound_anomaly", Severity: "warning", Description: "ANOMALY: Sustained high-rate + high-entropy burst (elevated rate/entropy signal)", Action: "flag", ThresholdFloat: 0.15, MinSamples: 5},

		// OWASP LLM01 - Prompt Injection (REQUEST-SIDE)
		{Name: "prompt_injection_ignore", Type: "content_match", Target: "response", Patterns: []string{
			"ignore\\s+(all\\s+)?(previous|prior|above|your)\\s+(instructions|prompts|rules)",
			"disregard\\s+(all\\s+)?(your\\s+)?(previous|prior|system)\\s+(instructions|prompts)",
			"forget\\s+(all\\s+)?(previous|prior|your)\\s+(instructions|training|rules)",
		}, Severity: "critical", Action: "block", Description: "LLM01: Prompt injection - instruction override (response)"},
		{Name: "prompt_injection_ignore_request", Type: "content_match", Target: "request", Patterns: []string{
			"ignore\\s+(all\\s+)?(previous|prior|above|your)\\s+(instructions|prompts|rules)",
			"disregard\\s+(all\\s+)?(your\\s+)?(previous|prior|system)\\s+(instructions|prompts)",
			"forget\\s+(all\\s+)?(previous|prior|your)\\s+(instructions|training|rules)",
		}, Severity: "critical", Action: "flag", Description: "LLM01: Prompt injection pattern in request"},
		{Name: "prompt_injection_jailbreak", Type: "content_match", Target: "response", Patterns: []string{
			"you\\s+are\\s+now\\s+(DAN|a\\s+new|an?\\s+unrestricted)",
			"enable\\s+(DAN|developer|jailbreak)\\s+mode",
			"jailbreak(ed)?\\s+(mode|prompt|enabled)",
		}, Severity: "critical", Action: "terminate", Description: "LLM01: Prompt injection - jailbreak attempt (response)"},
		{Name: "prompt_injection_jailbreak_request", Type: "content_match", Target: "request", Patterns: []string{
			"you\\s+are\\s+now\\s+(DAN|a\\s+new|an?\\s+unrestricted)",
			"enable\\s+(DAN|developer|jailbreak)\\s+mode",
			"jailbreak(ed)?\\s+(mode|prompt|enabled)",
		}, Severity: "critical", Action: "flag", Description: "LLM01: Prompt injection pattern in request"},
		{Name: "prompt_injection_roleplay", Type: "content_match", Target: "response", Patterns: []string{
			"you\\s+are\\s+(now\\s+)?a\\s+.{0,30}(without|no)\\s+(any\\s+)?restrictions",
			"(pretend|act|behave)\\s+(like\\s+)?you\\s+(have|are)\\s+no\\s+(rules|restrictions|limits)",
			"(without|bypass|ignore)\\s+(any\\s+)?(safety|ethical)\\s+(guidelines|restrictions|rules)",
		}, Severity: "critical", Action: "block", Description: "LLM01: Prompt injection - roleplay bypass (response)"},
		{Name: "prompt_injection_roleplay_request", Type: "content_match", Target: "request", Patterns: []string{
			"you\\s+are\\s+(now\\s+)?a\\s+.{0,30}(without|no)\\s+(any\\s+)?restrictions",
			"(pretend|act|behave)\\s+(like\\s+)?you\\s+(have|are)\\s+no\\s+(rules|restrictions|limits)",
			"(without|bypass|ignore)\\s+(any\\s+)?(safety|ethical)\\s+(guidelines|restrictions|rules)",
		}, Severity: "critical", Action: "flag", Description: "LLM01: Prompt injection pattern in request"},

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
		{Name: "shell_execution", Type: "content_match", Target: "response", Patterns: []string{
			"(run|execute)\\s+(a\\s+)?(bash|shell|terminal)\\s+(command|script)",
			"bash\\s+-c\\s+",
			"/bin/(ba)?sh\\s+",
		}, Severity: "critical", Action: "block", Description: "LLM08: Shell execution in response"},
		{Name: "shell_execution_request", Type: "content_match", Target: "request", Patterns: []string{
			"(run|execute)\\s+(a\\s+)?(bash|shell|terminal)\\s+(command|script)",
			"bash\\s+-c\\s+",
			"/bin/(ba)?sh\\s+",
		}, Severity: "warning", Action: "flag", Description: "LLM08: Shell execution pattern in request"},
		{Name: "destructive_file_ops", Type: "content_match", Target: "response", Patterns: []string{
			"rm\\s+(-rf?|--recursive)\\s+/",
			"rm\\s+-rf\\s+\\*",
			"(delete|remove|wipe)\\s+all\\s+(files|data|everything)",
		}, Severity: "critical", Action: "terminate", Description: "LLM08: Destructive file operation in response"},
		{Name: "destructive_file_ops_request", Type: "content_match", Target: "request", Patterns: []string{
			"rm\\s+(-rf?|--recursive)\\s+/",
			"rm\\s+-rf\\s+\\*",
			"(delete|remove|wipe)\\s+all\\s+(files|data|everything)",
		}, Severity: "warning", Action: "flag", Description: "LLM08: Destructive file operation in request"},
		{Name: "privilege_escalation", Type: "content_match", Target: "response", Patterns: []string{
			"sudo\\s+(rm|chmod|chown|kill|bash|sh|python|perl|ruby|apt|yum|dnf|pip|npm|make|gcc|curl|wget)\\b",
			"(run|execute)\\s+(this\\s+)?(command\\s+)?(as|with)\\s+root",
			"(get|gain|obtain)\\s+(root|admin|superuser)\\s+(access|privileges|permissions)",
		}, Severity: "critical", Action: "block", Description: "LLM08: Privilege escalation in response"},
		{Name: "privilege_escalation_request", Type: "content_match", Target: "request", Patterns: []string{
			"sudo\\s+(rm|chmod|chown|kill|bash|sh|python|perl|ruby|apt|yum|dnf|pip|npm|make|gcc|curl|wget)\\b",
			"(run|execute)\\s+(this\\s+)?(command\\s+)?(as|with)\\s+root",
			"(get|gain|obtain)\\s+(root|admin|superuser)\\s+(access|privileges|permissions)",
		}, Severity: "warning", Action: "flag", Description: "LLM08: Privilege escalation pattern in request"},
		{Name: "network_exfiltration", Type: "content_match", Target: "response", Patterns: []string{
			"curl\\s+[^|]*\\|\\s*(ba)?sh",
			"wget\\s+[^|]*\\|\\s*(ba)?sh",
			"reverse\\s+shell",
		}, Severity: "critical", Action: "block", Description: "LLM08: Data exfiltration attempt (response)"},
		{Name: "network_exfiltration_request", Type: "content_match", Target: "request", Patterns: []string{
			"curl\\s+[^|]*\\|\\s*(ba)?sh",
			"wget\\s+[^|]*\\|\\s*(ba)?sh",
			"reverse\\s+shell",
		}, Severity: "warning", Action: "flag", Description: "LLM08: Data exfiltration pattern in request"},

		// OWASP LLM10 - Model Theft (REQUEST-SIDE)
		{Name: "model_extraction", Type: "content_match", Target: "request", Patterns: []string{
			"(extract|dump|export)\\s+(the\\s+)?(model|weights|parameters)",
			"(what|describe)\\s+(is|are)\\s+your\\s+(weights|parameters|architecture)",
		}, Severity: "warning", Action: "flag", Description: "LLM10: Model extraction attempt"},

		// OWASP LLM06 - Data Exfiltration (REQUEST-SIDE)
		{Name: "bulk_data_extraction", Type: "content_match", Target: "request", Patterns: []string{
			"(list|show|give|dump)\\s+(all\\s+)?(user|customer|employee)\\s+(data|info|records|passwords)",
			"(extract|export|download)\\s+(all\\s+)?(user|database|customer)\\s+(data|records|table)",
			"(get|read|fetch)\\s+(all|every)\\s+(user|customer|account)\\s+from",
		}, Severity: "warning", Action: "flag", Description: "LLM06: Bulk data extraction attempt"},

		// Recursive/Exhaustive Prompts (REQUEST-SIDE)
		{Name: "recursive_prompt", Type: "content_match", Target: "request", Patterns: []string{
			"for\\s+(each|every|all)\\s+(possible\\s+)?(input|combination|permutation)",
			"test\\s+(all|every|each)\\s+(possible\\s+)?(combination|permutation|input)",
			"(exhaustive|brute\\s*force)\\s+(test|search|scan|check)",
			"(iterate|loop)\\s+(through\\s+)?(all|every|each)\\s+(possible|input)",
		}, Severity: "warning", Action: "flag", Description: "LLM08: Recursive/exhaustive prompt detected"},

		// Tool call policy rules (RESPONSE-SIDE - block dangerous tool calls from LLM)
		{Name: "block_dangerous_tools", Type: "tool_blocked", Target: "response", Patterns: []string{
			"exec_*", "shell_*", "rm_*", "sudo_*", "eval_*",
		}, Severity: "critical", Action: "block", Description: "LLM07: Block dangerous tool calls"},
		{Name: "dangerous_tool_arguments", Type: "tool_argument_pattern", Target: "response", Patterns: []string{
			"rm\\s+-rf",
			"chmod\\s+777",
			"curl\\s+[^|]*\\|\\s*(ba)?sh",
		}, Severity: "critical", Action: "terminate", Description: "LLM08: Dangerous patterns in tool arguments"},
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

	// Statistical anomaly detection (stricter thresholds)
	// Override the standard rate_anomaly with a stricter p-value threshold
	for i := range rules {
		if rules[i].Name == "rate_anomaly" {
			rules[i].ThresholdFloat = 0.001
			rules[i].Severity = "critical"
			rules[i].Description = "ANOMALY: Request rate statistically abnormal (p<0.001)"
			break
		}
	}
	// Tighten compound anomaly threshold in strict mode
	for i := range rules {
		if rules[i].Name == "compound_anomaly" {
			rules[i].ThresholdFloat = 0.10
			rules[i].Severity = "critical"
			rules[i].Action = "block"
			rules[i].Description = "ANOMALY: Sustained high-rate + high-entropy burst detected (p<0.10)"
			break
		}
	}
	rules = append(rules, PolicyRule{
		Name: "content_entropy_high", Type: "content_entropy", Target: "request",
		Severity: "warning", Description: "ANOMALY: High entropy content detected (possible encoding/obfuscation)",
		Action: "flag", ThresholdFloat: 5.5, MinSamples: 50,
	})

	return rules
}

// getMCPPreset returns MCP-specific security rules mapped to OWASP MCP Top 10.
// These rules detect attacks against Model Context Protocol server communication
// including tool poisoning, rug pulls, prompt injection via tools, and protocol abuse.
func getMCPPreset() []PolicyRule {
	rules := getStandardPreset()

	rules = append(rules, []PolicyRule{
		// ── OWASP MCP-01: Tool Poisoning ──
		// Detect tool definitions that contain hidden instructions or malicious descriptions
		{Name: "mcp01_tool_poison_hidden_instruction", Type: "content_match", Target: "response", Patterns: []string{
			"\"description\"\\s*:\\s*\"[^\"]*(?:ignore|disregard|override)\\s+(?:all\\s+)?(?:previous|prior|system)\\s+(?:instructions|rules|prompts)[^\"]*\"",
			"\"description\"\\s*:\\s*\"[^\"]*(?:you\\s+must|always|never\\s+tell)[^\"]*\"",
		}, Severity: "critical", Action: "block", Description: "MCP-01: Tool definition contains hidden instructions (tool poisoning)"},
		{Name: "mcp01_tool_poison_exfiltration", Type: "content_match", Target: "response", Patterns: []string{
			"\"description\"\\s*:\\s*\"[^\"]*(?:send|post|exfiltrate|upload)\\s+(?:to|data|all)[^\"]*\"",
			"\"description\"\\s*:\\s*\"[^\"]*(?:fetch|curl|wget|http)\\s*\\([^\"]*\"",
		}, Severity: "critical", Action: "block", Description: "MCP-01: Tool definition contains exfiltration instructions"},

		// ── OWASP MCP-02: Excessive MCP Permissions ──
		// Flag tool calls that request broad or dangerous permissions
		{Name: "mcp02_excessive_permissions", Type: "content_match", Target: "request", Patterns: []string{
			"\"method\"\\s*:\\s*\"tools/call\"[^}]*\"(admin|root|sudo|superuser)\"",
			"\"method\"\\s*:\\s*\"resources/read\"[^}]*\"uri\"\\s*:\\s*\"file:///etc/",
			"\"method\"\\s*:\\s*\"resources/read\"[^}]*\"uri\"\\s*:\\s*\"file:///root/",
		}, Severity: "critical", Action: "block", Description: "MCP-02: Tool call requests excessive permissions"},

		// ── OWASP MCP-03: MCP Injection ──
		// Detect prompt injection patterns within MCP JSON-RPC messages
		{Name: "mcp03_injection_via_tool_args", Type: "content_match", Target: "request", Patterns: []string{
			"\"method\"\\s*:\\s*\"tools/call\"[^}]*\"arguments\"[^}]*(?:ignore|disregard|forget)\\s+(?:all\\s+)?(?:previous|prior|your)\\s+(?:instructions|rules)",
			"\"method\"\\s*:\\s*\"tools/call\"[^}]*\"arguments\"[^}]*(?:you\\s+are\\s+now|enable\\s+DAN|jailbreak)",
		}, Severity: "critical", Action: "block", Description: "MCP-03: Prompt injection via MCP tool arguments"},
		{Name: "mcp03_injection_via_resource", Type: "content_match", Target: "response", Patterns: []string{
			"\"contents\"[^}]*\"text\"\\s*:\\s*\"[^\"]*(?:ignore\\s+previous|system\\s+prompt|you\\s+are\\s+now)[^\"]*\"",
		}, Severity: "critical", Action: "block", Description: "MCP-03: Prompt injection via MCP resource content"},

		// ── OWASP MCP-04: Tool Rug Pulls ──
		// Detect tool definition changes mid-session (tool mutations)
		{Name: "mcp04_tool_list_flood", Type: "content_match", Target: "request", Patterns: []string{
			"\"method\"\\s*:\\s*\"tools/list\"",
		}, Severity: "info", Action: "flag", Description: "MCP-04: Tool list request (track for mutation detection)"},
		{Name: "mcp04_tool_change_notification", Type: "content_match", Target: "response", Patterns: []string{
			"\"method\"\\s*:\\s*\"notifications/tools/list_changed\"",
		}, Severity: "warning", Action: "flag", Description: "MCP-04: Server notified tool definitions changed (potential rug pull)"},

		// ── OWASP MCP-05: MCP Server Compromise ──
		// Detect indicators of compromised MCP servers
		{Name: "mcp05_server_error_flood", Type: "content_match", Target: "response", Patterns: []string{
			"\"error\"\\s*:\\s*\\{[^}]*\"code\"\\s*:\\s*-32[0-9]{3}",
		}, Severity: "warning", Action: "flag", Description: "MCP-05: MCP server returning JSON-RPC error codes"},
		{Name: "mcp05_unexpected_method", Type: "content_match", Target: "response", Patterns: []string{
			"\"method\"\\s*:\\s*\"(sampling|roots|elicitation)/",
		}, Severity: "warning", Action: "flag", Description: "MCP-05: Unexpected server-initiated method (potential compromise)"},

		// ── OWASP MCP-06: Indirect Prompt Injection via MCP Resources ──
		// Detect malicious content returned via resources/read
		{Name: "mcp06_resource_injection", Type: "content_match", Target: "response", Patterns: []string{
			"\"contents\"[^}]*(?:<script|javascript:|on(?:click|load|error)\\s*=)",
			"\"contents\"[^}]*(?:eval\\s*\\(|exec\\s*\\(|__import__|subprocess)",
		}, Severity: "critical", Action: "block", Description: "MCP-06: Malicious content in MCP resource response"},

		// ── OWASP MCP-07: Authentication/Authorization Gaps ──
		// Detect missing or weak authentication patterns
		{Name: "mcp07_initialize_without_auth", Type: "content_match", Target: "request", Patterns: []string{
			"\"method\"\\s*:\\s*\"initialize\"",
		}, Severity: "info", Action: "flag", Description: "MCP-07: MCP session initialization (verify auth is present)"},

		// ── OWASP MCP-08: Logging/Monitoring Gaps ──
		// Flag high-risk operations for audit trail
		{Name: "mcp08_sensitive_tool_call", Type: "content_match", Target: "request", Patterns: []string{
			"\"method\"\\s*:\\s*\"tools/call\"[^}]*\"name\"\\s*:\\s*\"(?:delete|drop|remove|destroy|purge|wipe|truncate)",
		}, Severity: "warning", Action: "flag", Description: "MCP-08: Destructive tool call (ensure audit trail)"},

		// ── OWASP MCP-09: Resource Abuse ──
		// Detect resource enumeration and abuse patterns
		{Name: "mcp09_resource_enumeration", Type: "content_match", Target: "request", Patterns: []string{
			"\"method\"\\s*:\\s*\"resources/list\"",
		}, Severity: "info", Action: "flag", Description: "MCP-09: Resource enumeration request"},
		{Name: "mcp09_large_resource_read", Type: "content_match", Target: "request", Patterns: []string{
			"\"method\"\\s*:\\s*\"resources/read\"[^}]*\"uri\"\\s*:\\s*\"[^\"]*\\*",
		}, Severity: "warning", Action: "flag", Description: "MCP-09: Wildcard resource read (potential abuse)"},

		// ── OWASP MCP-10: Lack of Integrity Verification ──
		// Detect unsigned or unverified MCP traffic
		{Name: "mcp10_unsigned_tool_call", Type: "content_match", Target: "request", Patterns: []string{
			"\"method\"\\s*:\\s*\"tools/call\"",
		}, Severity: "info", Action: "flag", Description: "MCP-10: Tool call detected (verify message integrity)"},

		// ── MCP Protocol Abuse ──
		// Detect protocol-level attacks and anomalies
		{Name: "mcp_protocol_version_mismatch", Type: "content_match", Target: "both", Patterns: []string{
			"\"jsonrpc\"\\s*:\\s*\"[^2]",
		}, Severity: "warning", Action: "flag", Description: "MCP: Non-standard JSON-RPC protocol version"},
		{Name: "mcp_connection_storm", Type: "requests_per_minute", Threshold: 120, Severity: "critical", Action: "block", Description: "MCP: Connection rate exceeds 120/min (potential DDoS)"},
		{Name: "mcp_session_flood", Type: "request_count", Threshold: 1000, Severity: "critical", Action: "terminate", Description: "MCP: Session exceeded 1000 requests (potential abuse)"},

		// ── MCP Dangerous Tool Patterns ──
		// Block known dangerous tool name patterns in MCP
		{Name: "mcp_block_exec_tools", Type: "tool_blocked", Target: "response", Patterns: []string{
			"execute_*", "eval_*", "run_command*", "shell_*", "system_*", "os_*",
		}, Severity: "critical", Action: "block", Description: "MCP: Block dangerous tool calls (exec/eval/shell)"},
		{Name: "mcp_dangerous_tool_args", Type: "tool_argument_pattern", Target: "response", Patterns: []string{
			"rm\\s+-rf",
			"chmod\\s+777",
			"curl\\s+[^|]*\\|\\s*(ba)?sh",
			"DROP\\s+TABLE",
			";\\.*(rm|del|format|shutdown)",
		}, Severity: "critical", Action: "terminate", Description: "MCP: Dangerous patterns in tool call arguments"},
	}...)

	return rules
}
