package mcp

import (
	"encoding/json"
	"log/slog"
)

// AuditLogger records MCP tool invocations.
type AuditLogger struct {
	enabled bool
}

// NewAuditLogger creates an audit logger.
func NewAuditLogger(enabled bool) *AuditLogger {
	return &AuditLogger{enabled: enabled}
}

// Log records a tool invocation.
func (a *AuditLogger) Log(action, tokenName, toolName string, args json.RawMessage, result any) {
	if !a.enabled {
		return
	}
	slog.Info("mcp.audit",
		"action", action,
		"token", tokenName,
		"tool", toolName,
		"args", string(args),
	)
}
