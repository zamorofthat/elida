package config

import (
	"fmt"
	"log/slog"
	"net"
)

// IsLoopback returns true if the address is a loopback address.
// Empty string, "0.0.0.0", and "::" (all interfaces) return false.
func IsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return false
	}
	ip := net.ParseIP(host)
	if ip != nil {
		return ip.IsLoopback()
	}
	return host == "localhost"
}

// proxyAuthWarningNeeded reports whether the inference proxy is listening on
// a non-loopback address without authentication enabled — the same
// loopback-detection logic used for the control-API warning.
func proxyAuthWarningNeeded(listen string, authEnabled bool) bool {
	return !authEnabled && !IsLoopback(listen)
}

// ValidateSecurityConfig checks security-critical configuration at startup.
// Returns an error for hard failures, logs warnings for soft recommendations.
func ValidateSecurityConfig(cfg *Config) error {
	if proxyAuthWarningNeeded(cfg.Listen, cfg.Proxy.Auth.Enabled) {
		slog.Warn("inference proxy running WITHOUT authentication on non-loopback address — set ELIDA_PROXY_API_KEY or proxy.auth, or restrict with proxy.auth.trusted_networks",
			"listen", cfg.Listen,
		)
	}

	if !cfg.Control.Enabled {
		return nil
	}

	loopback := IsLoopback(cfg.Control.Listen)

	// Non-loopback binding requires auth or explicit allow_insecure
	if !loopback {
		hasAuth := cfg.Control.Auth.Enabled && cfg.Control.Auth.APIKey != ""
		if !hasAuth && !cfg.Control.Auth.AllowInsecure {
			return fmt.Errorf(
				"control API bound to non-loopback address %q without authentication — "+
					"set control.auth.api_key or bind to 127.0.0.1, "+
					"or set control.auth.allow_insecure=true to acknowledge the risk",
				cfg.Control.Listen,
			)
		}
		if cfg.Control.Auth.AllowInsecure && !hasAuth {
			slog.Warn("control API running WITHOUT authentication on non-loopback address — this is not recommended for production",
				"listen", cfg.Control.Listen,
			)
		}
		if !cfg.TLS.Enabled {
			slog.Warn("control API on non-loopback address without TLS — consider enabling TLS",
				"listen", cfg.Control.Listen,
			)
		}
	}

	// Warn if storage enabled but redaction disabled
	if cfg.Storage.Enabled && !cfg.Storage.Redaction.Enabled {
		slog.Warn("storage enabled without redaction — captured content may contain sensitive data")
	}

	return nil
}
