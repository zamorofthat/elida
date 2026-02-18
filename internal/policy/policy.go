package policy

import (
	"log/slog"
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

	// Token-based rules
	RuleTypeTokensIn       RuleType = "tokens_in"
	RuleTypeTokensOut      RuleType = "tokens_out"
	RuleTypeTokensTotal    RuleType = "tokens_total"
	RuleTypeTokensPerMin   RuleType = "tokens_per_minute"

	// Tool call rules
	RuleTypeToolCallCount  RuleType = "tool_call_count"
	RuleTypeToolFanout     RuleType = "tool_fanout" // Distinct tools used

	// Content inspection rules
	RuleTypeContentMatch RuleType = "content_match" // Match patterns in request/response body
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
	Name        string     `yaml:"name" json:"name"`
	Type        RuleType   `yaml:"type" json:"type"`
	Target      RuleTarget `yaml:"target" json:"target"`               // request, response, both (default: both)
	Threshold   int64      `yaml:"threshold" json:"threshold"`         // For metric rules
	Patterns    []string   `yaml:"patterns" json:"patterns,omitempty"` // For content_match rules (regex)
	Severity    Severity   `yaml:"severity" json:"severity"`
	Description string     `yaml:"description" json:"description"`
	Action      string     `yaml:"action" json:"action,omitempty"` // "flag", "block", "terminate"
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
	ToolCalls   int
	ToolFanout  int // Distinct tools used
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
	RiskScore       float64        `json:"risk_score"`        // Cumulative weighted risk score
	ViolationCounts map[string]int `json:"violation_counts"`  // Count per rule (not deduplicated)
	CurrentAction   string         `json:"current_action"`    // Current ladder action based on score
	ThrottleRate    int            `json:"throttle_rate"`     // Requests per minute when throttled (0 = no throttle)
}

// SeverityWeights defines risk score multipliers for each severity level
var SeverityWeights = map[Severity]float64{
	SeverityInfo:     1.0,
	SeverityWarning:  3.0,
	SeverityCritical: 10.0,
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

// CompiledRule is a rule with pre-compiled regex patterns
type CompiledRule struct {
	Rule
	CompiledPatterns []*regexp.Regexp
}

// Engine evaluates sessions against policy rules
type Engine struct {
	mu              sync.RWMutex
	rules           []Rule
	compiledRules   []CompiledRule // Rules with compiled regex
	flaggedSessions map[string]*FlaggedSession
	captureContent  bool
	maxCaptureSize  int  // Max bytes to capture per request
	auditMode       bool // If true, log but don't enforce (dry-run)

	// Risk ladder configuration
	riskLadderEnabled bool
	riskThresholds    []RiskThreshold
}

// Config for the policy engine
type Config struct {
	Enabled        bool   `yaml:"enabled"`
	Mode           string `yaml:"mode"` // "enforce" (default) or "audit"
	CaptureContent bool   `yaml:"capture_flagged"`
	MaxCaptureSize int    `yaml:"max_capture_size"`
	Rules          []Rule `yaml:"rules"`

	// Risk ladder configuration
	RiskLadder RiskLadderConfig `yaml:"risk_ladder"`
}

// RiskLadderConfig configures progressive escalation based on cumulative risk score
type RiskLadderConfig struct {
	Enabled    bool            `yaml:"enabled"`
	Thresholds []RiskThreshold `yaml:"thresholds"`
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

	mode := "enforce"
	if auditMode {
		mode = "audit"
	}
	slog.Info("policy engine initialized",
		"rules", len(cfg.Rules),
		"content_rules", len(e.compiledRules),
		"capture_content", cfg.CaptureContent,
		"mode", mode,
	)

	return e
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

// ContentCheckResult contains the result of content inspection
type ContentCheckResult struct {
	Violations      []Violation
	ShouldBlock     bool
	ShouldTerminate bool
}

// EvaluateContent checks request content against content rules (backward compatible)
func (e *Engine) EvaluateContent(sessionID, content string) *ContentCheckResult {
	return e.EvaluateRequestContent(sessionID, content)
}

// EvaluateRequestContent checks request content against request-applicable rules
func (e *Engine) EvaluateRequestContent(sessionID, content string) *ContentCheckResult {
	return e.evaluateContentWithTarget(sessionID, content, RuleTargetRequest)
}

// EvaluateResponseContent checks response content against response-applicable rules
func (e *Engine) EvaluateResponseContent(sessionID, content string) *ContentCheckResult {
	return e.evaluateContentWithTarget(sessionID, content, RuleTargetResponse)
}

// evaluateContentWithTarget is the internal implementation that filters by target
func (e *Engine) evaluateContentWithTarget(sessionID, content string, target RuleTarget) *ContentCheckResult {
	if len(e.compiledRules) == 0 || content == "" {
		return nil
	}

	result := &ContentCheckResult{}
	contentLower := strings.ToLower(content)

	for _, cr := range e.compiledRules {
		// Skip rules that don't apply to this target
		if !e.ruleAppliesToTarget(cr.Target, target) {
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
				}
				result.Violations = append(result.Violations, violation)

				// Only enforce actions if not in audit mode
				if !e.auditMode {
					switch cr.Action {
					case "block":
						result.ShouldBlock = true
					case "terminate":
						result.ShouldTerminate = true
						result.ShouldBlock = true
					}
				}

				// Log with audit mode indicator
				logFunc := slog.Warn
				actionMsg := cr.Action
				if e.auditMode {
					actionMsg = cr.Action + " (audit-only)"
				}

				targetStr := "request"
				if target == RuleTargetResponse {
					targetStr = "response"
				}

				logFunc("content policy violation detected",
					"session_id", sessionID,
					"rule", cr.Name,
					"severity", cr.Severity,
					"action", actionMsg,
					"target", targetStr,
					"matched", truncateMatch(match, 50),
					"audit_mode", e.auditMode,
				)

				// Record the violation
				e.recordViolations(sessionID, []Violation{violation})
				break // One match per rule is enough
			}
		}
	}

	if len(result.Violations) > 0 {
		return result
	}
	return nil
}

// ruleAppliesToTarget checks if a rule should be evaluated for the given target
func (e *Engine) ruleAppliesToTarget(ruleTarget RuleTarget, evaluationTarget RuleTarget) bool {
	// Default (empty or "both") applies to everything
	if ruleTarget == "" || ruleTarget == RuleTargetBoth {
		return true
	}
	return ruleTarget == evaluationTarget
}

// HasBlockingResponseRules returns true if any response rules have block/terminate action
func (e *Engine) HasBlockingResponseRules() bool {
	for _, cr := range e.compiledRules {
		if e.ruleAppliesToTarget(cr.Target, RuleTargetResponse) {
			if cr.Action == "block" || cr.Action == "terminate" {
				return true
			}
		}
	}
	return false
}

// IsAuditMode returns true if the engine is in audit (dry-run) mode
func (e *Engine) IsAuditMode() bool {
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

// calculateRiskScore computes cumulative weighted risk score
func (e *Engine) calculateRiskScore(fs *FlaggedSession) float64 {
	var score float64
	for _, v := range fs.Violations {
		count := fs.ViolationCounts[v.RuleName]
		if count == 0 {
			count = 1 // At least 1 occurrence
		}
		weight := SeverityWeights[v.Severity]
		if weight == 0 {
			weight = 1.0 // Default weight
		}
		score += float64(count) * weight
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

// calculateMaxSeverity returns the highest severity from violations
func (e *Engine) calculateMaxSeverity(violations []Violation) Severity {
	maxSeverity := SeverityInfo

	for _, v := range violations {
		if v.Severity == SeverityCritical {
			return SeverityCritical
		}
		if v.Severity == SeverityWarning && maxSeverity != SeverityCritical {
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
		"total_flagged":    len(e.flaggedSessions),
		"critical":         critical,
		"warning":          warning,
		"info":             info,
		"rules_count":      len(e.rules),
		"risk_ladder":      e.riskLadderEnabled,
		"high_risk":        highRisk,
		"throttled":        throttled,
		"blocked":          blocked,
		"avg_risk_score":   avgRiskScore,
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
	return e.riskLadderEnabled
}

// StreamingScanner handles chunk-based content scanning with overlap for cross-boundary patterns
type StreamingScanner struct {
	engine       *Engine
	sessionID    string
	overlapBuf   []byte
	overlapSize  int
	totalScanned int64
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

	s.totalScanned += int64(len(chunk))
	return result
}

// Finalize performs a final scan on any remaining overlap buffer
// Call this when the stream ends to catch patterns at the very end
func (s *StreamingScanner) Finalize() *ContentCheckResult {
	if len(s.overlapBuf) == 0 {
		return nil
	}
	// Final scan of overlap buffer (in case pattern is at the very end)
	return s.engine.EvaluateResponseContent(s.sessionID, string(s.overlapBuf))
}

// TotalScanned returns total bytes scanned so far
func (s *StreamingScanner) TotalScanned() int64 {
	return s.totalScanned
}

// Reset clears the scanner state for reuse
func (s *StreamingScanner) Reset() {
	s.overlapBuf = s.overlapBuf[:0]
	s.totalScanned = 0
}
