package telemetry

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"elida/internal/config"
)

// buildTLSConfig creates a *tls.Config from OCSFTLSConfig.
func buildTLSConfig(cfg config.OCSFTLSConfig) (*tls.Config, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if cfg.CAFile != "" {
		caCert, err := os.ReadFile(cfg.CAFile) // #nosec G304 -- trusted config path
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("CA file contains no valid certificates")
		}
		tlsCfg.RootCAs = pool
	}

	if cfg.CertFile != "" && cfg.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load client cert/key: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	if cfg.InsecureSkipVerify {
		tlsCfg.InsecureSkipVerify = true // #nosec G402 -- explicit opt-in
	}

	return tlsCfg, nil
}

// OCSFNozzle is a transport that delivers serialized OCSF events.
type OCSFNozzle interface {
	Emit(ctx context.Context, event []byte) error
	Close() error
}

// OCSFEmitter fans out OCSF events to all enabled nozzles.
type OCSFEmitter struct {
	nozzles []OCSFNozzle
}

// NewOCSFEmitter creates an emitter from config, initializing enabled nozzles.
func NewOCSFEmitter(cfg config.OCSFConfig) (*OCSFEmitter, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	var nozzles []OCSFNozzle

	if cfg.Stdout.Enabled {
		nozzles = append(nozzles, newStdoutNozzle())
		slog.Info("OCSF stdout nozzle enabled")
	}

	if cfg.Webhook.Enabled {
		n, err := newWebhookNozzle(cfg.Webhook)
		if err != nil {
			return nil, fmt.Errorf("webhook nozzle: %w", err)
		}
		nozzles = append(nozzles, n)
		slog.Info("OCSF webhook nozzle enabled", "url", cfg.Webhook.URL)
	}

	if cfg.Syslog.Enabled {
		n, err := newSyslogNozzle(cfg.Syslog)
		if err != nil {
			return nil, fmt.Errorf("syslog nozzle: %w", err)
		}
		nozzles = append(nozzles, n)
		slog.Info("OCSF syslog nozzle enabled", "addr", cfg.Syslog.Addr, "protocol", cfg.Syslog.Protocol)
	}

	if len(nozzles) == 0 {
		slog.Warn("OCSF enabled but no nozzles configured")
		return nil, nil
	}

	return &OCSFEmitter{nozzles: nozzles}, nil
}

// Emit marshals the event once and fans out to all nozzles.
// Errors are logged but never block the caller.
func (e *OCSFEmitter) Emit(ctx context.Context, classUID int, severityID int, event any) {
	data, err := MarshalOCSFEvent(event)
	if err != nil {
		slog.Warn("OCSF marshal failed", "class_uid", classUID, "error", err)
		return
	}

	for _, n := range e.nozzles {
		if err := n.Emit(ctx, data); err != nil {
			slog.Warn("OCSF nozzle emit failed", "error", err)
		}
	}
}

// Close shuts down all nozzles.
func (e *OCSFEmitter) Close() error {
	var firstErr error
	for _, n := range e.nozzles {
		if err := n.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// BuildTLSConfigForTest exposes buildTLSConfig for black-box tests.
func BuildTLSConfigForTest(cfg config.OCSFTLSConfig) (any, error) {
	return buildTLSConfig(cfg)
}

// NewOCSFEmitterForTest creates an emitter with pre-built nozzles (for testing).
func NewOCSFEmitterForTest(nozzles []OCSFNozzle) *OCSFEmitter {
	return &OCSFEmitter{nozzles: nozzles}
}

// Nozzles returns the emitter's nozzles (for testing).
func (e *OCSFEmitter) Nozzles() []OCSFNozzle {
	return e.nozzles
}

// --- stdout nozzle ---

type stdoutNozzle struct {
	mu sync.Mutex
	w  io.Writer
}

func newStdoutNozzle() *stdoutNozzle {
	return &stdoutNozzle{w: os.Stdout}
}

func (n *stdoutNozzle) Emit(_ context.Context, event []byte) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	_, err := fmt.Fprintln(n.w, string(event))
	return err
}

func (n *stdoutNozzle) Close() error { return nil }

// --- webhook nozzle ---

type webhookNozzle struct {
	url        string
	headers    map[string]string
	client     *http.Client
	retryCount int
}

func newWebhookNozzle(cfg config.OCSFWebhookConfig) (*webhookNozzle, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("webhook URL is required")
	}

	// Validate URL scheme
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid webhook URL: %w", err)
	}
	if u.Scheme == "http" && !cfg.TLS.InsecureSkipVerify {
		return nil, fmt.Errorf("plain HTTP webhook URL rejected; use https:// or set insecure_skip_verify")
	}
	if u.Scheme == "http" {
		slog.Warn("OCSF webhook using plain HTTP (insecure_skip_verify is set)", "url", cfg.URL)
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	retries := cfg.RetryCount
	if retries < 0 {
		retries = 0
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	tlsCfg, err := buildTLSConfig(cfg.TLS)
	if err != nil {
		return nil, fmt.Errorf("webhook TLS config: %w", err)
	}
	transport.TLSClientConfig = tlsCfg

	return &webhookNozzle{
		url:     cfg.URL,
		headers: cfg.Headers,
		client: &http.Client{
			Timeout:   timeout,
			Transport: transport,
		},
		retryCount: retries,
	}, nil
}

func (n *webhookNozzle) Emit(ctx context.Context, event []byte) error {
	var lastErr error
	attempts := 1 + n.retryCount
	for i := 0; i < attempts; i++ {
		if err := n.doPost(ctx, event); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return lastErr
}

func (n *webhookNozzle) doPost(ctx context.Context, event []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, strings.NewReader(string(event)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range n.headers {
		req.Header.Set(k, v)
	}

	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck

	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return nil
}

func (n *webhookNozzle) Close() error {
	n.client.CloseIdleConnections()
	return nil
}

// --- syslog nozzle ---

// syslog facility codes
var syslogFacilities = map[string]int{
	"kern":     0,
	"user":     1,
	"mail":     2,
	"daemon":   3,
	"auth":     4,
	"syslog":   5,
	"lpr":      6,
	"news":     7,
	"uucp":     8,
	"cron":     9,
	"authpriv": 10,
	"ftp":      11,
	"local0":   16,
	"local1":   17,
	"local2":   18,
	"local3":   19,
	"local4":   20,
	"local5":   21,
	"local6":   22,
	"local7":   23,
}

type syslogNozzle struct {
	mu        sync.Mutex
	addr      string
	protocol  string
	facility  int
	tag       string
	conn      net.Conn
	tlsConfig *tls.Config
}

func newSyslogNozzle(cfg config.OCSFSyslogConfig) (*syslogNozzle, error) {
	if cfg.Addr == "" {
		return nil, fmt.Errorf("syslog addr is required")
	}
	protocol := cfg.Protocol
	if protocol == "" {
		protocol = "udp"
	}
	facility := 16 // local0
	if f, ok := syslogFacilities[cfg.Facility]; ok {
		facility = f
	}
	tag := cfg.Tag
	if tag == "" {
		tag = "elida"
	}

	var tlsCfg *tls.Config
	if protocol == "tcp+tls" {
		var err error
		tlsCfg, err = buildTLSConfig(cfg.TLS)
		if err != nil {
			return nil, fmt.Errorf("syslog TLS config: %w", err)
		}
	}

	n := &syslogNozzle{
		addr:      cfg.Addr,
		protocol:  protocol,
		facility:  facility,
		tag:       tag,
		tlsConfig: tlsCfg,
	}

	if err := n.connect(); err != nil {
		return nil, fmt.Errorf("syslog connect: %w", err)
	}
	return n, nil
}

func (n *syslogNozzle) connect() error {
	netProto := n.protocol
	switch n.protocol {
	case "tcp+tls":
		conn, err := tls.Dial("tcp", n.addr, n.tlsConfig)
		if err != nil {
			return err
		}
		n.conn = conn
		return nil
	case "tcp", "udp":
		netProto = n.protocol
	default:
		netProto = "udp"
	}
	conn, err := net.DialTimeout(netProto, n.addr, 5*time.Second)
	if err != nil {
		return err
	}
	n.conn = conn
	return nil
}

func (n *syslogNozzle) Emit(_ context.Context, event []byte) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	// RFC 5424: <priority>1 timestamp hostname app-name procid msgid structured-data msg
	// priority = facility * 8 + severity (6 = informational)
	priority := n.facility*8 + 6
	timestamp := time.Now().Format(time.RFC3339)
	hostname, _ := os.Hostname()
	msg := fmt.Sprintf("<%d>1 %s %s %s - - - %s", priority, timestamp, hostname, n.tag, string(event))

	_, err := fmt.Fprintln(n.conn, msg)
	if err != nil {
		// Try to reconnect once
		if reconnErr := n.connect(); reconnErr != nil {
			return fmt.Errorf("syslog send failed and reconnect failed: %w", err)
		}
		_, err = fmt.Fprintln(n.conn, msg)
	}
	return err
}

func (n *syslogNozzle) Close() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.conn != nil {
		return n.conn.Close()
	}
	return nil
}
