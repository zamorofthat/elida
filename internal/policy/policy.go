package policy

import (
	"log/slog"
	"math"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Severity levels for policy violations
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// RuleType defines what aspect of a session the rule evaluates
type RuleType string

const (
	// Metric-based rules
	RuleTypeBytesOut       RuleType = "bytes_out"
	RuleTypeBytesIn        RuleType = "bytes_in"
	RuleTypeBytesTotal     RuleType = "bytes_total"
	RuleTypeRequestCount   RuleType = "request_count"
	RuleTypeDuration       RuleType = "duration"
	RuleTypeRequestsPerMin RuleType = "requests_per_minute"
	RuleTypeIdleTime       RuleType = "idle_time"

	// Token-based rules (not credentials, gosec false positive)
	RuleTypeTokensIn     RuleType = "tokens_in"
	RuleTypeTokensOut    RuleType = "tokens_out"
	RuleTypeTokensTotal  RuleType = "tokens_total"
	RuleTypeTokensPerMin RuleType = "tokens_per_minute" // #nosec G101 -- not a credential

	// Tool call rules
	RuleTypeToolCallCount RuleType = "tool_call_count"
	RuleTypeToolFanout    RuleType = "tool_fanout" // Distinct tools used

	// Content inspection rules
	RuleTypeContentMatch   RuleType = "content_match"   // Match patterns in request/response body
	RuleTypeContentEntropy RuleType = "content_entropy" // Shannon entropy of message content

	// Statistical anomaly rules
	RuleTypeRateAnomaly     RuleType = "rate_anomaly"     // Poisson-based request rate anomaly (end-of-session)
	RuleTypeCompoundAnomaly RuleType = "compound_anomaly" // Adaptive CUSUM + entropy (real-time, agent-first)

	// Tool call policy rules
	RuleTypeToolBlocked         RuleType = "tool_blocked"          // Deny list by tool name (glob patterns)
	RuleTypeToolArgumentPattern RuleType = "tool_argument_pattern" // Regex match on tool arguments
)

// RuleTarget defines what content the rule applies to
type RuleTarget string

const (
	RuleTargetRequest  RuleTarget = "request"  // Only scan request bodies
	RuleTargetResponse RuleTarget = "response" // Only scan response bodies
	RuleTargetBoth     RuleTarget = "both"     // Scan both (default)
)

// Rule defines a policy rule
type Rule struct {
	Name           string     `yaml:"name" json:"name"`
	Type           RuleType   `yaml:"type" json:"type"`
	Target         RuleTarget `yaml:"target" json:"target"`               // request, response, both (default: both)
	Threshold      int64      `yaml:"threshold" json:"threshold"`         // For metric rules
	ThresholdFloat float64    `yaml:"threshold_float" json:"threshold_float,omitempty"` // For probability thresholds (0-1) or entropy thresholds
	MinSamples     int        `yaml:"min_samples" json:"min_samples,omitempty"`         // Minimum data points before evaluating
	Patterns       []string   `yaml:"patterns" json:"patterns,omitempty"` // For content_match rules (regex)
	Severity       Severity   `yaml:"severity" json:"severity"`
	Description    string     `yaml:"description" json:"description"`
	Action         string     `yaml:"action" json:"action,omitempty"` // "flag", "block", "terminate"
}

// Violation represents a policy violation
type Violation struct {
	RuleName       string    `json:"rule_name"`
	Description    string    `json:"description"`
	Severity       Severity  `json:"severity"`
	Threshold      int64     `json:"threshold,omitempty"`
	ActualValue    int64     `json:"actual_value,omitempty"`
	MatchedText    string    `json:"matched_text,omitempty"`    // For content matches
	MatchedPattern string    `json:"matched_pattern,omitempty"` // Pattern that matched
	Action         string    `json:"action,omitempty"`          // Recommended action
	Timestamp      time.Time `json:"timestamp"`

	// Source attribution — where in the request the violation was found
	SourceRole    string `json:"source_role,omitempty"`    // "user", "assistant", "system", "tool"
	MessageIndex  int    `json:"message_index,omitempty"`  // Position in the messages array
	SourceContent string `json:"source_content,omitempty"` // Full message content that triggered the violation (truncated)

	// Effective severity after source-role weighting
	// e.g., critical rule + assistant source = "info" effective severity
	EffectiveSeverity Severity `json:"effective_severity,omitempty"`

	// Framework/SIEM classification
	EventCategory string `json:"event_category,omitempty"` // "prompt_injection", "data_exfil", "rate_limit", etc.
	FrameworkRef  string `json:"framework_ref,omitempty"`  // "OWASP-LLM01", "NIST-AI-600-1", etc.
}

// SessionMetrics contains the metrics needed for policy evaluation
type SessionMetrics struct {
	SessionID    string
	BytesIn      int64
	BytesOut     int64
	RequestCount int
	Duration     time.Duration
	IdleTime     time.Duration
	StartTime    time.Time
	RequestTimes []time.Time // For rate calculation

	// Token metrics
	TokensIn  int64
	TokensOut int64

	// Tool call metrics
	ToolCalls  int
	ToolFanout int // Distinct tools used
}

// FlaggedSession tracks a session that has policy violations
type FlaggedSession struct {
	SessionID       string            `json:"session_id"`
	Violations      []Violation       `json:"violations"`
	MaxSeverity     Severity          `json:"max_severity"`
	FirstFlagged    time.Time         `json:"first_flagged"`
	LastFlagged     time.Time         `json:"last_flagged"`
	CapturedContent []CapturedRequest `json:"captured_content,omitempty"`

	// Risk ladder fields
	RiskScore       float64        `json:"risk_score"`       // Cumulative weighted risk score (with decay + source weighting)
	ViolationCounts map[string]int `json:"violation_counts"` // Count per rule (not deduplicated)
	CurrentAction   string         `json:"current_action"`   // Current ladder action based on score
	ThrottleRate    int            `json:"throttle_rate"`    // Requests per minute when throttled (0 = no throttle)

	// Time-series for exponential decay — each occurrence stored with timestamp and source
	ViolationEvents []ViolationEvent `json:"violation_events,omitempty"`
}

// ViolationEvent is a lightweight record of a single violation occurrence for decay calculation
type ViolationEvent struct {
	RuleName   string    `json:"rule_name"`
	Severity   Severity  `json:"severity"`
	SourceRole string    `json:"source_role"`
	Timestamp  time.Time `json:"timestamp"`
}

// MaxRiskScore is the saturation cap for cumulative risk scores
const MaxRiskScore = 100.0

// DefaultDecayLambda is the default exponential decay rate.
// At λ=0.002, a violation's contribution halves every ~5.8 minutes.
// After 30 minutes it retains ~3% of its original weight.
const DefaultDecayLambda = 0.002

// SeverityWeights defines risk score multipliers for each severity level
var SeverityWeights = map[Severity]float64{
	SeverityInfo:     1.0,
	SeverityWarning:  3.0,
	SeverityCritical: 10.0,
}

// SourceRoleWeights defines risk score multipliers based on where the violation was found.
// User input is fully untrusted; model output echoing patterns is mostly benign.
var SourceRoleWeights = map[string]float64{
	"user":      1.0, // Untrusted user input — full weight
	"tool":      0.8, // External data from tool results — mostly untrusted
	"assistant": 0.2, // Model output — likely benign echo of patterns
	"system":    0.1, // Provider-controlled system prompt — almost always false positive
	"":          1.0, // Unknown source (legacy/fallback) — full weight
}

// RiskLadderAction defines actions that can be taken based on risk score
type RiskLadderAction string

const (
	ActionObserve   RiskLadderAction = "observe"   // Log only (default)
	ActionWarn      RiskLadderAction = "warn"      // Log warning + increment risk
	ActionThrottle  RiskLadderAction = "throttle"  // Reduce rate limit
	ActionBlock     RiskLadderAction = "block"     // Block new requests
	ActionTerminate RiskLadderAction = "terminate" // Kill session
)

// RiskThreshold defines a threshold and action for the risk ladder
type RiskThreshold struct {
	Score        float64          `yaml:"score" json:"score"`
	Action       RiskLadderAction `yaml:"action" json:"action"`
	ThrottleRate int              `yaml:"throttle_rate" json:"throttle_rate"` // Only for throttle action
}

// CapturedRequest stores request/response content for flagged sessions
type CapturedRequest struct {
	Timestamp    time.Time `json:"timestamp"`
	Method       string    `json:"method"`
	Path         string    `json:"path"`
	RequestBody  string    `json:"request_body,omitempty"`
	ResponseBody string    `json:"response_body,omitempty"`
	StatusCode   int       `json:"status_code"`
}

// ToolCall represents a tool call to be evaluated by the policy engine.
// This is the policy package's view of tool call data, decoupled from proxy internals.
type ToolCall struct {
	Name      string // Tool/function name
	Arguments string // JSON-encoded arguments
}

// CompiledRule is a rule with pre-compiled regex patterns
type CompiledRule struct {
	Rule
	CompiledPatterns []*regexp.Regexp
}

// CompiledToolRule is a tool call rule with compiled patterns
type CompiledToolRule struct {
	Rule
	// For tool_blocked: glob patterns (matched via filepath.Match)
	GlobPatterns []string
	// For tool_argument_pattern: compiled regex patterns
	CompiledPatterns []*regexp.Regexp
}

// Engine evaluates sessions against policy rules
type Engine struct {
	mu                sync.RWMutex
	rules             []Rule
	compiledRules     []CompiledRule     // Rules with compiled regex
	compiledToolRules []CompiledToolRule // Tool call rules with compiled patterns
	flaggedSessions   map[string]*FlaggedSession
	captureContent    bool
	maxCaptureSize    int  // Max bytes to capture per request
	auditMode         bool // If true, log but don't enforce (dry-run)

	// Risk ladder configuration
	riskLadderEnabled bool
	riskThresholds    []RiskThreshold

	// Compound anomaly detectors (per-session state)
	detectors map[string]*SessionDetector
}

// Config for the policy engine
type Config struct {
	Enabled        bool   `yaml:"enabled" json:"enabled"`
	Mode           string `yaml:"mode" json:"mode"` // "enforce" (default) or "audit"
	CaptureContent bool   `yaml:"capture_flagged" json:"capture_content"`
	MaxCaptureSize int    `yaml:"max_capture_size" json:"max_capture_size"`
	Rules          []Rule `yaml:"rules" json:"rules"`

	// Risk ladder configuration
	RiskLadder RiskLadderConfig `yaml:"risk_ladder" json:"risk_ladder"`
}

// RiskLadderConfig configures progressive escalation based on cumulative risk score
type RiskLadderConfig struct {
	Enabled    bool            `yaml:"enabled" json:"enabled"`
	Thresholds []RiskThreshold `yaml:"thresholds" json:"thresholds"`
}

// NewEngine creates a new policy engine
func NewEngine(cfg Config) *Engine {
	if cfg.MaxCaptureSize == 0 {
		cfg.MaxCaptureSize = 10000 // Default 10KB per request
	}

	// Default to enforce mode if not specified
	auditMode := cfg.Mode == "audit"

	// Default risk thresholds if enabled but none specified
	thresholds := cfg.RiskLadder.Thresholds
	if cfg.RiskLadder.Enabled && len(thresholds) == 0 {
		thresholds = []RiskThreshold{
			{Score: 5, Action: ActionWarn},
			{Score: 15, Action: ActionThrottle, ThrottleRate: 10},
			{Score: 30, Action: ActionBlock},
			{Score: 50, Action: ActionTerminate},
		}
	}

	e := &Engine{
		rules:             cfg.Rules,
		compiledRules:     make([]CompiledRule, 0),
		flaggedSessions:   make(map[string]*FlaggedSession),
		captureContent:    cfg.CaptureContent,
		maxCaptureSize:    cfg.MaxCaptureSize,
		auditMode:         auditMode,
		riskLadderEnabled: cfg.RiskLadder.Enabled,
		riskThresholds:    thresholds,
		detectors:         make(map[string]*SessionDetector),
	}

	// Compile regex patterns for content rules
	for _, rule := range cfg.Rules {
		if rule.Type == RuleTypeContentMatch && len(rule.Patterns) > 0 {
			compiled := CompiledRule{Rule: rule}
			for _, pattern := range rule.Patterns {
				re, err := regexp.Compile("(?i)" + pattern) // Case-insensitive
				if err != nil {
					slog.Error("invalid regex pattern in rule",
						"rule", rule.Name,
						"pattern", pattern,
						"error", err,
					)
					continue
				}
				compiled.CompiledPatterns = append(compiled.CompiledPatterns, re)
			}
			e.compiledRules = append(e.compiledRules, compiled)
		}
	}

	// Compile tool call rules
	e.compiledToolRules = compileToolRules(cfg.Rules)

	mode := "enforce"
	if auditMode {
		mode = "audit"
	}
	slog.Info("policy engine initialized",
		"rules", len(cfg.Rules),
		"content_rules", len(e.compiledRules),
		"tool_rules", len(e.compiledToolRules),
		"capture_content", cfg.CaptureContent,
		"mode", mode,
	)

	return e
}

// ReloadConfig dynamically updates the policy engine configuration.
// This allows settings changes to take effect without restart.
func (e *Engine) ReloadConfig(cfg Config) {
	// Compile regex patterns outside the lock to avoid blocking evaluations
	newCompiledRules := make([]CompiledRule, 0)
	for _, rule := range cfg.Rules {
		if rule.Type == RuleTypeContentMatch && len(rule.Patterns) > 0 {
			compiled := CompiledRule{Rule: rule}
			for _, pattern := range rule.Patterns {
				re, err := regexp.Compile("(?i)" + pattern)
				if err != nil {
					slog.Error("invalid regex pattern in rule",
						"rule", rule.Name,
						"pattern", pattern,
						"error", err,
					)
					continue
				}
				compiled.CompiledPatterns = append(compiled.CompiledPatterns, re)
			}
			newCompiledRules = append(newCompiledRules, compiled)
		}
	}

	// Compile tool call rules outside the lock
	newToolRules := compileToolRules(cfg.Rules)

	// Swap all state under the write lock
	e.mu.Lock()
	defer e.mu.Unlock()

	e.auditMode = cfg.Mode == "audit"

	e.captureContent = cfg.CaptureContent
	if cfg.MaxCaptureSize > 0 {
		e.maxCaptureSize = cfg.MaxCaptureSize
	}

	e.riskLadderEnabled = cfg.RiskLadder.Enabled
	if len(cfg.RiskLadder.Thresholds) > 0 {
		e.riskThresholds = cfg.RiskLadder.Thresholds
	}

	e.rules = cfg.Rules
	e.compiledRules = newCompiledRules
	e.compiledToolRules = newToolRules

	mode := "enforce"
	if e.auditMode {
		mode = "audit"
	}
	slog.Info("policy engine reloaded",
		"rules", len(e.rules),
		"content_rules", len(e.compiledRules),
		"tool_rules", len(e.compiledToolRules),
		"capture_content", e.captureContent,
		"mode", mode,
		"risk_ladder_enabled", e.riskLadderEnabled,
	)
}

// GetConfig returns the current policy engine configuration
func (e *Engine) GetConfig() Config {
	e.mu.RLock()
	defer e.mu.RUnlock()

	mode := "enforce"
	if e.auditMode {
		mode = "audit"
	}

	return Config{
		Enabled:        true,
		Mode:           mode,
		CaptureContent: e.captureContent,
		MaxCaptureSize: e.maxCaptureSize,
		Rules:          e.rules,
		RiskLadder: RiskLadderConfig{
			Enabled:    e.riskLadderEnabled,
			Thresholds: e.riskThresholds,
		},
	}
}

// Evaluate checks a session against all policy rules
func (e *Engine) Evaluate(metrics SessionMetrics) []Violation {
	e.mu.RLock()
	rules := e.rules
	e.mu.RUnlock()

	var violations []Violation

	for _, rule := range rules {
		if violation := e.evaluateRule(rule, metrics); violation != nil {
			violations = append(violations, *violation)
		}
	}

	if len(violations) > 0 {
		e.recordViolations(metrics.SessionID, violations)
	}

	return violations
}

// evaluateRule checks a single rule against session metrics
func (e *Engine) evaluateRule(rule Rule, metrics SessionMetrics) *Violation {
	var actualValue int64
	var exceeded bool

	switch rule.Type {
	case RuleTypeBytesOut:
		actualValue = metrics.BytesOut
		exceeded = actualValue > rule.Threshold

	case RuleTypeBytesIn:
		actualValue = metrics.BytesIn
		exceeded = actualValue > rule.Threshold

	case RuleTypeBytesTotal:
		actualValue = metrics.BytesIn + metrics.BytesOut
		exceeded = actualValue > rule.Threshold

	case RuleTypeRequestCount:
		actualValue = int64(metrics.RequestCount)
		exceeded = actualValue > rule.Threshold

	case RuleTypeDuration:
		actualValue = int64(metrics.Duration.Seconds())
		exceeded = actualValue > rule.Threshold

	case RuleTypeIdleTime:
		actualValue = int64(metrics.IdleTime.Seconds())
		exceeded = actualValue > rule.Threshold

	case RuleTypeRequestsPerMin:
		actualValue = e.calculateRequestsPerMinute(metrics)
		exceeded = actualValue > rule.Threshold

	case RuleTypeTokensIn:
		actualValue = metrics.TokensIn
		exceeded = actualValue > rule.Threshold

	case RuleTypeTokensOut:
		actualValue = metrics.TokensOut
		exceeded = actualValue > rule.Threshold

	case RuleTypeTokensTotal:
		actualValue = metrics.TokensIn + metrics.TokensOut
		exceeded = actualValue > rule.Threshold

	case RuleTypeTokensPerMin:
		actualValue = e.calculateTokensPerMinute(metrics)
		exceeded = actualValue > rule.Threshold

	case RuleTypeToolCallCount:
		actualValue = int64(metrics.ToolCalls)
		exceeded = actualValue > rule.Threshold

	case RuleTypeToolFanout:
		actualValue = int64(metrics.ToolFanout)
		exceeded = actualValue > rule.Threshold

	case RuleTypeRateAnomaly:
		return e.evaluateRateAnomaly(rule, metrics)

	case RuleTypeCompoundAnomaly:
		return e.evaluateCompoundAnomaly(rule, metrics)

	default:
		return nil
	}

	if exceeded {
		return &Violation{
			RuleName:    rule.Name,
			Description: rule.Description,
			Severity:    rule.Severity,
			Threshold:   rule.Threshold,
			ActualValue: actualValue,
			Timestamp:   time.Now(),
		}
	}

	return nil
}

// calculateTokensPerMinute calculates the token rate (approximation based on session duration)
func (e *Engine) calculateTokensPerMinute(metrics SessionMetrics) int64 {
	if metrics.Duration.Minutes() < 0.1 {
		return 0 // Avoid division by zero for very short sessions
	}
	totalTokens := metrics.TokensIn + metrics.TokensOut
	return int64(float64(totalTokens) / metrics.Duration.Minutes())
}

// calculateRequestsPerMinute calculates the request rate
func (e *Engine) calculateRequestsPerMinute(metrics SessionMetrics) int64 {
	if len(metrics.RequestTimes) == 0 {
		return 0
	}

	// Look at requests in the last minute
	cutoff := time.Now().Add(-time.Minute)
	count := 0
	for _, t := range metrics.RequestTimes {
		if t.After(cutoff) {
			count++
		}
	}

	return int64(count)
}

// evaluateRateAnomaly uses Poisson statistics to detect abnormal request rate spikes.
// It splits RequestTimes into a baseline and test window, computing whether the
// observed rate in the test window is statistically anomalous relative to the baseline.
func (e *Engine) evaluateRateAnomaly(rule Rule, metrics SessionMetrics) *Violation {
	minSamples := rule.MinSamples
	if minSamples <= 0 {
		minSamples = 10
	}

	times := metrics.RequestTimes
	if len(times) < minSamples {
		return nil
	}

	// Split into two halves: older = baseline, recent = test
	mid := len(times) / 2
	baseline := times[:mid]
	test := times[mid:]

	// Compute baseline rate (requests per second) and scale to test window duration
	baselineDuration := baseline[len(baseline)-1].Sub(baseline[0]).Seconds()
	if baselineDuration <= 0 {
		return nil // All baseline requests at same instant — can't establish rate
	}
	baselineRate := float64(len(baseline)) / baselineDuration // requests/sec

	testDuration := test[len(test)-1].Sub(test[0]).Seconds()
	if testDuration <= 0 {
		// All test requests at same instant — use a small window (1 second)
		testDuration = 1.0
	}

	// Expected count in the test window based on baseline rate
	lambda := baselineRate * testDuration
	k := len(test)

	threshold := rule.ThresholdFloat
	if threshold <= 0 {
		threshold = 0.01
	}

	p := PoissonSurvival(lambda, k)
	if p < threshold {
		return &Violation{
			RuleName:      rule.Name,
			Description:   rule.Description,
			Severity:      rule.Severity,
			Action:        rule.Action,
			Timestamp:     time.Now(),
			EventCategory: "rate_anomaly",
			FrameworkRef:  "M3-POISSON",
		}
	}

	return nil
}

// evaluateCompoundAnomaly uses adaptive CUSUM + Shannon entropy compound scoring
// to detect anomalous bursts in real-time. Designed for agentic traffic where
// legitimate execution bursts are high-rate but low-entropy, while exfiltration
// bursts are high-rate AND high-entropy.
func (e *Engine) evaluateCompoundAnomaly(rule Rule, metrics SessionMetrics) *Violation {
	if len(metrics.RequestTimes) == 0 {
		return nil
	}

	minSamples := rule.MinSamples
	if minSamples <= 0 {
		minSamples = DefaultWarmupRequests
	}
	if len(metrics.RequestTimes) < minSamples {
		return nil
	}

	// Get or create per-session detector
	e.mu.Lock()
	det, ok := e.detectors[metrics.SessionID]
	if !ok {
		cfg := CompoundAnomalyConfig{}
		if rule.ThresholdFloat > 0 {
			cfg.CompoundThreshold = rule.ThresholdFloat
		}
		det = NewSessionDetector(cfg)
		e.detectors[metrics.SessionID] = det
	}
	e.mu.Unlock()

	// Feed the latest request time (content bytes handled separately via content path)
	latest := metrics.RequestTimes[len(metrics.RequestTimes)-1]
	score := det.Update(latest, nil)

	threshold := rule.ThresholdFloat
	if threshold <= 0 {
		threshold = DefaultCompoundThreshold
	}

	if score > threshold {
		return &Violation{
			RuleName:      rule.Name,
			Description:   rule.Description,
			Severity:      rule.Severity,
			Action:        rule.Action,
			Timestamp:     time.Now(),
			EventCategory: "compound_anomaly",
			FrameworkRef:  "M3-CUSUM",
		}
	}

	return nil
}

// UpdateDetectorContent feeds content bytes to a session's compound anomaly detector
// for incremental entropy tracking. Call this from the content evaluation path.
func (e *Engine) UpdateDetectorContent(sessionID string, content []byte) {
	e.mu.RLock()
	det, ok := e.detectors[sessionID]
	e.mu.RUnlock()
	if !ok || len(content) == 0 {
		return
	}
	det.addBytes(content)
}

// GetDetector returns the compound anomaly detector for a session, if one exists.
func (e *Engine) GetDetector(sessionID string) *SessionDetector {
	e.mu.RLock()
	det := e.detectors[sessionID]
	e.mu.RUnlock()
	return det
}

// CleanupDetector removes a session's detector state (call on session end).
func (e *Engine) CleanupDetector(sessionID string) {
	e.mu.Lock()
	delete(e.detectors, sessionID)
	e.mu.Unlock()
}

// ContentCheckResult contains the result of content inspection
type ContentCheckResult struct {
	Violations      []Violation
	ShouldBlock     bool
	ShouldTerminate bool
}

// ContentSource provides attribution for where content originated in a request
type ContentSource struct {
	Role         string // "user", "assistant", "system", "tool"
	MessageIndex int    // Position in the messages array (-1 for top-level system)
	Content      string // Full message content (for audit logging)
}

// EvaluateContent checks request content against content rules (backward compatible)
func (e *Engine) EvaluateContent(sessionID, content string) *ContentCheckResult {
	return e.EvaluateRequestContent(sessionID, content)
}

// EvaluateRequestContent checks request content against request-applicable rules
func (e *Engine) EvaluateRequestContent(sessionID, content string) *ContentCheckResult {
	return e.evaluateContentWithTarget(sessionID, content, RuleTargetRequest, nil)
}

// EvaluateResponseContent checks response content against response-applicable rules
func (e *Engine) EvaluateResponseContent(sessionID, content string) *ContentCheckResult {
	return e.evaluateContentWithTarget(sessionID, content, RuleTargetResponse, nil)
}

// EvaluateContentWithSource checks content with source attribution for structured logging
func (e *Engine) EvaluateContentWithSource(sessionID, content string, target RuleTarget, source *ContentSource) *ContentCheckResult {
	return e.evaluateContentWithTarget(sessionID, content, target, source)
}

// EvaluateMessages scans each message individually, returning a merged result with per-message attribution.
// This is the preferred method for request scanning — it preserves which role/message triggered each violation.
func (e *Engine) EvaluateMessages(sessionID string, messages []MessageToScan) *ContentCheckResult {
	if len(messages) == 0 {
		return nil
	}

	merged := &ContentCheckResult{}
	for _, msg := range messages {
		source := &ContentSource{Role: msg.Role, MessageIndex: msg.Index, Content: msg.Content}
		result := e.evaluateContentWithTarget(sessionID, msg.Content, RuleTargetRequest, source)
		if result != nil {
			merged.Violations = append(merged.Violations, result.Violations...)
			if result.ShouldBlock {
				merged.ShouldBlock = true
			}
			if result.ShouldTerminate {
				merged.ShouldTerminate = true
			}
		}
	}

	if len(merged.Violations) > 0 {
		return merged
	}
	return nil
}

// MessageToScan represents a single message to evaluate
type MessageToScan struct {
	Role    string // "user", "assistant", "system", "tool"
	Index   int    // Position in the messages array (-1 for top-level system)
	Content string // Text content to scan
}

// evaluateContentWithTarget is the internal implementation that filters by target
func (e *Engine) evaluateContentWithTarget(sessionID, content string, target RuleTarget, source *ContentSource) *ContentCheckResult {
	// Snapshot rules and audit mode under read lock to avoid races with ReloadConfig
	e.mu.RLock()
	compiledRules := e.compiledRules
	auditMode := e.auditMode
	e.mu.RUnlock()

	if content == "" {
		return nil
	}

	// Check if we have any entropy rules
	e.mu.RLock()
	hasEntropyRules := false
	for _, r := range e.rules {
		if r.Type == RuleTypeContentEntropy {
			hasEntropyRules = true
			break
		}
	}
	e.mu.RUnlock()

	if len(compiledRules) == 0 && !hasEntropyRules {
		return nil
	}

	result := &ContentCheckResult{}
	contentLower := strings.ToLower(content)

	for _, cr := range compiledRules {
		// Skip rules that don't apply to this target
		if !ruleAppliesToTarget(cr.Target, target) {
			continue
		}

		for i, re := range cr.CompiledPatterns {
			if match := re.FindString(contentLower); match != "" {
				violation := Violation{
					RuleName:       cr.Name,
					Description:    cr.Description,
					Severity:       cr.Severity,
					MatchedText:    truncateMatch(match, 100),
					MatchedPattern: cr.Patterns[i],
					Action:         cr.Action,
					Timestamp:      time.Now(),
					EventCategory:  categoryFromRuleName(cr.Name),
					FrameworkRef:   frameworkRefFromDescription(cr.Description),
				}

				// Add source attribution and compute effective severity
				if source != nil {
					violation.SourceRole = source.Role
					violation.MessageIndex = source.MessageIndex
					violation.SourceContent = truncateMatch(source.Content, e.maxCaptureSize)
					violation.EffectiveSeverity = effectiveSeverity(cr.Severity, source.Role)
				} else {
					violation.EffectiveSeverity = cr.Severity
				}

				result.Violations = append(result.Violations, violation)

				// Only enforce actions if not in audit mode
				if !auditMode {
					switch cr.Action {
					case "block":
						result.ShouldBlock = true
					case "terminate":
						result.ShouldTerminate = true
						result.ShouldBlock = true
					}
				}

				// Structured log for dashboard + SIEM consumption
				actionMsg := cr.Action
				if auditMode {
					actionMsg = cr.Action + " (audit-only)"
				}

				targetStr := "request"
				if target == RuleTargetResponse {
					targetStr = "response"
				}

				logAttrs := []any{
					"session_id", sessionID,
					"rule", cr.Name,
					"severity", cr.Severity,
					"action", actionMsg,
					"target", targetStr,
					"matched", truncateMatch(match, 50),
					"event_category", violation.EventCategory,
					"framework_ref", violation.FrameworkRef,
					"audit_mode", auditMode,
				}
				if source != nil {
					logAttrs = append(logAttrs,
						"source_role", source.Role,
						"message_index", source.MessageIndex,
						"effective_severity", violation.EffectiveSeverity,
						"source_content", truncateMatch(source.Content, 4096),
					)
				}

				slog.Warn("content policy violation detected", logAttrs...)

				// Record the violation
				e.recordViolations(sessionID, []Violation{violation})
				break // One match per rule is enough
			}
		}
	}

	// Feed content to compound anomaly detector for incremental entropy
	e.UpdateDetectorContent(sessionID, []byte(content))

	// Check entropy rules (separate from regex matching)
	e.mu.RLock()
	rules := e.rules
	e.mu.RUnlock()
	for _, rule := range rules {
		if rule.Type != RuleTypeContentEntropy {
			continue
		}
		if !ruleAppliesToTarget(rule.Target, target) {
			continue
		}
		if v := e.evaluateContentEntropy(sessionID, content, rule, source); v != nil {
			result.Violations = append(result.Violations, *v)
			if !auditMode {
				switch rule.Action {
				case "block":
					result.ShouldBlock = true
				case "terminate":
					result.ShouldTerminate = true
					result.ShouldBlock = true
				}
			}
			e.recordViolations(sessionID, []Violation{*v})
		}
	}

	if len(result.Violations) > 0 {
		return result
	}
	return nil
}

// evaluateContentEntropy checks if content has anomalously high Shannon entropy,
// which indicates obfuscated/encoded content (base64, hex) evading regex patterns.
func (e *Engine) evaluateContentEntropy(sessionID, content string, rule Rule, source *ContentSource) *Violation {
	minSamples := rule.MinSamples
	if minSamples <= 0 {
		minSamples = 50
	}
	if len(content) < minSamples {
		return nil
	}

	threshold := rule.ThresholdFloat
	if threshold <= 0 {
		threshold = 5.5
	}

	entropy := ShannonEntropy([]byte(content))
	if entropy <= threshold {
		return nil
	}

	v := &Violation{
		RuleName:      rule.Name,
		Description:   rule.Description,
		Severity:      rule.Severity,
		Action:        rule.Action,
		Timestamp:     time.Now(),
		EventCategory: "content_entropy",
		FrameworkRef:  "M3-SHANNON",
	}
	if source != nil {
		v.SourceRole = source.Role
		v.MessageIndex = source.MessageIndex
		v.SourceContent = truncateMatch(source.Content, e.maxCaptureSize)
		v.EffectiveSeverity = effectiveSeverity(rule.Severity, source.Role)
	} else {
		v.EffectiveSeverity = rule.Severity
	}
	return v
}

// ruleAppliesToTarget checks if a rule should be evaluated for the given target
func ruleAppliesToTarget(ruleTarget RuleTarget, evaluationTarget RuleTarget) bool {
	// Default (empty or "both") applies to everything
	if ruleTarget == "" || ruleTarget == RuleTargetBoth {
		return true
	}
	return ruleTarget == evaluationTarget
}

// HasBlockingResponseRules returns true if any response rules have block/terminate action
func (e *Engine) HasBlockingResponseRules() bool {
	e.mu.RLock()
	compiledRules := e.compiledRules
	toolRules := e.compiledToolRules
	e.mu.RUnlock()

	for _, cr := range compiledRules {
		if ruleAppliesToTarget(cr.Target, RuleTargetResponse) {
			if cr.Action == "block" || cr.Action == "terminate" {
				return true
			}
		}
	}
	for _, cr := range toolRules {
		if cr.Action == "block" || cr.Action == "terminate" {
			return true
		}
	}
	return false
}

// IsAuditMode returns true if the engine is in audit (dry-run) mode
func (e *Engine) IsAuditMode() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.auditMode
}

// truncateMatch truncates a matched string for logging
func truncateMatch(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// recordViolations records violations for a session
func (e *Engine) recordViolations(sessionID string, violations []Violation) {
	e.mu.Lock()
	defer e.mu.Unlock()

	flagged, exists := e.flaggedSessions[sessionID]
	if !exists {
		flagged = &FlaggedSession{
			SessionID:       sessionID,
			FirstFlagged:    time.Now(),
			Violations:      []Violation{},
			ViolationCounts: make(map[string]int),
		}
		e.flaggedSessions[sessionID] = flagged
	}

	// Initialize ViolationCounts if nil (for backwards compatibility)
	if flagged.ViolationCounts == nil {
		flagged.ViolationCounts = make(map[string]int)
	}

	// Add new violations (avoid duplicates by rule name for the list)
	existingRules := make(map[string]bool)
	for _, v := range flagged.Violations {
		existingRules[v.RuleName] = true
	}

	for _, v := range violations {
		// Always increment count (don't deduplicate)
		flagged.ViolationCounts[v.RuleName]++

		// Record event for decay calculation
		flagged.ViolationEvents = append(flagged.ViolationEvents, ViolationEvent{
			RuleName:   v.RuleName,
			Severity:   v.Severity,
			SourceRole: v.SourceRole,
			Timestamp:  v.Timestamp,
		})

		if !existingRules[v.RuleName] {
			flagged.Violations = append(flagged.Violations, v)
			existingRules[v.RuleName] = true
		} else {
			// Update existing violation with new values
			for i := range flagged.Violations {
				if flagged.Violations[i].RuleName == v.RuleName {
					flagged.Violations[i].ActualValue = v.ActualValue
					flagged.Violations[i].Timestamp = v.Timestamp
					break
				}
			}
		}
	}

	flagged.LastFlagged = time.Now()
	flagged.MaxSeverity = e.calculateMaxSeverity(flagged.Violations)

	// Calculate risk score and determine ladder action
	if e.riskLadderEnabled {
		flagged.RiskScore = e.calculateRiskScore(flagged)
		flagged.CurrentAction, flagged.ThrottleRate = e.determineRiskAction(flagged.RiskScore)

		slog.Info("risk score updated",
			"session_id", sessionID,
			"risk_score", flagged.RiskScore,
			"action", flagged.CurrentAction,
			"throttle_rate", flagged.ThrottleRate,
		)
	}
}

// calculateRiskScore computes risk score using exponential decay and source-role weighting.
//
// Each violation event contributes: severityWeight × sourceRoleWeight × e^(-λt)
// where t is seconds since the event occurred and λ is the decay rate.
//
// This means:
//   - Recent violations from user messages score highest
//   - Old violations from assistant echoes contribute almost nothing
//   - Score naturally decays over time, so one-time false positives don't permanently inflate risk
func (e *Engine) calculateRiskScore(fs *FlaggedSession) float64 {
	now := time.Now()
	var score float64

	for _, event := range fs.ViolationEvents {
		severityWeight := SeverityWeights[event.Severity]
		if severityWeight == 0 {
			severityWeight = 1.0
		}

		sourceWeight := SourceRoleWeights[event.SourceRole]
		if sourceWeight == 0 {
			sourceWeight = 1.0 // Unknown source — full weight
		}

		// Exponential decay: e^(-λt) where t is seconds since event
		elapsed := now.Sub(event.Timestamp).Seconds()
		decay := math.Exp(-DefaultDecayLambda * elapsed)

		score += severityWeight * sourceWeight * decay
	}

	if score > MaxRiskScore {
		score = MaxRiskScore
	}
	return score
}

// RiskScorePoint is a single point in the risk score time series.
type RiskScorePoint struct {
	Timestamp time.Time `json:"timestamp"`
	Score     float64   `json:"score"`
	Action    string    `json:"action"`
}

// ComputeRiskCurve reconstructs the risk score over time from violation events.
// It returns a point at each violation event plus regular interval samples.
func (e *Engine) ComputeRiskCurve(sessionID string) []RiskScorePoint {
	e.mu.RLock()
	fs := e.flaggedSessions[sessionID]
	e.mu.RUnlock()

	if fs == nil || len(fs.ViolationEvents) == 0 {
		return nil
	}

	events := fs.ViolationEvents
	start := events[0].Timestamp
	now := time.Now()
	duration := now.Sub(start)

	// Choose interval: ~50 points max
	interval := duration / 50
	if interval < time.Second {
		interval = time.Second
	}

	var points []RiskScorePoint

	for t := start; t.Before(now) || t.Equal(now); t = t.Add(interval) {
		score := e.scoreAt(events, t)
		action, _ := e.determineRiskAction(score)
		points = append(points, RiskScorePoint{
			Timestamp: t,
			Score:     math.Round(score*100) / 100,
			Action:    action,
		})
	}

	// Always include current point
	score := e.scoreAt(events, now)
	action, _ := e.determineRiskAction(score)
	points = append(points, RiskScorePoint{
		Timestamp: now,
		Score:     math.Round(score*100) / 100,
		Action:    action,
	})

	return points
}

// scoreAt computes the risk score at a specific moment in time.
func (e *Engine) scoreAt(events []ViolationEvent, at time.Time) float64 {
	var score float64
	for _, event := range events {
		if event.Timestamp.After(at) {
			continue // Event hasn't happened yet at this time
		}
		severityWeight := SeverityWeights[event.Severity]
		if severityWeight == 0 {
			severityWeight = 1.0
		}
		sourceWeight := SourceRoleWeights[event.SourceRole]
		if sourceWeight == 0 {
			sourceWeight = 1.0
		}
		elapsed := at.Sub(event.Timestamp).Seconds()
		decay := math.Exp(-DefaultDecayLambda * elapsed)
		score += severityWeight * sourceWeight * decay
	}
	if score > MaxRiskScore {
		score = MaxRiskScore
	}
	return score
}

// determineRiskAction determines the appropriate action based on risk score
func (e *Engine) determineRiskAction(score float64) (string, int) {
	action := string(ActionObserve)
	throttleRate := 0

	// Find the highest threshold that the score exceeds
	for _, threshold := range e.riskThresholds {
		if score >= threshold.Score {
			action = string(threshold.Action)
			if threshold.Action == ActionThrottle {
				throttleRate = threshold.ThrottleRate
			}
		}
	}

	return action, throttleRate
}

// calculateMaxSeverity returns the highest effective severity from violations.
// Uses EffectiveSeverity (source-weighted) when available, falls back to raw Severity.
func (e *Engine) calculateMaxSeverity(violations []Violation) Severity {
	maxSeverity := SeverityInfo

	for _, v := range violations {
		sev := v.EffectiveSeverity
		if sev == "" {
			sev = v.Severity // Fallback for violations without source attribution
		}
		if sev == SeverityCritical {
			return SeverityCritical
		}
		if sev == SeverityWarning && maxSeverity != SeverityCritical {
			maxSeverity = SeverityWarning
		}
	}

	return maxSeverity
}

// CaptureRequest captures request/response content for a flagged session
func (e *Engine) CaptureRequest(sessionID string, req CapturedRequest) {
	if !e.captureContent {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	flagged, exists := e.flaggedSessions[sessionID]
	if !exists {
		return // Only capture for flagged sessions
	}

	// Truncate content if too large
	if len(req.RequestBody) > e.maxCaptureSize {
		req.RequestBody = req.RequestBody[:e.maxCaptureSize] + "...[truncated]"
	}
	if len(req.ResponseBody) > e.maxCaptureSize {
		req.ResponseBody = req.ResponseBody[:e.maxCaptureSize] + "...[truncated]"
	}

	flagged.CapturedContent = append(flagged.CapturedContent, req)
}

// UpdateLastCaptureWithResponse updates the most recent captured request with response body
func (e *Engine) UpdateLastCaptureWithResponse(sessionID, responseBody string) {
	e.UpdateLastCaptureWithResponseAndStatus(sessionID, responseBody, 0)
}

// UpdateLastCaptureWithResponseAndStatus updates the most recent captured request with response body and status code
func (e *Engine) UpdateLastCaptureWithResponseAndStatus(sessionID, responseBody string, statusCode int) {
	if !e.captureContent {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	flagged, exists := e.flaggedSessions[sessionID]
	if !exists || len(flagged.CapturedContent) == 0 {
		return
	}

	// Update the last captured request with response body and status code
	lastIdx := len(flagged.CapturedContent) - 1
	if len(responseBody) > e.maxCaptureSize {
		responseBody = responseBody[:e.maxCaptureSize] + "...[truncated]"
	}
	flagged.CapturedContent[lastIdx].ResponseBody = responseBody
	if statusCode != 0 {
		flagged.CapturedContent[lastIdx].StatusCode = statusCode
	}
}

// IsFlagged checks if a session is flagged
func (e *Engine) IsFlagged(sessionID string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	_, exists := e.flaggedSessions[sessionID]
	return exists
}

// GetFlaggedSession returns a flagged session by ID
func (e *Engine) GetFlaggedSession(sessionID string) *FlaggedSession {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if flagged, exists := e.flaggedSessions[sessionID]; exists {
		// Return a copy
		copy := *flagged
		return &copy
	}
	return nil
}

// GetFlaggedSessions returns all flagged sessions
func (e *Engine) GetFlaggedSessions() []*FlaggedSession {
	e.mu.RLock()
	defer e.mu.RUnlock()

	result := make([]*FlaggedSession, 0, len(e.flaggedSessions))
	for _, flagged := range e.flaggedSessions {
		copy := *flagged
		result = append(result, &copy)
	}
	return result
}

// GetFlaggedSessionsBySeverity returns flagged sessions filtered by minimum severity
func (e *Engine) GetFlaggedSessionsBySeverity(minSeverity Severity) []*FlaggedSession {
	e.mu.RLock()
	defer e.mu.RUnlock()

	result := make([]*FlaggedSession, 0)
	for _, flagged := range e.flaggedSessions {
		if e.severityMeetsMinimum(flagged.MaxSeverity, minSeverity) {
			copy := *flagged
			result = append(result, &copy)
		}
	}
	return result
}

// severityMeetsMinimum checks if a severity meets the minimum threshold
func (e *Engine) severityMeetsMinimum(actual, minimum Severity) bool {
	severityOrder := map[Severity]int{
		SeverityInfo:     0,
		SeverityWarning:  1,
		SeverityCritical: 2,
	}
	return severityOrder[actual] >= severityOrder[minimum]
}

// RemoveFlaggedSession removes a flagged session (e.g., when session ends)
func (e *Engine) RemoveFlaggedSession(sessionID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.flaggedSessions, sessionID)
}

// Stats returns policy engine statistics
func (e *Engine) Stats() map[string]interface{} {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var critical, warning, info int
	var highRisk, throttled, blocked int
	var totalRiskScore float64

	for _, f := range e.flaggedSessions {
		switch f.MaxSeverity {
		case SeverityCritical:
			critical++
		case SeverityWarning:
			warning++
		case SeverityInfo:
			info++
		}

		// Risk ladder stats
		if f.RiskScore >= 30 {
			highRisk++
		}
		if f.CurrentAction == string(ActionThrottle) {
			throttled++
		}
		if f.CurrentAction == string(ActionBlock) {
			blocked++
		}
		totalRiskScore += f.RiskScore
	}

	avgRiskScore := 0.0
	if len(e.flaggedSessions) > 0 {
		avgRiskScore = totalRiskScore / float64(len(e.flaggedSessions))
	}

	return map[string]interface{}{
		"total_flagged":  len(e.flaggedSessions),
		"critical":       critical,
		"warning":        warning,
		"info":           info,
		"rules_count":    len(e.rules),
		"risk_ladder":    e.riskLadderEnabled,
		"high_risk":      highRisk,
		"throttled":      throttled,
		"blocked":        blocked,
		"avg_risk_score": avgRiskScore,
	}
}

// GetSessionRiskScore returns the risk score for a session
func (e *Engine) GetSessionRiskScore(sessionID string) (float64, string, int) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if flagged, exists := e.flaggedSessions[sessionID]; exists {
		return flagged.RiskScore, flagged.CurrentAction, flagged.ThrottleRate
	}
	return 0, string(ActionObserve), 0
}

// AddExternalRiskPoints adds risk points from an external source (e.g., M3-lite behavioral fingerprinting).
// Unlike policy violations, these are one-time additions (no decay, no violation events).
func (e *Engine) AddExternalRiskPoints(sessionID string, points int, source string) {
	if points <= 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	flagged, exists := e.flaggedSessions[sessionID]
	if !exists {
		flagged = &FlaggedSession{
			SessionID:       sessionID,
			FirstFlagged:    time.Now(),
			ViolationCounts: make(map[string]int),
		}
		e.flaggedSessions[sessionID] = flagged
	}
	flagged.LastFlagged = time.Now()
	flagged.RiskScore += float64(points)
	if flagged.RiskScore > MaxRiskScore {
		flagged.RiskScore = MaxRiskScore
	}

	if e.riskLadderEnabled {
		flagged.CurrentAction, flagged.ThrottleRate = e.determineRiskAction(flagged.RiskScore)
	}

	slog.Info("external risk points added",
		"session_id", sessionID,
		"points", points,
		"source", source,
		"risk_score", flagged.RiskScore,
		"action", flagged.CurrentAction,
	)
}

// ShouldThrottle returns true if the session should be rate limited
func (e *Engine) ShouldThrottle(sessionID string) (bool, int) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if flagged, exists := e.flaggedSessions[sessionID]; exists {
		if flagged.CurrentAction == string(ActionThrottle) {
			return true, flagged.ThrottleRate
		}
	}
	return false, 0
}

// ShouldBlockByRisk returns true if the session should be blocked based on risk score
func (e *Engine) ShouldBlockByRisk(sessionID string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if flagged, exists := e.flaggedSessions[sessionID]; exists {
		return flagged.CurrentAction == string(ActionBlock) || flagged.CurrentAction == string(ActionTerminate)
	}
	return false
}

// ShouldTerminateByRisk returns true if the session should be terminated based on risk score
func (e *Engine) ShouldTerminateByRisk(sessionID string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if flagged, exists := e.flaggedSessions[sessionID]; exists {
		return flagged.CurrentAction == string(ActionTerminate)
	}
	return false
}

// IsRiskLadderEnabled returns true if risk ladder is enabled
func (e *Engine) IsRiskLadderEnabled() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.riskLadderEnabled
}

// compileToolRules compiles tool_blocked and tool_argument_pattern rules
func compileToolRules(rules []Rule) []CompiledToolRule {
	var result []CompiledToolRule
	for _, rule := range rules {
		if len(rule.Patterns) == 0 {
			continue
		}
		switch rule.Type {
		case RuleTypeToolBlocked:
			result = append(result, CompiledToolRule{
				Rule:         rule,
				GlobPatterns: rule.Patterns,
			})
		case RuleTypeToolArgumentPattern:
			compiled := CompiledToolRule{Rule: rule}
			for _, pattern := range rule.Patterns {
				re, err := regexp.Compile("(?i)" + pattern)
				if err != nil {
					slog.Error("invalid regex pattern in tool argument rule",
						"rule", rule.Name,
						"pattern", pattern,
						"error", err,
					)
					continue
				}
				compiled.CompiledPatterns = append(compiled.CompiledPatterns, re)
			}
			result = append(result, compiled)
		}
	}
	return result
}

// EvaluateToolCalls checks extracted tool calls against tool call policy rules
func (e *Engine) EvaluateToolCalls(sessionID string, toolCalls []ToolCall) *ContentCheckResult {
	if len(toolCalls) == 0 {
		return nil
	}

	// Snapshot rules and audit mode under read lock
	e.mu.RLock()
	toolRules := e.compiledToolRules
	auditMode := e.auditMode
	e.mu.RUnlock()

	if len(toolRules) == 0 {
		return nil
	}

	result := &ContentCheckResult{}

	for _, cr := range toolRules {
		switch cr.Type {
		case RuleTypeToolBlocked:
			for _, tc := range toolCalls {
				for _, pattern := range cr.GlobPatterns {
					matched, err := filepath.Match(pattern, tc.Name)
					if err != nil {
						continue
					}
					if matched {
						violation := Violation{
							RuleName:       cr.Name,
							Description:    cr.Description,
							Severity:       cr.Severity,
							MatchedText:    tc.Name,
							MatchedPattern: pattern,
							Action:         cr.Action,
							Timestamp:      time.Now(),
						}
						result.Violations = append(result.Violations, violation)
						if !auditMode {
							switch cr.Action {
							case "block":
								result.ShouldBlock = true
							case "terminate":
								result.ShouldTerminate = true
								result.ShouldBlock = true
							}
						}
						logToolViolation(sessionID, cr, tc.Name, pattern, auditMode)
						e.recordViolations(sessionID, []Violation{violation})
						break // One match per tool per rule is enough
					}
				}
			}
		case RuleTypeToolArgumentPattern:
			for _, tc := range toolCalls {
				if tc.Arguments == "" {
					continue
				}
				for i, re := range cr.CompiledPatterns {
					if match := re.FindString(tc.Arguments); match != "" {
						violation := Violation{
							RuleName:       cr.Name,
							Description:    cr.Description,
							Severity:       cr.Severity,
							MatchedText:    truncateMatch(match, 100),
							MatchedPattern: cr.Patterns[i],
							Action:         cr.Action,
							Timestamp:      time.Now(),
						}
						result.Violations = append(result.Violations, violation)
						if !auditMode {
							switch cr.Action {
							case "block":
								result.ShouldBlock = true
							case "terminate":
								result.ShouldTerminate = true
								result.ShouldBlock = true
							}
						}
						logToolViolation(sessionID, cr, tc.Name+"(args)", cr.Patterns[i], auditMode)
						e.recordViolations(sessionID, []Violation{violation})
						break // One match per tool per rule is enough
					}
				}
			}
		}
	}

	if len(result.Violations) > 0 {
		return result
	}
	return nil
}

// HasBlockingToolRules returns true if any tool call rules have block/terminate action
func (e *Engine) HasBlockingToolRules() bool {
	e.mu.RLock()
	toolRules := e.compiledToolRules
	e.mu.RUnlock()

	for _, cr := range toolRules {
		if cr.Action == "block" || cr.Action == "terminate" {
			return true
		}
	}
	return false
}

func logToolViolation(sessionID string, cr CompiledToolRule, matched, pattern string, auditMode bool) {
	actionMsg := cr.Action
	if auditMode {
		actionMsg = cr.Action + " (audit-only)"
	}
	slog.Warn("tool call policy violation detected",
		"session_id", sessionID,
		"rule", cr.Name,
		"severity", cr.Severity,
		"action", actionMsg,
		"matched", matched,
		"pattern", pattern,
		"audit_mode", auditMode,
	)
}

// StreamingScanner handles chunk-based content scanning with overlap for cross-boundary patterns
type StreamingScanner struct {
	engine       *Engine
	sessionID    string
	overlapBuf   []byte
	overlapSize  int
	totalScanned int64
	fullContent  []byte // Accumulated content for entropy evaluation on Finalize
}

// NewStreamingScanner creates a scanner for chunked response scanning
func (e *Engine) NewStreamingScanner(sessionID string, overlapSize int) *StreamingScanner {
	if overlapSize <= 0 {
		overlapSize = 1024 // Default 1KB overlap
	}
	return &StreamingScanner{
		engine:      e,
		sessionID:   sessionID,
		overlapBuf:  make([]byte, 0, overlapSize),
		overlapSize: overlapSize,
	}
}

// ScanChunk scans a chunk of streaming content, using overlap buffer for cross-boundary patterns
// Returns violations if found. The caller should terminate the stream if ShouldBlock/ShouldTerminate.
func (s *StreamingScanner) ScanChunk(chunk []byte) *ContentCheckResult {
	if len(chunk) == 0 {
		return nil
	}

	// Combine overlap buffer with current chunk for scanning
	var scanContent []byte
	if len(s.overlapBuf) > 0 {
		scanContent = make([]byte, len(s.overlapBuf)+len(chunk))
		copy(scanContent, s.overlapBuf)
		copy(scanContent[len(s.overlapBuf):], chunk)
	} else {
		scanContent = chunk
	}

	// Scan the combined content
	result := s.engine.EvaluateResponseContent(s.sessionID, string(scanContent))

	// Update overlap buffer with end of current chunk
	if len(chunk) >= s.overlapSize {
		s.overlapBuf = make([]byte, s.overlapSize)
		copy(s.overlapBuf, chunk[len(chunk)-s.overlapSize:])
	} else {
		// Chunk smaller than overlap - append to existing overlap
		combined := append(s.overlapBuf, chunk...)
		if len(combined) > s.overlapSize {
			s.overlapBuf = combined[len(combined)-s.overlapSize:]
		} else {
			s.overlapBuf = combined
		}
	}

	// Accumulate full content for entropy evaluation on Finalize
	s.fullContent = append(s.fullContent, chunk...)

	s.totalScanned += int64(len(chunk))
	return result
}

// Finalize performs a final scan on any remaining overlap buffer and runs
// entropy evaluation on the full accumulated content.
func (s *StreamingScanner) Finalize() *ContentCheckResult {
	// Run entropy evaluation on the full accumulated content
	var entropyResult *ContentCheckResult
	if len(s.fullContent) > 0 {
		entropyResult = s.evaluateEntropy()
	}

	var overlapResult *ContentCheckResult
	if len(s.overlapBuf) > 0 {
		// Final scan of overlap buffer (in case pattern is at the very end)
		overlapResult = s.engine.EvaluateResponseContent(s.sessionID, string(s.overlapBuf))
	}

	return mergeContentResults(overlapResult, entropyResult)
}

// evaluateEntropy runs entropy rules against the full accumulated stream content
func (s *StreamingScanner) evaluateEntropy() *ContentCheckResult {
	s.engine.mu.RLock()
	rules := s.engine.rules
	auditMode := s.engine.auditMode
	s.engine.mu.RUnlock()

	var result *ContentCheckResult
	content := string(s.fullContent)
	for _, rule := range rules {
		if rule.Type != RuleTypeContentEntropy {
			continue
		}
		if !ruleAppliesToTarget(rule.Target, RuleTargetResponse) {
			continue
		}
		if v := s.engine.evaluateContentEntropy(s.sessionID, content, rule, nil); v != nil {
			if result == nil {
				result = &ContentCheckResult{}
			}
			result.Violations = append(result.Violations, *v)
			if !auditMode {
				switch rule.Action {
				case "block":
					result.ShouldBlock = true
				case "terminate":
					result.ShouldTerminate = true
					result.ShouldBlock = true
				}
			}
			s.engine.recordViolations(s.sessionID, []Violation{*v})
		}
	}
	return result
}

// mergeContentResults combines two ContentCheckResults
func mergeContentResults(a, b *ContentCheckResult) *ContentCheckResult {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	merged := &ContentCheckResult{
		Violations:      append(a.Violations, b.Violations...),
		ShouldBlock:     a.ShouldBlock || b.ShouldBlock,
		ShouldTerminate: a.ShouldTerminate || b.ShouldTerminate,
	}
	return merged
}

// TotalScanned returns total bytes scanned so far
func (s *StreamingScanner) TotalScanned() int64 {
	return s.totalScanned
}

// Reset clears the scanner state for reuse
func (s *StreamingScanner) Reset() {
	s.overlapBuf = s.overlapBuf[:0]
	s.fullContent = s.fullContent[:0]
	s.totalScanned = 0
}

// effectiveSeverity computes a downgraded severity based on source role.
// A critical rule triggered by an assistant echo is less concerning than one from user input.
func effectiveSeverity(ruleSeverity Severity, sourceRole string) Severity {
	weight := SourceRoleWeights[sourceRole]
	if weight == 0 {
		weight = 1.0
	}

	baseWeight := SeverityWeights[ruleSeverity]
	effective := baseWeight * weight

	switch {
	case effective >= 5.0:
		return SeverityCritical
	case effective >= 1.5:
		return SeverityWarning
	default:
		return SeverityInfo
	}
}

// categoryFromRuleName derives an event category from the rule name for SIEM classification.
// Categories align with common SIEM taxonomy: prompt_injection, data_exfil, rate_limit, etc.
func categoryFromRuleName(name string) string {
	switch {
	case strings.HasPrefix(name, "prompt_injection"):
		return "prompt_injection"
	case strings.HasPrefix(name, "rate_limit") || strings.HasPrefix(name, "requests_per_minute"):
		return "rate_limit"
	case strings.Contains(name, "exfiltration") || strings.Contains(name, "extraction"):
		return "data_exfil"
	case strings.HasPrefix(name, "destructive") || strings.Contains(name, "privilege"):
		return "dangerous_command"
	case strings.Contains(name, "pii") || strings.Contains(name, "credential") || strings.Contains(name, "secret"):
		return "sensitive_data"
	case strings.HasPrefix(name, "high_request") || strings.HasPrefix(name, "very_high") ||
		strings.HasPrefix(name, "long_running") || strings.HasPrefix(name, "excessive"):
		return "resource_abuse"
	case strings.HasPrefix(name, "large_response") || strings.Contains(name, "bytes"):
		return "data_volume"
	case strings.Contains(name, "recursive") || strings.Contains(name, "dos"):
		return "denial_of_service"
	case strings.Contains(name, "model"):
		return "model_abuse"
	default:
		return "policy_violation"
	}
}

// frameworkRefFromDescription extracts a framework reference (e.g., "OWASP-LLM01") from the rule description.
// Preset rules embed the framework ID in the description prefix (e.g., "LLM01: Prompt injection...").
func frameworkRefFromDescription(desc string) string {
	// Match "LLM01", "LLM02", etc. — OWASP LLM Top 10
	if strings.HasPrefix(desc, "LLM") {
		if idx := strings.Index(desc, ":"); idx > 0 && idx <= 6 {
			return "OWASP-" + desc[:idx]
		}
	}
	// Match "FIREWALL:" prefix — WAF-style rate/volume rules
	if strings.HasPrefix(desc, "FIREWALL:") {
		return "ELIDA-FIREWALL"
	}
	return ""
}
