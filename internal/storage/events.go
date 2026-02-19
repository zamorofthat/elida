package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// EventType defines the type of event
type EventType string

const (
	EventSessionStarted     EventType = "session_started"
	EventSessionEnded       EventType = "session_ended"
	EventViolationDetected  EventType = "violation_detected"
	EventCaptureRecorded    EventType = "capture_recorded"
	EventPolicyAction       EventType = "policy_action"
	EventKillRequested      EventType = "kill_requested"
	EventTerminateRequested EventType = "terminate_requested"
	EventRiskEscalated      EventType = "risk_escalated"
	EventToolCalled         EventType = "tool_called"
	EventTokensUsed         EventType = "tokens_used"
)

// Event represents an immutable audit event
type Event struct {
	ID        int64           `json:"id"`
	Timestamp time.Time       `json:"timestamp"`
	Type      EventType       `json:"type"`
	SessionID string          `json:"session_id"`
	Severity  string          `json:"severity,omitempty"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"created_at"`
}

// EventData is the base interface for event data
type EventData interface{}

// SessionStartedData contains data for session_started events
type SessionStartedData struct {
	ClientAddr string `json:"client_addr"`
	Backend    string `json:"backend"`
}

// SessionEndedData contains data for session_ended events
type SessionEndedData struct {
	State        string `json:"state"`
	DurationMs   int64  `json:"duration_ms"`
	RequestCount int    `json:"request_count"`
	BytesIn      int64  `json:"bytes_in"`
	BytesOut     int64  `json:"bytes_out"`
	TokensIn     int64  `json:"tokens_in,omitempty"`
	TokensOut    int64  `json:"tokens_out,omitempty"`
	ToolCalls    int    `json:"tool_calls,omitempty"`
}

// ViolationDetectedData contains data for violation_detected events
type ViolationDetectedData struct {
	RuleName    string `json:"rule_name"`
	Description string `json:"description"`
	Severity    string `json:"severity"`
	Action      string `json:"action"`
	MatchedText string `json:"matched_text,omitempty"` // May be redacted
}

// PolicyActionData contains data for policy_action events
type PolicyActionData struct {
	Action    string  `json:"action"`
	RiskScore float64 `json:"risk_score,omitempty"`
	Reason    string  `json:"reason"`
}

// ToolCalledData contains data for tool_called events
type ToolCalledData struct {
	ToolName  string `json:"tool_name"`
	ToolType  string `json:"tool_type,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	CallCount int    `json:"call_count,omitempty"`
}

// TokensUsedData contains data for tokens_used events
type TokensUsedData struct {
	TokensIn  int64 `json:"tokens_in"`
	TokensOut int64 `json:"tokens_out"`
}

// ListEventsOptions contains options for listing events
type ListEventsOptions struct {
	Limit     int
	Offset    int
	SessionID string
	Type      EventType
	Severity  string
	Since     *time.Time
	Until     *time.Time
}

// RecordEvent records an immutable event
func (s *SQLiteStore) RecordEvent(ctx context.Context, eventType EventType, sessionID string, severity string, data interface{}) error {
	dataJSON, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal event data: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO events (timestamp, event_type, session_id, severity, data)
		VALUES (?, ?, ?, ?, ?)`,
		time.Now(),
		string(eventType),
		sessionID,
		severity,
		string(dataJSON),
	)
	if err != nil {
		return fmt.Errorf("failed to record event: %w", err)
	}

	return nil
}

// ListEvents retrieves events with filtering and pagination
func (s *SQLiteStore) ListEvents(opts ListEventsOptions) ([]Event, error) {
	query := `
		SELECT id, timestamp, event_type, session_id, severity, data, created_at
		FROM events WHERE 1=1`

	args := []interface{}{}

	if opts.SessionID != "" {
		query += " AND session_id = ?"
		args = append(args, opts.SessionID)
	}
	if opts.Type != "" {
		query += " AND event_type = ?"
		args = append(args, string(opts.Type))
	}
	if opts.Severity != "" {
		query += " AND severity = ?"
		args = append(args, opts.Severity)
	}
	if opts.Since != nil {
		query += " AND timestamp >= ?"
		args = append(args, *opts.Since)
	}
	if opts.Until != nil {
		query += " AND timestamp <= ?"
		args = append(args, *opts.Until)
	}

	query += " ORDER BY timestamp DESC"

	if opts.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, opts.Limit)
	}
	if opts.Offset > 0 {
		query += " OFFSET ?"
		args = append(args, opts.Offset)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list events: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var event Event
		var severity sql.NullString
		var dataStr string

		err := rows.Scan(
			&event.ID,
			&event.Timestamp,
			&event.Type,
			&event.SessionID,
			&severity,
			&dataStr,
			&event.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan event: %w", err)
		}

		if severity.Valid {
			event.Severity = severity.String
		}
		event.Data = json.RawMessage(dataStr)

		events = append(events, event)
	}

	return events, nil
}

// GetSessionEvents retrieves all events for a session
func (s *SQLiteStore) GetSessionEvents(sessionID string) ([]Event, error) {
	return s.ListEvents(ListEventsOptions{
		SessionID: sessionID,
	})
}

// EventStats represents aggregate event statistics
type EventStats struct {
	TotalEvents      int64            `json:"total_events"`
	EventsByType     map[string]int64 `json:"events_by_type"`
	EventsBySeverity map[string]int64 `json:"events_by_severity"`
	UniqueSessionIDs int64            `json:"unique_session_ids"`
}

// GetEventStats retrieves aggregate event statistics
func (s *SQLiteStore) GetEventStats(since *time.Time) (*EventStats, error) {
	stats := &EventStats{
		EventsByType:     make(map[string]int64),
		EventsBySeverity: make(map[string]int64),
	}

	whereClause := "WHERE 1=1"
	args := []interface{}{}
	if since != nil {
		whereClause += " AND timestamp >= ?"
		args = append(args, *since)
	}

	// Total events
	row := s.db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM events %s`, whereClause), args...)
	if err := row.Scan(&stats.TotalEvents); err != nil {
		return nil, fmt.Errorf("failed to get total events: %w", err)
	}

	// Unique sessions
	row = s.db.QueryRow(fmt.Sprintf(`SELECT COUNT(DISTINCT session_id) FROM events %s`, whereClause), args...)
	if err := row.Scan(&stats.UniqueSessionIDs); err != nil {
		return nil, fmt.Errorf("failed to get unique sessions: %w", err)
	}

	// Events by type
	rows, err := s.db.Query(fmt.Sprintf(`SELECT event_type, COUNT(*) FROM events %s GROUP BY event_type`, whereClause), args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get events by type: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var eventType string
		var count int64
		if scanErr := rows.Scan(&eventType, &count); scanErr != nil {
			return nil, scanErr
		}
		stats.EventsByType[eventType] = count
	}

	// Events by severity
	rows, err = s.db.Query(fmt.Sprintf(`SELECT COALESCE(severity, 'none'), COUNT(*) FROM events %s GROUP BY severity`, whereClause), args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get events by severity: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var severity string
		var count int64
		if scanErr := rows.Scan(&severity, &count); scanErr != nil {
			return nil, scanErr
		}
		stats.EventsBySeverity[severity] = count
	}

	return stats, nil
}

// CleanupEvents removes old events based on retention
func (s *SQLiteStore) CleanupEvents(retentionDays int) (int64, error) {
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	result, err := s.db.Exec("DELETE FROM events WHERE timestamp < ?", cutoff)
	if err != nil {
		return 0, fmt.Errorf("failed to cleanup old events: %w", err)
	}

	deleted, _ := result.RowsAffected()
	return deleted, nil
}
