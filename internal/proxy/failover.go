package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"elida/internal/session"
)

// FailureType represents the type of backend failure
type FailureType int

const (
	FailureNone FailureType = iota
	FailureTimeout
	FailureConnectionRefused
	FailureConnectionReset
	FailureServerError     // 5xx
	FailureRateLimit       // 429 without retry-after
	FailureStreamInterrupt // Connection closed mid-stream
)

func (f FailureType) String() string {
	switch f {
	case FailureNone:
		return "none"
	case FailureTimeout:
		return "timeout"
	case FailureConnectionRefused:
		return "connection_refused"
	case FailureConnectionReset:
		return "connection_reset"
	case FailureServerError:
		return "server_error"
	case FailureRateLimit:
		return "rate_limit"
	case FailureStreamInterrupt:
		return "stream_interrupt"
	default:
		return "unknown"
	}
}

// DetectFailure determines the type of failure from response/error
func DetectFailure(resp *http.Response, err error) FailureType {
	if err != nil {
		// Check for timeout
		if os.IsTimeout(err) {
			return FailureTimeout
		}

		// Check for context deadline
		if errors.Is(err, context.DeadlineExceeded) {
			return FailureTimeout
		}

		// Check for connection refused
		var netErr *net.OpError
		if errors.As(err, &netErr) {
			if strings.Contains(netErr.Error(), "connection refused") {
				return FailureConnectionRefused
			}
			if strings.Contains(netErr.Error(), "connection reset") {
				return FailureConnectionReset
			}
		}

		// Generic connection error
		errStr := err.Error()
		if strings.Contains(errStr, "connection refused") {
			return FailureConnectionRefused
		}
		if strings.Contains(errStr, "connection reset") {
			return FailureConnectionReset
		}
		if strings.Contains(errStr, "EOF") {
			return FailureStreamInterrupt
		}

		return FailureStreamInterrupt
	}

	if resp == nil {
		return FailureStreamInterrupt
	}

	// Check HTTP status codes
	if resp.StatusCode >= 500 {
		return FailureServerError
	}
	if resp.StatusCode == 429 {
		// Check for retry-after header - if missing, treat as hard failure
		if resp.Header.Get("Retry-After") == "" {
			return FailureRateLimit
		}
	}

	return FailureNone
}

// FailoverConfig holds failover configuration
type FailoverConfig struct {
	Enabled        bool          `yaml:"enabled"`
	MaxRetries     int           `yaml:"max_retries"`
	RetryDelay     time.Duration `yaml:"retry_delay"`
	BackendTimeout time.Duration `yaml:"backend_timeout"`
	PreserveModel  bool          `yaml:"preserve_model"`
	FallbackOrder  []string      `yaml:"fallback_order"`
}

// DefaultFailoverConfig returns sensible defaults
func DefaultFailoverConfig() FailoverConfig {
	return FailoverConfig{
		Enabled:        false,
		MaxRetries:     2,
		RetryDelay:     100 * time.Millisecond,
		BackendTimeout: 30 * time.Second,
		PreserveModel:  true,
	}
}

// FailoverController handles backend failover logic
type FailoverController struct {
	config   FailoverConfig
	backends map[string]*Backend
}

// Backend represents a backend for failover purposes
type Backend struct {
	Name     string
	URL      string
	Type     string // "openai", "anthropic", "ollama"
	Priority int
	Healthy  bool
}

// NewFailoverController creates a new failover controller
func NewFailoverController(config FailoverConfig) *FailoverController {
	return &FailoverController{
		config:   config,
		backends: make(map[string]*Backend),
	}
}

// RegisterBackend adds a backend to the failover pool
func (fc *FailoverController) RegisterBackend(name, url, backendType string, priority int) {
	fc.backends[name] = &Backend{
		Name:     name,
		URL:      url,
		Type:     backendType,
		Priority: priority,
		Healthy:  true,
	}
}

// SelectFallback chooses the next available backend
func (fc *FailoverController) SelectFallback(sess *session.Session, failedBackend string) (*Backend, error) {
	failedBackends := sess.GetFailedBackends()

	// If explicit fallback order is configured, use it
	if len(fc.config.FallbackOrder) > 0 {
		for _, name := range fc.config.FallbackOrder {
			if name == failedBackend {
				continue
			}
			if contains(failedBackends, name) {
				continue
			}
			if backend, ok := fc.backends[name]; ok && backend.Healthy {
				return backend, nil
			}
		}
	}

	// Otherwise, select by priority
	var bestBackend *Backend
	for _, backend := range fc.backends {
		if backend.Name == failedBackend {
			continue
		}
		if contains(failedBackends, backend.Name) {
			continue
		}
		if !backend.Healthy {
			continue
		}
		if bestBackend == nil || backend.Priority < bestBackend.Priority {
			bestBackend = backend
		}
	}

	if bestBackend == nil {
		return nil, errors.New("no available fallback backends")
	}

	return bestBackend, nil
}

// HandleFailover attempts to failover to another backend
func (fc *FailoverController) HandleFailover(
	ctx context.Context,
	sess *session.Session,
	failedBackend string,
	failureType FailureType,
) (*Backend, error) {
	if !fc.config.Enabled {
		return nil, errors.New("failover not enabled")
	}

	// Mark backend as failed for this session
	sess.AddFailedBackend(failedBackend)

	// Log the failover attempt
	slog.Info("failover triggered",
		"session_id", sess.ID,
		"failed_backend", failedBackend,
		"failure_type", failureType.String(),
		"attempt", len(sess.GetFailedBackends()),
	)

	// Check retry limit
	if len(sess.GetFailedBackends()) > fc.config.MaxRetries {
		return nil, fmt.Errorf("max failover retries exceeded (%d)", fc.config.MaxRetries)
	}

	// Select fallback
	fallback, err := fc.SelectFallback(sess, failedBackend)
	if err != nil {
		return nil, err
	}

	// Apply retry delay if configured
	if fc.config.RetryDelay > 0 {
		select {
		case <-time.After(fc.config.RetryDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	slog.Info("failover selected",
		"session_id", sess.ID,
		"from", failedBackend,
		"to", fallback.Name,
		"messages", len(sess.GetMessages()),
	)

	return fallback, nil
}

// MarkBackendUnhealthy marks a backend as unhealthy
func (fc *FailoverController) MarkBackendUnhealthy(name string) {
	if backend, ok := fc.backends[name]; ok {
		backend.Healthy = false
		slog.Warn("backend marked unhealthy", "backend", name)
	}
}

// MarkBackendHealthy marks a backend as healthy
func (fc *FailoverController) MarkBackendHealthy(name string) {
	if backend, ok := fc.backends[name]; ok {
		backend.Healthy = true
		slog.Info("backend marked healthy", "backend", name)
	}
}

// GetBackend returns a backend by name
func (fc *FailoverController) GetBackend(name string) (*Backend, bool) {
	b, ok := fc.backends[name]
	return b, ok
}

// IsEnabled returns whether failover is enabled
func (fc *FailoverController) IsEnabled() bool {
	return fc.config.Enabled
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
