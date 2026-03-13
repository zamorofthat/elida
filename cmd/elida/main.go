package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"elida/internal/config"
	"elida/internal/control"
	"elida/internal/policy"
	"elida/internal/proxy"
	"elida/internal/session"
	"elida/internal/storage"
	"elida/internal/telemetry"
	"elida/internal/websocket"
)

// Version is set at build time via -ldflags "-X main.Version=..."
var Version = "dev"

// app holds all shared state for the ELIDA application.
// This replaces forward-declared variables and makes dependencies explicit.
type app struct {
	cfg        *config.Config
	configPath string

	store      session.Store
	redisStore *session.RedisStore
	manager    *session.Manager

	sqliteStore *storage.SQLiteStore

	policyEngine    *policy.Engine
	tp              *telemetry.Provider
	proxyCaptureBuf *proxy.CaptureBuffer

	proxyHandler   *proxy.Proxy
	wsHandler      *websocket.Handler
	settingsStore  *config.SettingsStore
	controlHandler *control.Handler

	proxyServer   *http.Server
	controlServer *http.Server
}

func main() {
	configPath := flag.String("config", "configs/elida.yaml", "path to config file")
	validateOnly := flag.Bool("validate", false, "validate config and exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("elida " + Version)
		return
	}

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ Failed to load config: %s\n  %v\n", *configPath, err)
		os.Exit(1)
	}

	// Validate-only mode
	if *validateOnly {
		result := cfg.Validate()
		printValidationResult(*configPath, result)
		if !result.Valid {
			os.Exit(1)
		}
		os.Exit(0)
	}

	a := &app{cfg: cfg, configPath: *configPath}

	initLogging(cfg)

	slog.Info("starting ELIDA",
		"version", Version,
		"listen", cfg.Listen,
		"backend", cfg.Backend,
		"session_store", cfg.Session.Store,
	)

	a.initSessionStore()
	a.initSQLiteStorage()
	a.initSessionEndCallback()
	a.initTelemetry()
	a.initPolicyEngine()

	// Start session manager (handles timeouts, cleanup)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.manager.Run(ctx)

	a.initProxy()
	a.initWebSocket()
	a.initSettings()
	a.initControlAPI()

	errChan := a.startServers()

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errChan:
		slog.Error("server error", "error", err)
	case sig := <-sigChan:
		slog.Info("received shutdown signal", "signal", sig)
	}

	a.shutdown(cancel)
}

func initLogging(cfg *config.Config) {
	logLevel := slog.LevelInfo
	if cfg.Logging.Level == "debug" {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)
}

func (a *app) initSessionStore() {
	switch a.cfg.Session.Store {
	case "redis":
		var err error
		a.redisStore, err = session.NewRedisStore(session.RedisConfig{
			Addr:      a.cfg.Session.Redis.Addr,
			Password:  a.cfg.Session.Redis.Password,
			DB:        a.cfg.Session.Redis.DB,
			KeyPrefix: a.cfg.Session.Redis.KeyPrefix,
		}, a.cfg.Session.Timeout)
		if err != nil {
			slog.Error("failed to connect to Redis", "error", err)
			os.Exit(1)
		}
		a.store = a.redisStore
		slog.Info("using Redis session store", "addr", a.cfg.Session.Redis.Addr)
	default:
		a.store = session.NewMemoryStore()
		slog.Info("using in-memory session store")
	}

	killBlockConfig := session.KillBlockConfig{
		Mode:     session.KillBlockMode(a.cfg.Session.KillBlock.Mode),
		Duration: a.cfg.Session.KillBlock.Duration,
	}
	if killBlockConfig.Mode == "" {
		killBlockConfig.Mode = session.KillBlockUntilHourChange
	}

	a.manager = session.NewManagerWithKillBlock(a.store, a.cfg.Session.Timeout, killBlockConfig)
	slog.Info("session manager configured", "kill_block_mode", killBlockConfig.Mode, "kill_block_duration", killBlockConfig.Duration)
}

func (a *app) initSQLiteStorage() {
	if !a.cfg.Storage.Enabled {
		return
	}

	dataDir := filepath.Dir(a.cfg.Storage.Path)
	if err := os.MkdirAll(dataDir, 0750); err != nil {
		slog.Error("failed to create data directory", "error", err, "path", dataDir)
		os.Exit(1)
	}

	var err error
	a.sqliteStore, err = storage.NewSQLiteStore(a.cfg.Storage.Path)
	if err != nil {
		slog.Error("failed to initialize SQLite storage", "error", err)
		os.Exit(1)
	}
	slog.Info("SQLite storage enabled", "path", a.cfg.Storage.Path, "retention_days", a.cfg.Storage.RetentionDays)
}

func (a *app) initSessionEndCallback() {
	if !a.cfg.Storage.Enabled && !a.cfg.Telemetry.Enabled {
		return
	}

	a.manager.SetSessionEndCallback(func(sess *session.Session) {
		snap := sess.Snapshot()
		var endTime time.Time
		if snap.EndTime != nil {
			endTime = *snap.EndTime
		} else {
			endTime = time.Now()
		}
		record := storage.SessionRecord{
			ID:           snap.ID,
			State:        snap.State.String(),
			StartTime:    snap.StartTime,
			EndTime:      endTime,
			DurationMs:   endTime.Sub(snap.StartTime).Milliseconds(),
			RequestCount: snap.RequestCount,
			BytesIn:      snap.BytesIn,
			BytesOut:     snap.BytesOut,
			Backend:      snap.Backend,
			ClientAddr:   snap.ClientAddr,
			Metadata:     snap.Metadata,
		}

		a.enrichRecordFromPolicy(&record, snap.ID)
		a.enrichRecordFromCaptureBuffer(&record, snap.ID)
		a.persistToSQLite(&record, sess, endTime)
		a.exportToTelemetry(&record, &snap, endTime)
	})
}

func (a *app) enrichRecordFromPolicy(record *storage.SessionRecord, sessionID string) {
	if a.policyEngine == nil {
		return
	}
	flagged := a.policyEngine.GetFlaggedSession(sessionID)
	if flagged == nil {
		return
	}
	slog.Debug("found flagged session for history", "session_id", sessionID, "captures", len(flagged.CapturedContent), "violations", len(flagged.Violations))
	for _, cap := range flagged.CapturedContent {
		record.CapturedContent = append(record.CapturedContent, storage.CapturedRequest{
			Timestamp:    cap.Timestamp,
			Method:       cap.Method,
			Path:         cap.Path,
			RequestBody:  cap.RequestBody,
			ResponseBody: cap.ResponseBody,
			StatusCode:   cap.StatusCode,
		})
	}
	for _, v := range flagged.Violations {
		record.Violations = append(record.Violations, storage.Violation{
			RuleName:    v.RuleName,
			Description: v.Description,
			Severity:    string(v.Severity),
			MatchedText: v.MatchedText,
			Action:      v.Action,
		})
	}
}

func (a *app) enrichRecordFromCaptureBuffer(record *storage.SessionRecord, sessionID string) {
	if a.proxyCaptureBuf == nil || !a.proxyCaptureBuf.HasContent(sessionID) {
		return
	}
	capturedAll := a.proxyCaptureBuf.GetContent(sessionID)
	if len(record.CapturedContent) > 0 {
		// Policy captures take priority (they include violation context)
		return
	}
	for _, c := range capturedAll {
		record.CapturedContent = append(record.CapturedContent, storage.CapturedRequest{
			Timestamp:    c.Timestamp,
			Method:       c.Method,
			Path:         c.Path,
			RequestBody:  c.RequestBody,
			ResponseBody: c.ResponseBody,
			StatusCode:   c.StatusCode,
		})
	}
}

func (a *app) persistToSQLite(record *storage.SessionRecord, sess *session.Session, endTime time.Time) {
	if a.sqliteStore == nil {
		return
	}
	snap := sess.Snapshot()

	if saveErr := a.sqliteStore.SaveSession(*record); saveErr != nil {
		slog.Error("failed to save session to history", "session_id", snap.ID, "error", saveErr)
	}

	eventCtx := context.Background()

	if eventErr := a.sqliteStore.RecordEvent(eventCtx, storage.EventSessionEnded, snap.ID, "", storage.SessionEndedData{
		State:        snap.State.String(),
		DurationMs:   endTime.Sub(snap.StartTime).Milliseconds(),
		RequestCount: snap.RequestCount,
		BytesIn:      snap.BytesIn,
		BytesOut:     snap.BytesOut,
	}); eventErr != nil {
		slog.Error("failed to record session_ended event", "session_id", snap.ID, "error", eventErr)
	}

	for _, v := range record.Violations {
		if eventErr := a.sqliteStore.RecordEvent(eventCtx, storage.EventViolationDetected, snap.ID, v.Severity, storage.ViolationDetectedData{
			RuleName:    v.RuleName,
			Description: v.Description,
			Severity:    v.Severity,
			MatchedText: v.MatchedText,
			Action:      v.Action,
		}); eventErr != nil {
			slog.Error("failed to record violation event", "session_id", snap.ID, "error", eventErr)
		}
	}

	tokensIn, tokensOut := sess.GetTokens()
	if tokensIn > 0 || tokensOut > 0 {
		if eventErr := a.sqliteStore.RecordEvent(eventCtx, storage.EventTokensUsed, snap.ID, "", storage.TokensUsedData{
			TokensIn:  tokensIn,
			TokensOut: tokensOut,
		}); eventErr != nil {
			slog.Error("failed to record tokens_used event", "session_id", snap.ID, "error", eventErr)
		}
	}

	for toolName, count := range sess.GetToolCallCounts() {
		if eventErr := a.sqliteStore.RecordEvent(eventCtx, storage.EventToolCalled, snap.ID, "", storage.ToolCalledData{
			ToolName:  toolName,
			CallCount: count,
		}); eventErr != nil {
			slog.Error("failed to record tool_called event", "session_id", snap.ID, "error", eventErr)
		}
	}
}

func (a *app) exportToTelemetry(record *storage.SessionRecord, snap *session.Session, endTime time.Time) {
	slog.Debug("checking telemetry export", "tp_nil", a.tp == nil, "tp_enabled", a.tp != nil && a.tp.Enabled())
	if a.tp == nil || !a.tp.Enabled() {
		return
	}

	telemRecord := telemetry.SessionRecord{
		SessionID:    snap.ID,
		State:        snap.State.String(),
		Backend:      snap.Backend,
		ClientAddr:   snap.ClientAddr,
		DurationMs:   endTime.Sub(snap.StartTime).Milliseconds(),
		RequestCount: snap.RequestCount,
		BytesIn:      snap.BytesIn,
		BytesOut:     snap.BytesOut,
		CaptureCount: len(record.CapturedContent),
		TokensIn:     snap.TokensIn,
		TokensOut:    snap.TokensOut,
	}
	for _, v := range record.Violations {
		telemRecord.Violations = append(telemRecord.Violations, telemetry.Violation{
			RuleName:    v.RuleName,
			Description: v.Description,
			Severity:    v.Severity,
			MatchedText: v.MatchedText,
			Action:      v.Action,
		})
	}
	for _, c := range record.CapturedContent {
		telemRecord.Captures = append(telemRecord.Captures, telemetry.CapturedRequest{
			Timestamp:    c.Timestamp.Format(time.RFC3339),
			Method:       c.Method,
			Path:         c.Path,
			RequestBody:  c.RequestBody,
			ResponseBody: c.ResponseBody,
			StatusCode:   c.StatusCode,
		})
	}
	a.tp.ExportSessionRecord(context.Background(), telemRecord)
}

func (a *app) initTelemetry() {
	if !a.cfg.Telemetry.Enabled {
		return
	}
	var err error
	a.tp, err = telemetry.NewProvider(telemetry.Config{
		Enabled:        a.cfg.Telemetry.Enabled,
		Exporter:       a.cfg.Telemetry.Exporter,
		Endpoint:       a.cfg.Telemetry.Endpoint,
		ServiceName:    a.cfg.Telemetry.ServiceName,
		Insecure:       a.cfg.Telemetry.Insecure,
		CaptureContent: a.cfg.Telemetry.CaptureContent,
		MaxBodySize:    a.cfg.Telemetry.MaxBodySize,
	})
	if err != nil {
		slog.Warn("telemetry initialization failed, continuing without tracing", "error", err)
		a.tp = nil
		return
	}
	slog.Info("telemetry enabled",
		"exporter", a.cfg.Telemetry.Exporter,
		"endpoint", a.cfg.Telemetry.Endpoint,
	)
}

func (a *app) initPolicyEngine() {
	if !a.cfg.Policy.Enabled {
		return
	}
	policyRules := make([]policy.Rule, len(a.cfg.Policy.Rules))
	for i, r := range a.cfg.Policy.Rules {
		policyRules[i] = policy.Rule{
			Name:        r.Name,
			Type:        policy.RuleType(r.Type),
			Target:      policy.RuleTarget(r.Target),
			Threshold:   r.Threshold,
			Patterns:    r.Patterns,
			Severity:    policy.Severity(r.Severity),
			Description: r.Description,
			Action:      r.Action,
		}
	}

	a.policyEngine = policy.NewEngine(policy.Config{
		Enabled:        a.cfg.Policy.Enabled,
		Mode:           a.cfg.Policy.Mode,
		CaptureContent: a.cfg.Policy.CaptureContent,
		MaxCaptureSize: a.cfg.Policy.MaxCaptureSize,
		Rules:          policyRules,
	})
	slog.Info("policy engine enabled", "rules", len(policyRules))
}

func (a *app) initProxy() {
	var err error
	a.proxyHandler, err = proxy.NewWithPolicy(a.cfg, a.store, a.manager, a.tp, a.policyEngine)
	if err != nil {
		slog.Error("failed to create proxy", "error", err)
		os.Exit(1)
	}

	if a.sqliteStore != nil {
		a.proxyHandler.SetStorage(a.sqliteStore)
	}

	a.proxyCaptureBuf = a.proxyHandler.GetCaptureBuffer()
}

func (a *app) initWebSocket() {
	if !a.cfg.WebSocket.Enabled {
		return
	}

	a.wsHandler = websocket.NewHandler(
		&a.cfg.WebSocket,
		a.cfg.Session.Header,
		a.manager,
		a.proxyHandler.GetRouter(),
	)
	a.proxyHandler.SetWebSocketHandler(a.wsHandler)

	if a.policyEngine != nil {
		a.wsHandler.SetPolicyEngine(a.policyEngine)
		slog.Info("WebSocket policy scanning enabled",
			"scan_text_frames", a.cfg.WebSocket.ScanTextFrames,
		)
	}

	if a.sqliteStore != nil {
		a.wsHandler.SetVoiceSessionCallbacks(
			nil,
			func(wsSession *session.Session, vs *websocket.VoiceSession) {
				snap := vs.Snapshot()
				record := storage.VoiceSessionRecord{
					ID:              snap.ID,
					ParentSessionID: snap.ParentSessionID,
					State:           snap.State.String(),
					StartTime:       snap.StartTime,
					AnswerTime:      snap.AnswerTime,
					EndTime:         snap.EndTime,
					DurationMs:      snap.Duration().Milliseconds(),
					AudioDurationMs: snap.AudioDurationMs,
					TurnCount:       snap.TurnCount,
					Model:           snap.Model,
					Voice:           snap.Voice,
					Language:        snap.Language,
					AudioBytesIn:    snap.AudioBytesIn,
					AudioBytesOut:   snap.AudioBytesOut,
					Metadata:        snap.Metadata,
				}

				if proto, ok := snap.Metadata["protocol"]; ok {
					record.Protocol = proto
				}

				for _, t := range snap.Transcript {
					record.Transcript = append(record.Transcript, storage.TranscriptEntry{
						Timestamp: t.Timestamp,
						Speaker:   t.Speaker,
						Text:      t.Text,
						IsFinal:   t.IsFinal,
						Source:    t.Source,
					})
				}

				if err := a.sqliteStore.SaveVoiceSession(record); err != nil {
					slog.Error("failed to save voice session",
						"voice_session_id", snap.ID,
						"error", err,
					)
				} else {
					slog.Info("voice session CDR saved",
						"voice_session_id", snap.ID,
						"parent_session_id", snap.ParentSessionID,
						"transcript_entries", len(record.Transcript),
					)
				}
			},
		)
		slog.Info("voice session CDR persistence enabled")
	}

	slog.Info("WebSocket proxy enabled",
		"ping_interval", a.cfg.WebSocket.PingInterval,
		"max_message_size", a.cfg.WebSocket.MaxMessageSize,
	)
}

func (a *app) initSettings() {
	configDir := filepath.Dir(a.configPath)
	if err := os.MkdirAll(configDir, 0750); err != nil {
		slog.Warn("failed to create config directory for settings", "error", err, "path", configDir)
		return
	}
	var err error
	a.settingsStore, err = config.NewSettingsStoreFromConfig(a.cfg, configDir)
	if err != nil {
		slog.Warn("failed to initialize settings store", "error", err)
		return
	}
	slog.Info("settings store initialized", "path", filepath.Join(configDir, "settings.yaml"))
}

func (a *app) initControlAPI() {
	a.controlHandler = control.NewWithAuth(
		a.store,
		a.manager,
		a.sqliteStore,
		a.policyEngine,
		a.cfg.Control.Auth.Enabled,
		a.cfg.Control.Auth.APIKey,
	)
	if a.settingsStore != nil {
		a.controlHandler.SetSettingsStore(a.settingsStore)
	}
	if a.wsHandler != nil {
		a.controlHandler.SetWebSocketHandler(a.wsHandler)
	}
	if a.cfg.Storage.Enabled {
		a.controlHandler.SetCaptureMode(a.cfg.Storage.CaptureMode)
	}

	if a.cfg.Control.Auth.Enabled {
		slog.Info("control API authentication enabled")
	} else {
		slog.Warn("control API authentication is DISABLED — all endpoints are unauthenticated. Set control.auth.enabled=true in production.")
	}
}

func (a *app) startServers() chan error {
	a.proxyServer = &http.Server{
		Addr:         a.cfg.Listen,
		Handler:      a.proxyHandler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // Disable for streaming
		IdleTimeout:  120 * time.Second,
	}

	if a.cfg.Control.Enabled {
		a.controlServer = &http.Server{
			Addr:         a.cfg.Control.Listen,
			Handler:      a.controlHandler,
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  60 * time.Second,
		}
	}

	errChan := make(chan error, 2)

	if a.cfg.TLS.Enabled {
		tlsConfig, err := setupTLS(a.cfg.TLS)
		if err != nil {
			slog.Error("failed to setup TLS", "error", err)
			os.Exit(1)
		}
		a.proxyServer.TLSConfig = tlsConfig
		slog.Info("TLS enabled for proxy server")
	}

	go func() {
		if a.cfg.TLS.Enabled {
			slog.Info("proxy server starting (HTTPS)", "addr", a.cfg.Listen)
			if err := a.proxyServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				errChan <- fmt.Errorf("proxy server error: %w", err)
			}
		} else {
			slog.Info("proxy server starting (HTTP)", "addr", a.cfg.Listen)
			if err := a.proxyServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				errChan <- fmt.Errorf("proxy server error: %w", err)
			}
		}
	}()

	if a.controlServer != nil {
		go func() {
			slog.Info("control server starting", "addr", a.cfg.Control.Listen)
			if err := a.controlServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				errChan <- fmt.Errorf("control server error: %w", err)
			}
		}()
	}

	return errChan
}

func (a *app) shutdown(cancel context.CancelFunc) {
	slog.Info("shutting down gracefully", "timeout", a.cfg.ShutdownTimeout)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), a.cfg.ShutdownTimeout)
	defer shutdownCancel()

	// Step 1: Stop accepting new connections, drain in-flight HTTP requests
	if err := a.proxyServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("proxy server shutdown error", "error", err)
	}

	if a.controlServer != nil {
		if err := a.controlServer.Shutdown(shutdownCtx); err != nil {
			slog.Error("control server shutdown error", "error", err)
		}
	}

	// Step 2: Stop the session manager background loop
	cancel()

	// Step 3: Drain all active sessions (invoke session end callback for each)
	drained := a.manager.DrainActiveSessions()
	if drained > 0 {
		slog.Info("drained sessions on shutdown", "count", drained)
	}

	// Step 4: Flush telemetry AFTER draining (drain creates new OTEL spans)
	if a.tp != nil {
		if err := a.tp.Shutdown(shutdownCtx); err != nil {
			slog.Error("telemetry shutdown error", "error", err)
		}
	}

	// Step 5: Close storage backends
	if a.redisStore != nil {
		if err := a.redisStore.Close(); err != nil {
			slog.Error("Redis close error", "error", err)
		}
	}

	if a.sqliteStore != nil {
		if err := a.sqliteStore.Close(); err != nil {
			slog.Error("SQLite close error", "error", err)
		}
	}

	slog.Info("ELIDA stopped")
}

// setupTLS configures TLS for the proxy server
func setupTLS(cfg config.TLSConfig) (*tls.Config, error) {
	var cert tls.Certificate
	var err error

	if cfg.AutoCert {
		// Generate self-signed certificate for development
		cert, err = generateSelfSignedCert()
		if err != nil {
			return nil, fmt.Errorf("generating self-signed cert: %w", err)
		}
		slog.Warn("using auto-generated self-signed certificate (development only)")
	} else if cfg.CertFile != "" && cfg.KeyFile != "" {
		// Load certificate from files
		cert, err = tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("loading TLS certificate: %w", err)
		}
		slog.Info("loaded TLS certificate", "cert", cfg.CertFile, "key", cfg.KeyFile)
	} else {
		return nil, fmt.Errorf("TLS enabled but no certificate configured (set cert_file/key_file or auto_cert)")
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// generateSelfSignedCert creates a self-signed certificate for development
func generateSelfSignedCert() (tls.Certificate, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"ELIDA Development"},
			CommonName:   "localhost",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost", "elida", "*.elida.local"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})

	privBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privBytes})

	return tls.X509KeyPair(certPEM, keyPEM)
}

// printValidationResult prints a human-readable validation result
func printValidationResult(configPath string, result *config.ValidationResult) {
	if result.Valid {
		fmt.Printf("✓ Configuration valid: %s\n", configPath)
		fmt.Printf("  Listen: %s\n", result.Summary.Listen)
		if result.Summary.BackendCount > 0 {
			fmt.Printf("  Backends: %d configured (default: %s)\n", result.Summary.BackendCount, result.Summary.DefaultBackend)
		}
		if result.Summary.PolicyEnabled {
			if result.Summary.PolicyPreset != "" {
				fmt.Printf("  Policy: enabled (preset: %s)\n", result.Summary.PolicyPreset)
			} else {
				fmt.Printf("  Policy: enabled (%d rules)\n", result.Summary.PolicyRules)
			}
		} else {
			fmt.Printf("  Policy: disabled\n")
		}
		if result.Summary.StorageEnabled {
			fmt.Printf("  Storage: enabled (capture_mode: %s)\n", result.Summary.CaptureMode)
		} else {
			fmt.Printf("  Storage: disabled\n")
		}
		if result.Summary.TLSEnabled {
			fmt.Printf("  TLS: enabled\n")
		}
		if result.Summary.WebSocketEnabled {
			fmt.Printf("  WebSocket: enabled\n")
		}
	} else {
		fmt.Fprintf(os.Stderr, "✗ Configuration invalid: %s\n", configPath)
		for _, e := range result.Errors {
			if e.Hint != "" {
				fmt.Fprintf(os.Stderr, "  - %s: %s\n    hint: %s\n", e.Field, e.Message, e.Hint)
			} else {
				fmt.Fprintf(os.Stderr, "  - %s: %s\n", e.Field, e.Message)
			}
		}
	}
}
