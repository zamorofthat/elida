package policy

import (
	"log/slog"
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
	RuleTypeBytesOut        RuleType = "bytes_out"
	RuleTypeBytesIn         RuleType = "bytes_in"
	RuleTypeBytesTotal      RuleType = "bytes_total"
	RuleTypeRequestCount    RuleType = "request_count"
	RuleTypeDuration        RuleType = "duration"
	RuleTypeRequestsPerMin  RuleType = "requests_per_minute"
	RuleTypeIdleTime        RuleType = "idle_time"
)

// Rule defines a policy rule
type Rule struct {
	Name        string   `yaml:"name" json:"name"`
	Type        RuleType `yaml:"type" json:"type"`
	Threshold   int64    `yaml:"threshold" json:"threshold"`
	Severity    Severity `yaml:"severity" json:"severity"`
	Description string   `yaml:"description" json:"description"`
}

// Violation represents a policy violation
type Violation struct {
	RuleName    string    `json:"rule_name"`
	Description string    `json:"description"`
	Severity    Severity  `json:"severity"`
	Threshold   int64     `json:"threshold"`
	ActualValue int64     `json:"actual_value"`
	Timestamp   time.Time `json:"timestamp"`
}

// SessionMetrics contains the metrics needed for policy evaluation
type SessionMetrics struct {
	SessionID       string
	BytesIn         int64
	BytesOut        int64
	RequestCount    int
	Duration        time.Duration
	IdleTime        time.Duration
	StartTime       time.Time
	RequestTimes    []time.Time // For rate calculation
}

// FlaggedSession tracks a session that has policy violations
type FlaggedSession struct {
	SessionID      string       `json:"session_id"`
	Violations     []Violation  `json:"violations"`
	MaxSeverity    Severity     `json:"max_severity"`
	FirstFlagged   time.Time    `json:"first_flagged"`
	LastFlagged    time.Time    `json:"last_flagged"`
	CapturedContent []CapturedRequest `json:"captured_content,omitempty"`
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

// Engine evaluates sessions against policy rules
type Engine struct {
	mu              sync.RWMutex
	rules           []Rule
	flaggedSessions map[string]*FlaggedSession
	captureContent  bool
	maxCaptureSize  int // Max bytes to capture per request
}

// Config for the policy engine
type Config struct {
	Enabled        bool   `yaml:"enabled"`
	CaptureContent bool   `yaml:"capture_flagged"`
	MaxCaptureSize int    `yaml:"max_capture_size"`
	Rules          []Rule `yaml:"rules"`
}

// NewEngine creates a new policy engine
func NewEngine(cfg Config) *Engine {
	if cfg.MaxCaptureSize == 0 {
		cfg.MaxCaptureSize = 10000 // Default 10KB per request
	}

	e := &Engine{
		rules:           cfg.Rules,
		flaggedSessions: make(map[string]*FlaggedSession),
		captureContent:  cfg.CaptureContent,
		maxCaptureSize:  cfg.MaxCaptureSize,
	}

	slog.Info("policy engine initialized",
		"rules", len(cfg.Rules),
		"capture_content", cfg.CaptureContent,
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

// recordViolations records violations for a session
func (e *Engine) recordViolations(sessionID string, violations []Violation) {
	e.mu.Lock()
	defer e.mu.Unlock()

	flagged, exists := e.flaggedSessions[sessionID]
	if !exists {
		flagged = &FlaggedSession{
			SessionID:    sessionID,
			FirstFlagged: time.Now(),
			Violations:   []Violation{},
		}
		e.flaggedSessions[sessionID] = flagged
	}

	// Add new violations (avoid duplicates by rule name)
	existingRules := make(map[string]bool)
	for _, v := range flagged.Violations {
		existingRules[v.RuleName] = true
	}

	for _, v := range violations {
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
	for _, f := range e.flaggedSessions {
		switch f.MaxSeverity {
		case SeverityCritical:
			critical++
		case SeverityWarning:
			warning++
		case SeverityInfo:
			info++
		}
	}

	return map[string]interface{}{
		"total_flagged": len(e.flaggedSessions),
		"critical":      critical,
		"warning":       warning,
		"info":          info,
		"rules_count":   len(e.rules),
	}
}
