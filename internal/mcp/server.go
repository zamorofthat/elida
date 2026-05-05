package mcp

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"elida/internal/config"
	"elida/internal/policy"
	"elida/internal/session"
	"elida/internal/storage"
)

const (
	mcpProtocolVersion = "2024-11-05"
	serverName         = "elida"
	serverVersion      = "0.1.0"
)

// Server handles MCP JSON-RPC 2.0 requests.
type Server struct {
	cfg      config.MCPConfig
	auth     *Auth
	audit    *AuditLogger
	approval *ApprovalQueue

	manager      *session.Manager
	store        session.Store
	policyEngine *policy.Engine
	historyStore *storage.SQLiteStore
	settings     *config.SettingsStore

	mu    sync.RWMutex
	tools []ToolDef
}

// ToolDef describes an MCP tool for tools/list.
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Scope       string         `json:"-"` // required scope (not sent to client)
}

// jsonRPCRequest is a JSON-RPC 2.0 request.
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      any             `json:"id"`
}

// jsonRPCResponse is a JSON-RPC 2.0 response.
type jsonRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	Result  any           `json:"result,omitempty"`
	Error   *jsonRPCError `json:"error,omitempty"`
	ID      any           `json:"id"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Standard JSON-RPC error codes
const (
	errCodeParse          = -32700
	errCodeInvalidRequest = -32600
	errCodeMethodNotFound = -32601
	errCodeInvalidParams  = -32602
	errCodeInternal       = -32603

	// Custom error codes
	errCodeUnauthorized       = -32001
	errCodeForbidden          = -32003
	errCodeNotFound           = -32004
	errCodeRateLimited        = -32005
	errCodeAntiSelfKill       = -32006
	errCodePendingApproval    = -32007
)

// New creates a new MCP server.
func New(cfg config.MCPConfig, store session.Store, manager *session.Manager) *Server {
	s := &Server{
		cfg:     cfg,
		auth:    NewAuth(cfg),
		audit:   NewAuditLogger(cfg.Audit),
		manager: manager,
		store:   store,
	}
	if cfg.Approval.Enabled {
		s.approval = NewApprovalQueue()
	}
	s.tools = s.registerTools()
	return s
}

// SetPolicyEngine wires the policy engine into the MCP server.
func (s *Server) SetPolicyEngine(engine *policy.Engine) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policyEngine = engine
}

// SetHistoryStore wires the SQLite store.
func (s *Server) SetHistoryStore(store *storage.SQLiteStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.historyStore = store
}

// SetSettingsStore wires the settings store.
func (s *Server) SetSettingsStore(store *config.SettingsStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings = store
}

// GetApprovalQueue returns the approval queue for external approval endpoints.
func (s *Server) GetApprovalQueue() *ApprovalQueue {
	return s.approval
}

// ServeHTTP handles MCP requests on POST /mcp.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONRPC(w, nil, &jsonRPCError{Code: errCodeInvalidRequest, Message: "POST required"})
		return
	}

	var req jsonRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONRPC(w, nil, &jsonRPCError{Code: errCodeParse, Message: "parse error"})
		return
	}

	if req.JSONRPC != "2.0" {
		writeJSONRPC(w, req.ID, &jsonRPCError{Code: errCodeInvalidRequest, Message: "jsonrpc must be \"2.0\""})
		return
	}

	// Initialize doesn't require auth
	if req.Method == "initialize" {
		s.handleInitialize(w, &req)
		return
	}

	// Authenticate
	token, err := s.auth.Authenticate(r)
	if err != nil {
		writeJSONRPC(w, req.ID, &jsonRPCError{Code: errCodeUnauthorized, Message: err.Error()})
		return
	}

	// Rate limit
	if !s.auth.CheckRateLimit(token.Name) {
		writeJSONRPC(w, req.ID, &jsonRPCError{Code: errCodeRateLimited, Message: "rate limit exceeded"})
		return
	}

	switch req.Method {
	case "tools/list":
		s.handleToolsList(w, &req, token)
	case "tools/call":
		s.handleToolsCall(w, r, &req, token)
	default:
		writeJSONRPC(w, req.ID, &jsonRPCError{Code: errCodeMethodNotFound, Message: fmt.Sprintf("unknown method %q", req.Method)})
	}
}

func (s *Server) handleInitialize(w http.ResponseWriter, req *jsonRPCRequest) {
	result := map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    serverName,
			"version": serverVersion,
		},
	}
	writeJSONRPCResult(w, req.ID, result)
}

func (s *Server) handleToolsList(w http.ResponseWriter, req *jsonRPCRequest, token *TokenInfo) {
	var visible []map[string]any
	for _, t := range s.tools {
		if s.auth.HasScope(token.Scope, t.Scope) {
			visible = append(visible, map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"inputSchema": t.InputSchema,
			})
		}
	}
	writeJSONRPCResult(w, req.ID, map[string]any{"tools": visible})
}

func (s *Server) handleToolsCall(w http.ResponseWriter, r *http.Request, req *jsonRPCRequest, token *TokenInfo) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeJSONRPC(w, req.ID, &jsonRPCError{Code: errCodeInvalidParams, Message: "invalid params"})
		return
	}

	// Find tool
	var tool *ToolDef
	for i := range s.tools {
		if s.tools[i].Name == params.Name {
			tool = &s.tools[i]
			break
		}
	}
	if tool == nil {
		writeJSONRPC(w, req.ID, &jsonRPCError{Code: errCodeMethodNotFound, Message: fmt.Sprintf("unknown tool %q", params.Name)})
		return
	}

	// Check scope
	if !s.auth.HasScope(token.Scope, tool.Scope) {
		s.audit.Log("denied", token.Name, params.Name, params.Arguments, "insufficient permissions")
		writeJSONRPC(w, req.ID, &jsonRPCError{Code: errCodeForbidden, Message: "insufficient permissions"})
		return
	}

	// Execute tool
	result, rpcErr := s.executeTool(r, params.Name, params.Arguments, token)

	s.audit.Log("call", token.Name, params.Name, params.Arguments, result)

	if rpcErr != nil {
		writeJSONRPC(w, req.ID, rpcErr)
		return
	}

	// Format output
	content := s.formatResult(params.Name, result, params.Arguments)
	writeJSONRPCResult(w, req.ID, map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": content},
		},
	})
}

func (s *Server) executeTool(r *http.Request, name string, args json.RawMessage, token *TokenInfo) (any, *jsonRPCError) {
	switch name {
	// Read tools
	case "elida_get_stats":
		return s.toolGetStats()
	case "elida_list_sessions":
		return s.toolListSessions(args)
	case "elida_get_session":
		return s.toolGetSession(args)
	case "elida_get_outliers":
		return s.toolGetOutliers(args)
	case "elida_get_timeline":
		return s.toolGetTimeline(args)
	case "elida_get_violations":
		return s.toolGetViolations(args)

	// Write tools
	case "elida_kill_session":
		return s.toolKillSession(r, args, token)
	case "elida_resume_session":
		return s.toolResumeSession(args, token)

	// Admin tools
	case "elida_terminate_session":
		return s.toolTerminateSession(r, args, token)
	case "elida_update_settings":
		return s.toolUpdateSettings(args, token)
	case "elida_check_approval":
		return s.toolCheckApproval(args)

	default:
		return nil, &jsonRPCError{Code: errCodeMethodNotFound, Message: fmt.Sprintf("unknown tool %q", name)}
	}
}

func (s *Server) formatResult(toolName string, result any, args json.RawMessage) string {
	// Check if JSON format was requested in args
	var argMap map[string]any
	if err := json.Unmarshal(args, &argMap); err == nil {
		if fmt, ok := argMap["format"].(string); ok && fmt == "json" {
			b, _ := json.MarshalIndent(result, "", "  ")
			return string(b)
		}
	}

	if s.cfg.Format == "json" {
		b, _ := json.MarshalIndent(result, "", "  ")
		return string(b)
	}

	return FormatASCII(toolName, result)
}

func writeJSONRPC(w http.ResponseWriter, id any, rpcErr *jsonRPCError) {
	w.Header().Set("Content-Type", "application/json")
	if id == nil {
		id = 0
	}
	resp := jsonRPCResponse{JSONRPC: "2.0", ID: id, Error: rpcErr}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Error("mcp: failed to write response", "error", err)
	}
}

func writeJSONRPCResult(w http.ResponseWriter, id any, result any) {
	w.Header().Set("Content-Type", "application/json")
	resp := jsonRPCResponse{JSONRPC: "2.0", ID: id, Result: result}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Error("mcp: failed to write response", "error", err)
	}
}

func (s *Server) registerTools() []ToolDef {
	return []ToolDef{
		// Read tools
		{
			Name: "elida_get_stats", Description: "Get session statistics (active, completed, killed counts)",
			Scope:       "read",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		},
		{
			Name: "elida_list_sessions", Description: "List sessions with optional state filter, limit, and sort",
			Scope: "read",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"state":   map[string]any{"type": "string", "enum": []string{"active", "completed", "killed", "timeout"}, "description": "Filter by state"},
					"limit":   map[string]any{"type": "integer", "description": "Max sessions to return (default 20)"},
					"sort_by": map[string]any{"type": "string", "enum": []string{"risk", "requests", "bytes_out", "start_time"}, "description": "Sort field"},
					"format":  map[string]any{"type": "string", "enum": []string{"ascii", "json"}, "description": "Output format override"},
				},
				"additionalProperties": false,
			},
		},
		{
			Name: "elida_get_session", Description: "Get full detail for a single session",
			Scope: "read",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"session_id"},
				"properties": map[string]any{
					"session_id": map[string]any{"type": "string"},
					"format":     map[string]any{"type": "string", "enum": []string{"ascii", "json"}},
				},
				"additionalProperties": false,
			},
		},
		{
			Name: "elida_get_outliers", Description: "Get top N sessions by risk score, request count, or token usage",
			Scope: "read",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"top_n":  map[string]any{"type": "integer", "description": "Number of results (default 5)"},
					"metric": map[string]any{"type": "string", "enum": []string{"risk", "requests", "tokens", "bytes_out"}, "description": "Sort metric"},
					"format": map[string]any{"type": "string", "enum": []string{"ascii", "json"}},
				},
				"additionalProperties": false,
			},
		},
		{
			Name: "elida_get_timeline", Description: "Get turn-by-turn timeline for a session (messages, tool calls, violations)",
			Scope: "read",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"session_id"},
				"properties": map[string]any{
					"session_id": map[string]any{"type": "string"},
					"format":     map[string]any{"type": "string", "enum": []string{"ascii", "json"}},
				},
				"additionalProperties": false,
			},
		},
		{
			Name: "elida_get_violations", Description: "Get policy violations filtered by severity",
			Scope: "read",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"min_severity": map[string]any{"type": "string", "enum": []string{"info", "warning", "critical"}},
					"limit":        map[string]any{"type": "integer", "description": "Max results (default 20)"},
					"format":       map[string]any{"type": "string", "enum": []string{"ascii", "json"}},
				},
				"additionalProperties": false,
			},
		},

		// Write tools
		{
			Name: "elida_kill_session", Description: "Kill (pause) a session. Session can be resumed later.",
			Scope: "write",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"session_id"},
				"properties": map[string]any{
					"session_id": map[string]any{"type": "string"},
					"reason":     map[string]any{"type": "string", "description": "Reason for killing"},
				},
				"additionalProperties": false,
			},
		},
		{
			Name: "elida_resume_session", Description: "Resume a previously killed session",
			Scope: "write",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"session_id"},
				"properties": map[string]any{
					"session_id": map[string]any{"type": "string"},
				},
				"additionalProperties": false,
			},
		},

		// Admin tools
		{
			Name: "elida_terminate_session", Description: "Permanently terminate a session. Cannot be resumed.",
			Scope: "admin",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"session_id"},
				"properties": map[string]any{
					"session_id": map[string]any{"type": "string"},
					"reason":     map[string]any{"type": "string"},
				},
				"additionalProperties": false,
			},
		},
		{
			Name: "elida_update_settings", Description: "Update a runtime setting by path",
			Scope: "admin",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"path", "value"},
				"properties": map[string]any{
					"path":  map[string]any{"type": "string", "description": "Dot-separated setting path, e.g. policy.mode"},
					"value": map[string]any{"description": "New value"},
				},
				"additionalProperties": false,
			},
		},
		{
			Name: "elida_check_approval", Description: "Check status of a pending approval",
			Scope: "read",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []string{"approval_id"},
				"properties": map[string]any{
					"approval_id": map[string]any{"type": "string"},
				},
				"additionalProperties": false,
			},
		},
	}
}
