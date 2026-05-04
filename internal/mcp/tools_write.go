package mcp

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
)

// checkAntiSelfKill returns an error if the caller is trying to kill their own session.
func (s *Server) checkAntiSelfKill(r *http.Request, targetSessionID string) *jsonRPCError {
	if !s.cfg.AntiSelfKill {
		return nil
	}

	// Check explicit session ID header
	callerSession := r.Header.Get("X-Elida-Session-ID")
	if callerSession != "" && callerSession == targetSessionID {
		return &jsonRPCError{Code: errCodeAntiSelfKill, Message: "a session cannot kill itself"}
	}

	// Check client IP correlation
	callerIP := extractIP(r.RemoteAddr)
	if callerIP == "" {
		return nil
	}

	sess, ok := s.manager.Get(targetSessionID)
	if !ok {
		return nil // session not found, will be caught later
	}
	snap := sess.Snapshot()
	targetIP := extractIP(snap.ClientAddr)

	if callerIP != "" && targetIP != "" && callerIP == targetIP {
		// Same IP — check if there's a declared session header mismatch
		if callerSession != "" && callerSession != targetSessionID {
			return nil // different declared sessions from same IP is fine
		}
		if callerSession == "" {
			// No declared session, same IP — could be self-kill
			return &jsonRPCError{Code: errCodeAntiSelfKill, Message: "possible self-kill detected (same client IP, declare X-Elida-Session-ID to override)"}
		}
	}

	return nil
}

func extractIP(addr string) string {
	if addr == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return strings.TrimSpace(addr)
	}
	return host
}

func (s *Server) requiresApproval(action string) bool {
	if s.approval == nil || !s.cfg.Approval.Enabled {
		return false
	}
	for _, a := range s.cfg.Approval.RequireFor {
		if a == action {
			return true
		}
	}
	return false
}

func (s *Server) toolKillSession(r *http.Request, args json.RawMessage, token *TokenInfo) (any, *jsonRPCError) {
	var params struct {
		SessionID string `json:"session_id"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal(args, &params); err != nil || params.SessionID == "" {
		return nil, &jsonRPCError{Code: errCodeInvalidParams, Message: "session_id required"}
	}

	if err := s.checkAntiSelfKill(r, params.SessionID); err != nil {
		return nil, err
	}

	if s.requiresApproval("kill") {
		id := s.approval.Submit("kill", params.SessionID, params.Reason, token.Name)
		return map[string]any{"status": "pending_approval", "approval_id": id}, nil
	}

	slog.Info("mcp: kill session", "session_id", params.SessionID, "token", token.Name, "reason", params.Reason)
	if s.manager.Kill(params.SessionID) {
		return map[string]any{"status": "killed", "session_id": params.SessionID}, nil
	}
	return nil, &jsonRPCError{Code: errCodeNotFound, Message: "session not found or already terminated"}
}

func (s *Server) toolResumeSession(args json.RawMessage, token *TokenInfo) (any, *jsonRPCError) {
	var params struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil || params.SessionID == "" {
		return nil, &jsonRPCError{Code: errCodeInvalidParams, Message: "session_id required"}
	}

	slog.Info("mcp: resume session", "session_id", params.SessionID, "token", token.Name)
	if s.manager.Resume(params.SessionID) {
		return map[string]any{"status": "resumed", "session_id": params.SessionID}, nil
	}

	// Check if terminated
	if sess, ok := s.manager.Get(params.SessionID); ok && sess.IsTerminated() {
		return nil, &jsonRPCError{Code: errCodeForbidden, Message: "session is terminated and cannot be resumed"}
	}
	return nil, &jsonRPCError{Code: errCodeNotFound, Message: "session not found or not in killed state"}
}

func (s *Server) toolTerminateSession(r *http.Request, args json.RawMessage, token *TokenInfo) (any, *jsonRPCError) {
	var params struct {
		SessionID string `json:"session_id"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal(args, &params); err != nil || params.SessionID == "" {
		return nil, &jsonRPCError{Code: errCodeInvalidParams, Message: "session_id required"}
	}

	if err := s.checkAntiSelfKill(r, params.SessionID); err != nil {
		return nil, err
	}

	if s.requiresApproval("terminate") {
		id := s.approval.Submit("terminate", params.SessionID, params.Reason, token.Name)
		return map[string]any{"status": "pending_approval", "approval_id": id}, nil
	}

	slog.Warn("mcp: terminate session", "session_id", params.SessionID, "token", token.Name, "reason", params.Reason)
	if s.manager.Terminate(params.SessionID) {
		return map[string]any{"status": "terminated", "session_id": params.SessionID}, nil
	}
	return nil, &jsonRPCError{Code: errCodeNotFound, Message: "session not found or already terminated"}
}

func (s *Server) toolUpdateSettings(args json.RawMessage, token *TokenInfo) (any, *jsonRPCError) {
	var params struct {
		Path  string `json:"path"`
		Value any    `json:"value"`
	}
	if err := json.Unmarshal(args, &params); err != nil || params.Path == "" {
		return nil, &jsonRPCError{Code: errCodeInvalidParams, Message: "path and value required"}
	}

	s.mu.RLock()
	settings := s.settings
	s.mu.RUnlock()
	if settings == nil {
		return nil, &jsonRPCError{Code: errCodeInternal, Message: "settings store not available"}
	}

	slog.Warn("mcp: update settings", "path", params.Path, "value", params.Value, "token", token.Name)

	return map[string]any{
		"status":  "updated",
		"path":    params.Path,
		"message": "setting update acknowledged (apply via control API)",
	}, nil
}

func (s *Server) toolCheckApproval(args json.RawMessage) (any, *jsonRPCError) {
	var params struct {
		ApprovalID string `json:"approval_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil || params.ApprovalID == "" {
		return nil, &jsonRPCError{Code: errCodeInvalidParams, Message: "approval_id required"}
	}

	if s.approval == nil {
		return nil, &jsonRPCError{Code: errCodeInternal, Message: "approval system not enabled"}
	}

	req := s.approval.Get(params.ApprovalID)
	if req == nil {
		return nil, &jsonRPCError{Code: errCodeNotFound, Message: "approval request not found"}
	}

	result := map[string]any{
		"approval_id": req.ID,
		"status":      string(req.Status),
		"action":      req.Action,
		"session_id":  req.SessionID,
	}

	// If approved, execute the action
	if req.Status == ApprovalApproved {
		switch req.Action {
		case "kill":
			s.manager.Kill(req.SessionID)
			result["executed"] = true
		case "terminate":
			s.manager.Terminate(req.SessionID)
			result["executed"] = true
		}
	}

	return result, nil
}
