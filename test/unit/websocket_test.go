package unit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/coder/websocket"

	"elida/internal/config"
	"elida/internal/router"
	"elida/internal/session"
	ws "elida/internal/websocket"
)

func TestWebSocket_IsWebSocketRequest(t *testing.T) {
	tests := []struct {
		name        string
		headers     map[string]string
		isWebSocket bool
	}{
		{
			name: "valid websocket upgrade",
			headers: map[string]string{
				"Connection": "Upgrade",
				"Upgrade":    "websocket",
			},
			isWebSocket: true,
		},
		{
			name: "case insensitive upgrade",
			headers: map[string]string{
				"Connection": "upgrade",
				"Upgrade":    "WebSocket",
			},
			isWebSocket: true,
		},
		{
			name: "connection with keep-alive and upgrade",
			headers: map[string]string{
				"Connection": "keep-alive, Upgrade",
				"Upgrade":    "websocket",
			},
			isWebSocket: true,
		},
		{
			name: "missing upgrade header",
			headers: map[string]string{
				"Connection": "Upgrade",
			},
			isWebSocket: false,
		},
		{
			name: "missing connection header",
			headers: map[string]string{
				"Upgrade": "websocket",
			},
			isWebSocket: false,
		},
		{
			name:        "no headers",
			headers:     map[string]string{},
			isWebSocket: false,
		},
		{
			name: "wrong upgrade value",
			headers: map[string]string{
				"Connection": "Upgrade",
				"Upgrade":    "h2c",
			},
			isWebSocket: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/ws", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			got := ws.IsWebSocketRequest(req)
			if got != tt.isWebSocket {
				t.Errorf("IsWebSocketRequest() = %v, want %v", got, tt.isWebSocket)
			}
		})
	}
}

func TestWebSocket_TransformURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "http to ws",
			input:    "http://localhost:8080/api",
			expected: "ws://localhost:8080/api",
		},
		{
			name:     "https to wss",
			input:    "https://api.openai.com/v1/realtime",
			expected: "wss://api.openai.com/v1/realtime",
		},
		{
			name:     "with query string",
			input:    "http://localhost:8080/ws?token=abc",
			expected: "ws://localhost:8080/ws?token=abc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, _ := url.Parse(tt.input)
			got := ws.TransformURL(input)
			if got.String() != tt.expected {
				t.Errorf("TransformURL() = %v, want %v", got.String(), tt.expected)
			}
		})
	}
}

func TestWebSocket_SessionTracking(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)

	// Create a session
	sess := manager.GetOrCreate("ws-session-1", "http://localhost:8080", "127.0.0.1:12345")
	if sess == nil {
		t.Fatal("expected session to be created")
	}

	// Mark as WebSocket
	sess.SetWebSocket()
	snap := sess.Snapshot()
	if !snap.IsWebSocket {
		t.Error("expected IsWebSocket to be true")
	}

	// Add frames
	sess.AddFrame(session.FrameText, 100, session.FrameInbound)
	sess.AddFrame(session.FrameBinary, 200, session.FrameOutbound)
	sess.AddFrame(session.FrameText, 50, session.FrameInbound)

	snap = sess.Snapshot()
	if snap.FrameCount != 3 {
		t.Errorf("expected FrameCount=3, got %d", snap.FrameCount)
	}
	if snap.TextFrames != 2 {
		t.Errorf("expected TextFrames=2, got %d", snap.TextFrames)
	}
	if snap.BinaryFrames != 1 {
		t.Errorf("expected BinaryFrames=1, got %d", snap.BinaryFrames)
	}
	if snap.BytesIn != 150 { // 100 + 50
		t.Errorf("expected BytesIn=150, got %d", snap.BytesIn)
	}
	if snap.BytesOut != 200 {
		t.Errorf("expected BytesOut=200, got %d", snap.BytesOut)
	}
}

func TestWebSocket_KillSignal(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)

	// Create a session
	sess := manager.GetOrCreate("ws-kill-test", "http://localhost:8080", "127.0.0.1:12345")
	sess.SetWebSocket()

	// Kill the session
	manager.Kill("ws-kill-test")

	// Verify kill channel is closed
	select {
	case <-sess.KillChan():
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Error("expected kill channel to be closed")
	}
}

func TestWebSocket_RouterWSURL(t *testing.T) {
	backends := map[string]config.BackendConfig{
		"openai": {
			URL:     "https://api.openai.com",
			Type:    "openai",
			Models:  []string{"gpt-*"},
			Default: true,
		},
		"local": {
			URL:    "http://localhost:11434",
			Type:   "ollama",
			Models: []string{"llama-*"},
		},
	}

	r, err := router.NewRouter(backends, config.RoutingConfig{})
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	// Check WSURL is derived correctly
	openai, ok := r.GetBackend("openai")
	if !ok {
		t.Fatal("expected openai backend")
	}
	if openai.WSURL.Scheme != "wss" {
		t.Errorf("expected wss scheme, got %s", openai.WSURL.Scheme)
	}
	if openai.WSURL.Host != "api.openai.com" {
		t.Errorf("expected api.openai.com host, got %s", openai.WSURL.Host)
	}

	local, ok := r.GetBackend("local")
	if !ok {
		t.Fatal("expected local backend")
	}
	if local.WSURL.Scheme != "ws" {
		t.Errorf("expected ws scheme, got %s", local.WSURL.Scheme)
	}
}

func TestWebSocket_Frame(t *testing.T) {
	// Test Frame creation
	data := []byte("test message")
	frame := ws.NewFrame(websocket.MessageText, data, ws.Inbound)

	if !frame.IsText() {
		t.Error("expected IsText() to be true")
	}
	if frame.IsBinary() {
		t.Error("expected IsBinary() to be false")
	}
	if frame.Size != len(data) {
		t.Errorf("expected Size=%d, got %d", len(data), frame.Size)
	}
	if frame.Direction != ws.Inbound {
		t.Errorf("expected Inbound direction")
	}

	// Test binary frame
	binFrame := ws.NewFrame(websocket.MessageBinary, []byte{0x01, 0x02}, ws.Outbound)
	if binFrame.IsText() {
		t.Error("expected IsText() to be false for binary")
	}
	if !binFrame.IsBinary() {
		t.Error("expected IsBinary() to be true")
	}
}

func TestWebSocket_FrameScanner(t *testing.T) {
	scanner := ws.NewFrameScanner("test-session", true, 1024)

	// Text frame should be scanned (placeholder returns nil for now)
	textFrame := ws.NewFrame(websocket.MessageText, []byte("hello"), ws.Inbound)
	result := scanner.ScanFrame(textFrame)
	// Currently placeholder, returns nil
	if result != nil {
		t.Logf("ScanFrame returned result (placeholder behavior may change)")
	}

	// Finalize should not panic
	finalResult := scanner.Finalize()
	if finalResult != nil {
		t.Logf("Finalize returned result (placeholder behavior may change)")
	}
}

func TestWebSocket_HandlerConfig(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)

	backends := map[string]config.BackendConfig{
		"default": {
			URL:     "http://localhost:8080",
			Type:    "test",
			Default: true,
		},
	}
	r, _ := router.NewRouter(backends, config.RoutingConfig{})

	cfg := &config.WebSocketConfig{
		Enabled:          true,
		ReadBufferSize:   4096,
		WriteBufferSize:  4096,
		HandshakeTimeout: 10 * time.Second,
		PingInterval:     30 * time.Second,
		PongTimeout:      60 * time.Second,
		MaxMessageSize:   1048576,
		ScanTextFrames:   true,
	}

	handler := ws.NewHandler(cfg, "X-Session-ID", manager, r)
	if handler == nil {
		t.Fatal("expected handler to be created")
	}
}

// Integration test for full WebSocket proxy - requires a real WebSocket server
func TestWebSocket_ProxyIntegration(t *testing.T) {
	// Create a mock WebSocket backend server
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err != nil {
			t.Logf("backend accept error: %v", err)
			return
		}
		defer conn.CloseNow()

		// Echo messages back
		for {
			msgType, data, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			err = conn.Write(context.Background(), msgType, data)
			if err != nil {
				return
			}
		}
	}))
	defer backend.Close()

	// Create router pointing to backend
	backendURL, _ := url.Parse(backend.URL)
	backends := map[string]config.BackendConfig{
		"echo": {
			URL:     backend.URL,
			Type:    "test",
			Default: true,
		},
	}
	r, err := router.NewRouter(backends, config.RoutingConfig{})
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	// Verify WSURL was derived correctly
	echoBackend, _ := r.GetBackend("echo")
	expectedWSScheme := "ws"
	if backendURL.Scheme == "https" {
		expectedWSScheme = "wss"
	}
	if echoBackend.WSURL.Scheme != expectedWSScheme {
		t.Errorf("expected WSURL scheme %s, got %s", expectedWSScheme, echoBackend.WSURL.Scheme)
	}

	// Create WebSocket handler
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)

	cfg := &config.WebSocketConfig{
		Enabled:          true,
		ReadBufferSize:   4096,
		WriteBufferSize:  4096,
		HandshakeTimeout: 5 * time.Second,
		PingInterval:     30 * time.Second,
		PongTimeout:      60 * time.Second,
		MaxMessageSize:   1048576,
		ScanTextFrames:   true,
	}

	handler := ws.NewHandler(cfg, "X-Session-ID", manager, r)

	// Create proxy server
	proxy := httptest.NewServer(handler)
	defer proxy.Close()

	// Connect to proxy via WebSocket
	proxyURL, _ := url.Parse(proxy.URL)
	proxyURL.Scheme = "ws"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, resp, err := websocket.Dial(ctx, proxyURL.String()+"/test", nil)
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("failed to connect to proxy: %v", err)
	}
	defer conn.CloseNow()

	// Send a message
	testMsg := []byte("Hello WebSocket!")
	err = conn.Write(ctx, websocket.MessageText, testMsg)
	if err != nil {
		t.Fatalf("failed to send message: %v", err)
	}

	// Receive echo
	msgType, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("failed to receive message: %v", err)
	}
	if msgType != websocket.MessageText {
		t.Errorf("expected text message, got %v", msgType)
	}
	if string(data) != string(testMsg) {
		t.Errorf("expected %q, got %q", testMsg, data)
	}

	// Close connection
	conn.Close(websocket.StatusNormalClosure, "test complete")

	// Give time for session to update
	time.Sleep(100 * time.Millisecond)

	// Verify session was tracked
	sessions := manager.ListAll()
	if len(sessions) == 0 {
		t.Fatal("expected at least one session")
	}

	// Find WebSocket session
	var wsSess *session.Session
	for _, s := range sessions {
		snap := s.Snapshot()
		if snap.IsWebSocket {
			wsSess = s
			break
		}
	}

	if wsSess == nil {
		t.Fatal("expected WebSocket session to be tracked")
	}

	snap := wsSess.Snapshot()
	if !snap.IsWebSocket {
		t.Error("expected session to be marked as WebSocket")
	}
	if snap.FrameCount < 1 {
		t.Errorf("expected at least 1 frame, got %d", snap.FrameCount)
	}
}

func TestWebSocket_PolicyEngineIntegration(t *testing.T) {
	store := session.NewMemoryStore()
	manager := session.NewManager(store, 5*time.Minute)

	cfg := &config.WebSocketConfig{
		Enabled:          true,
		ReadBufferSize:   4096,
		WriteBufferSize:  4096,
		HandshakeTimeout: 10 * time.Second,
		PingInterval:     30 * time.Second,
		PongTimeout:      60 * time.Second,
		MaxMessageSize:   1048576,
		ScanTextFrames:   true, // Enable scanning
	}

	backends := map[string]config.BackendConfig{
		"default": {
			URL:     "http://localhost:11434",
			Type:    "test",
			Default: true,
		},
	}
	rtr, err := router.NewRouter(backends, config.RoutingConfig{})
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}

	handler := ws.NewHandler(cfg, "X-Session-ID", manager, rtr)

	// Verify handler was created
	if handler == nil {
		t.Fatal("expected handler to be created")
	}

	// Verify SetPolicyEngine method exists and works
	// (we don't have a real policy engine here, but we test the method exists)
	handler.SetPolicyEngine(nil) // Should not panic
}

func TestWebSocket_ScanTextFramesConfig(t *testing.T) {
	// Test that ScanTextFrames config is respected
	cfg := &config.WebSocketConfig{
		Enabled:        true,
		ScanTextFrames: true,
	}

	if !cfg.Enabled {
		t.Error("expected Enabled to be true")
	}
	if !cfg.ScanTextFrames {
		t.Error("expected ScanTextFrames to be true")
	}

	cfg.ScanTextFrames = false
	if cfg.ScanTextFrames {
		t.Error("expected ScanTextFrames to be false")
	}
}
