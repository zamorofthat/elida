package mcp

import (
	"encoding/json"
	"sort"
	"time"

	"elida/internal/policy"
	"elida/internal/session"
)

type sessionSummary struct {
	ID            string            `json:"id"`
	State         string            `json:"state"`
	StartTime     time.Time         `json:"start_time"`
	Duration      string            `json:"duration"`
	RequestCount  int               `json:"request_count"`
	BytesIn       int64             `json:"bytes_in"`
	BytesOut      int64             `json:"bytes_out"`
	Backend       string            `json:"backend"`
	ClientAddr    string            `json:"client_addr"`
	RiskScore     float64           `json:"risk_score,omitempty"`
	CurrentAction string            `json:"current_action,omitempty"`
	TokensIn      int64             `json:"tokens_in,omitempty"`
	TokensOut     int64             `json:"tokens_out,omitempty"`
	ToolCalls     int               `json:"tool_calls,omitempty"`
	ToolCounts    map[string]int    `json:"tool_counts,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	Terminated    bool              `json:"terminated,omitempty"`
}

func (s *Server) buildSummary(sess *session.Session) sessionSummary {
	snap := sess.Snapshot()
	tokIn, tokOut := sess.GetTokens()
	sum := sessionSummary{
		ID:           snap.ID,
		State:        snap.State.String(),
		StartTime:    snap.StartTime,
		Duration:     sess.Duration().String(),
		RequestCount: snap.RequestCount,
		BytesIn:      snap.BytesIn,
		BytesOut:     snap.BytesOut,
		Backend:      snap.Backend,
		ClientAddr:   snap.ClientAddr,
		TokensIn:     tokIn,
		TokensOut:    tokOut,
		ToolCalls:    snap.ToolCalls,
		ToolCounts:   sess.GetToolCallCounts(),
		Metadata:     snap.Metadata,
		Terminated:   snap.Terminated,
	}

	s.mu.RLock()
	pe := s.policyEngine
	s.mu.RUnlock()

	if pe != nil {
		if flagged := pe.GetFlaggedSession(snap.ID); flagged != nil {
			sum.RiskScore = flagged.RiskScore
			sum.CurrentAction = flagged.CurrentAction
		}
	}
	return sum
}

func (s *Server) toolGetStats() (any, *jsonRPCError) {
	stats := s.manager.Stats()
	return stats, nil
}

func (s *Server) toolListSessions(args json.RawMessage) (any, *jsonRPCError) {
	var params struct {
		State  string `json:"state"`
		Limit  int    `json:"limit"`
		SortBy string `json:"sort_by"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, &jsonRPCError{Code: errCodeInvalidParams, Message: "invalid arguments"}
	}
	if params.Limit <= 0 {
		params.Limit = 20
	}

	var sessions []*session.Session
	switch params.State {
	case "active":
		sessions = s.manager.ListActive()
	case "":
		sessions = s.manager.ListAll()
	default:
		all := s.manager.ListAll()
		for _, sess := range all {
			if sess.GetState().String() == params.State {
				sessions = append(sessions, sess)
			}
		}
	}

	// Build summaries
	summaries := make([]sessionSummary, 0, len(sessions))
	for _, sess := range sessions {
		summaries = append(summaries, s.buildSummary(sess))
	}

	// Sort
	switch params.SortBy {
	case "risk":
		sort.Slice(summaries, func(i, j int) bool { return summaries[i].RiskScore > summaries[j].RiskScore })
	case "requests":
		sort.Slice(summaries, func(i, j int) bool { return summaries[i].RequestCount > summaries[j].RequestCount })
	case "bytes_out":
		sort.Slice(summaries, func(i, j int) bool { return summaries[i].BytesOut > summaries[j].BytesOut })
	default: // start_time (newest first)
		sort.Slice(summaries, func(i, j int) bool { return summaries[i].StartTime.After(summaries[j].StartTime) })
	}

	if len(summaries) > params.Limit {
		summaries = summaries[:params.Limit]
	}

	return map[string]any{
		"total":    len(summaries),
		"sessions": summaries,
	}, nil
}

func (s *Server) toolGetSession(args json.RawMessage) (any, *jsonRPCError) {
	var params struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil || params.SessionID == "" {
		return nil, &jsonRPCError{Code: errCodeInvalidParams, Message: "session_id required"}
	}

	sess, ok := s.manager.Get(params.SessionID)
	if !ok {
		return nil, &jsonRPCError{Code: errCodeNotFound, Message: "session not found"}
	}

	return s.buildSummary(sess), nil
}

func (s *Server) toolGetOutliers(args json.RawMessage) (any, *jsonRPCError) {
	var params struct {
		TopN   int    `json:"top_n"`
		Metric string `json:"metric"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, &jsonRPCError{Code: errCodeInvalidParams, Message: "invalid arguments"}
	}
	if params.TopN <= 0 {
		params.TopN = 5
	}
	if params.Metric == "" {
		params.Metric = "risk"
	}

	sessions := s.manager.ListAll()
	summaries := make([]sessionSummary, 0, len(sessions))
	for _, sess := range sessions {
		summaries = append(summaries, s.buildSummary(sess))
	}

	switch params.Metric {
	case "risk":
		sort.Slice(summaries, func(i, j int) bool { return summaries[i].RiskScore > summaries[j].RiskScore })
	case "requests":
		sort.Slice(summaries, func(i, j int) bool { return summaries[i].RequestCount > summaries[j].RequestCount })
	case "tokens":
		sort.Slice(summaries, func(i, j int) bool {
			return (summaries[i].TokensIn + summaries[i].TokensOut) > (summaries[j].TokensIn + summaries[j].TokensOut)
		})
	case "bytes_out":
		sort.Slice(summaries, func(i, j int) bool { return summaries[i].BytesOut > summaries[j].BytesOut })
	}

	if len(summaries) > params.TopN {
		summaries = summaries[:params.TopN]
	}

	return map[string]any{
		"metric":   params.Metric,
		"outliers": summaries,
	}, nil
}

type timelineEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"` // "tool_call", "violation", "message"
	Detail    any       `json:"detail"`
}

func (s *Server) toolGetTimeline(args json.RawMessage) (any, *jsonRPCError) {
	var params struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil || params.SessionID == "" {
		return nil, &jsonRPCError{Code: errCodeInvalidParams, Message: "session_id required"}
	}

	sess, ok := s.manager.Get(params.SessionID)
	if !ok {
		return nil, &jsonRPCError{Code: errCodeNotFound, Message: "session not found"}
	}

	snap := sess.Snapshot()
	var entries []timelineEntry

	// Tool call history
	for _, tc := range snap.ToolCallHistory {
		entries = append(entries, timelineEntry{
			Timestamp: tc.Timestamp,
			Type:      "tool_call",
			Detail: map[string]any{
				"tool_name": tc.ToolName,
				"tool_type": tc.ToolType,
			},
		})
	}

	// Messages
	for i, msg := range snap.Messages {
		entries = append(entries, timelineEntry{
			Timestamp: snap.StartTime.Add(time.Duration(i) * time.Millisecond), // approximate
			Type:      "message",
			Detail: map[string]any{
				"role": msg.Role,
			},
		})
	}

	// Violations from policy engine
	s.mu.RLock()
	pe := s.policyEngine
	s.mu.RUnlock()
	if pe != nil {
		if flagged := pe.GetFlaggedSession(params.SessionID); flagged != nil {
			for _, v := range flagged.Violations {
				entries = append(entries, timelineEntry{
					Timestamp: v.Timestamp,
					Type:      "violation",
					Detail: map[string]any{
						"rule":     v.RuleName,
						"severity": string(v.Severity),
						"action":   v.Action,
					},
				})
			}
		}
	}

	// Sort by timestamp
	sort.Slice(entries, func(i, j int) bool { return entries[i].Timestamp.Before(entries[j].Timestamp) })

	return map[string]any{
		"session_id": params.SessionID,
		"entries":    entries,
	}, nil
}

func (s *Server) toolGetViolations(args json.RawMessage) (any, *jsonRPCError) {
	var params struct {
		MinSeverity string `json:"min_severity"`
		Limit       int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, &jsonRPCError{Code: errCodeInvalidParams, Message: "invalid arguments"}
	}
	if params.Limit <= 0 {
		params.Limit = 20
	}

	s.mu.RLock()
	pe := s.policyEngine
	s.mu.RUnlock()
	if pe == nil {
		return map[string]any{"violations": []any{}, "total": 0}, nil
	}

	var flagged []*policy.FlaggedSession
	switch params.MinSeverity {
	case "critical":
		flagged = pe.GetFlaggedSessionsBySeverity(policy.SeverityCritical)
	case "warning":
		flagged = pe.GetFlaggedSessionsBySeverity(policy.SeverityWarning)
	default:
		flagged = pe.GetFlaggedSessions()
	}

	type violationSummary struct {
		SessionID   string  `json:"session_id"`
		RiskScore   float64 `json:"risk_score"`
		MaxSeverity string  `json:"max_severity"`
		Violations  int     `json:"violation_count"`
		Action      string  `json:"current_action"`
		LastFlagged string  `json:"last_flagged"`
	}

	results := make([]violationSummary, 0, len(flagged))
	for _, f := range flagged {
		results = append(results, violationSummary{
			SessionID:   f.SessionID,
			RiskScore:   f.RiskScore,
			MaxSeverity: string(f.MaxSeverity),
			Violations:  len(f.Violations),
			Action:      f.CurrentAction,
			LastFlagged: f.LastFlagged.Format(time.RFC3339),
		})
	}

	// Sort by risk (highest first)
	sort.Slice(results, func(i, j int) bool { return results[i].RiskScore > results[j].RiskScore })

	if len(results) > params.Limit {
		results = results[:params.Limit]
	}

	return map[string]any{
		"total":      len(results),
		"violations": results,
	}, nil
}
