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
)

func main() {
	configPath := flag.String("config", "configs/elida.yaml", "path to config file")
	flag.Parse()

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// Setup structured logging
	logLevel := slog.LevelInfo
	if cfg.Logging.Level == "debug" {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)

	slog.Info("starting ELIDA",
		"version", "0.1.0",
		"listen", cfg.Listen,
		"backend", cfg.Backend,
		"session_store", cfg.Session.Store,
	)

	// Initialize session store based on configuration
	var store session.Store
	var redisStore *session.RedisStore

	switch cfg.Session.Store {
	case "redis":
		var err error
		redisStore, err = session.NewRedisStore(session.RedisConfig{
			Addr:      cfg.Session.Redis.Addr,
			Password:  cfg.Session.Redis.Password,
			DB:        cfg.Session.Redis.DB,
			KeyPrefix: cfg.Session.Redis.KeyPrefix,
		}, cfg.Session.Timeout)
		if err != nil {
			slog.Error("failed to connect to Redis", "error", err)
			os.Exit(1)
		}
		store = redisStore
		slog.Info("using Redis session store", "addr", cfg.Session.Redis.Addr)
	default:
		store = session.NewMemoryStore()
		slog.Info("using in-memory session store")
	}

	// Configure kill block settings
	killBlockConfig := session.KillBlockConfig{
		Mode:     session.KillBlockMode(cfg.Session.KillBlock.Mode),
		Duration: cfg.Session.KillBlock.Duration,
	}
	if killBlockConfig.Mode == "" {
		killBlockConfig.Mode = session.KillBlockUntilHourChange
	}

	manager := session.NewManagerWithKillBlock(store, cfg.Session.Timeout, killBlockConfig)
	slog.Info("session manager configured", "kill_block_mode", killBlockConfig.Mode, "kill_block_duration", killBlockConfig.Duration)

	// Forward-declare policyEngine so session callback closure can reference it
	var policyEngine *policy.Engine

	// Initialize SQLite storage for session history
	var sqliteStore *storage.SQLiteStore
	if cfg.Storage.Enabled {
		// Ensure data directory exists
		dataDir := filepath.Dir(cfg.Storage.Path)
		if err := os.MkdirAll(dataDir, 0755); err != nil {
			slog.Error("failed to create data directory", "error", err, "path", dataDir)
			os.Exit(1)
		}

		var err error
		sqliteStore, err = storage.NewSQLiteStore(cfg.Storage.Path)
		if err != nil {
			slog.Error("failed to initialize SQLite storage", "error", err)
			os.Exit(1)
		}
		slog.Info("SQLite storage enabled", "path", cfg.Storage.Path, "retention_days", cfg.Storage.RetentionDays)

		// Set up callback to save sessions when they end
		manager.SetSessionEndCallback(func(sess *session.Session) {
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

			// Include captured content and violations from policy engine
			if policyEngine != nil {
				if flagged := policyEngine.GetFlaggedSession(snap.ID); flagged != nil {
					slog.Debug("found flagged session for history", "session_id", snap.ID, "captures", len(flagged.CapturedContent), "violations", len(flagged.Violations))
					// Convert captured requests
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
					// Convert violations
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
			}

			if err := sqliteStore.SaveSession(record); err != nil {
				slog.Error("failed to save session to history", "session_id", snap.ID, "error", err)
			}
		})
	}

	// Initialize telemetry (graceful degradation if initialization fails)
	var tp *telemetry.Provider
	if cfg.Telemetry.Enabled {
		var err error
		tp, err = telemetry.NewProvider(telemetry.Config{
			Enabled:     cfg.Telemetry.Enabled,
			Exporter:    cfg.Telemetry.Exporter,
			Endpoint:    cfg.Telemetry.Endpoint,
			ServiceName: cfg.Telemetry.ServiceName,
			Insecure:    cfg.Telemetry.Insecure,
		})
		if err != nil {
			slog.Warn("telemetry initialization failed, continuing without tracing", "error", err)
			tp = nil // Continue without telemetry
		} else {
			slog.Info("telemetry enabled",
				"exporter", cfg.Telemetry.Exporter,
				"endpoint", cfg.Telemetry.Endpoint,
			)
		}
	}

	// Initialize policy engine
	if cfg.Policy.Enabled {
		// Convert config rules to policy rules
		policyRules := make([]policy.Rule, len(cfg.Policy.Rules))
		for i, r := range cfg.Policy.Rules {
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

		policyEngine = policy.NewEngine(policy.Config{
			Enabled:        cfg.Policy.Enabled,
			Mode:           cfg.Policy.Mode,
			CaptureContent: cfg.Policy.CaptureContent,
			MaxCaptureSize: cfg.Policy.MaxCaptureSize,
			Rules:          policyRules,
		})
		slog.Info("policy engine enabled", "rules", len(policyRules))
	}

	// Start session manager (handles timeouts, cleanup)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go manager.Run(ctx)

	// Initialize proxy
	proxyHandler, err := proxy.NewWithPolicy(cfg, store, manager, tp, policyEngine)
	if err != nil {
		slog.Error("failed to create proxy", "error", err)
		os.Exit(1)
	}

	// Initialize control API
	controlHandler := control.NewWithPolicy(store, manager, sqliteStore, policyEngine)

	// Setup HTTP servers
	proxyServer := &http.Server{
		Addr:         cfg.Listen,
		Handler:      proxyHandler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // Disable for streaming
		IdleTimeout:  120 * time.Second,
	}

	var controlServer *http.Server
	if cfg.Control.Enabled {
		controlServer = &http.Server{
			Addr:         cfg.Control.Listen,
			Handler:      controlHandler,
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  60 * time.Second,
		}
	}

	// Start servers
	errChan := make(chan error, 2)

	// Configure TLS if enabled
	var tlsConfig *tls.Config
	if cfg.TLS.Enabled {
		var err error
		tlsConfig, err = setupTLS(cfg.TLS)
		if err != nil {
			slog.Error("failed to setup TLS", "error", err)
			os.Exit(1)
		}
		proxyServer.TLSConfig = tlsConfig
		slog.Info("TLS enabled for proxy server")
	}

	go func() {
		if cfg.TLS.Enabled {
			slog.Info("proxy server starting (HTTPS)", "addr", cfg.Listen)
			if err := proxyServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				errChan <- fmt.Errorf("proxy server error: %w", err)
			}
		} else {
			slog.Info("proxy server starting (HTTP)", "addr", cfg.Listen)
			if err := proxyServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				errChan <- fmt.Errorf("proxy server error: %w", err)
			}
		}
	}()

	if controlServer != nil {
		go func() {
			slog.Info("control server starting", "addr", cfg.Control.Listen)
			if err := controlServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				errChan <- fmt.Errorf("control server error: %w", err)
			}
		}()
	}

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errChan:
		slog.Error("server error", "error", err)
	case sig := <-sigChan:
		slog.Info("received shutdown signal", "signal", sig)
	}

	// Graceful shutdown
	slog.Info("shutting down servers")
	cancel() // Stop session manager

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := proxyServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("proxy server shutdown error", "error", err)
	}

	if controlServer != nil {
		if err := controlServer.Shutdown(shutdownCtx); err != nil {
			slog.Error("control server shutdown error", "error", err)
		}
	}

	// Close Redis connection if used
	if redisStore != nil {
		if err := redisStore.Close(); err != nil {
			slog.Error("Redis close error", "error", err)
		}
	}

	// Close SQLite storage if used
	if sqliteStore != nil {
		if err := sqliteStore.Close(); err != nil {
			slog.Error("SQLite close error", "error", err)
		}
	}

	// Shutdown telemetry
	if tp != nil {
		if err := tp.Shutdown(shutdownCtx); err != nil {
			slog.Error("telemetry shutdown error", "error", err)
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
