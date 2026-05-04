package mcp

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"elida/internal/config"
)

// TokenInfo holds resolved token metadata.
type TokenInfo struct {
	Name  string
	Scope string
}

// Auth handles MCP token authentication and rate limiting.
type Auth struct {
	tokens   []config.MCPTokenConfig
	rateRPM  int
	limiters map[string]*rateBucket
	mu       sync.Mutex
}

type rateBucket struct {
	count    int
	windowAt time.Time
}

// NewAuth creates an Auth from MCPConfig.
func NewAuth(cfg config.MCPConfig) *Auth {
	return &Auth{
		tokens:   cfg.Auth.Tokens,
		rateRPM:  cfg.RateLimit.RequestsPerMinute,
		limiters: make(map[string]*rateBucket),
	}
}

// Authenticate validates the Bearer token and returns the matching token info.
func (a *Auth) Authenticate(r *http.Request) (*TokenInfo, error) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return nil, fmt.Errorf("authorization required")
	}

	bearer := strings.TrimPrefix(authHeader, "Bearer ")
	if bearer == authHeader {
		return nil, fmt.Errorf("Bearer token required")
	}

	for _, t := range a.tokens {
		if subtle.ConstantTimeCompare([]byte(bearer), []byte(t.Key)) == 1 {
			return &TokenInfo{Name: t.Name, Scope: t.Scope}, nil
		}
	}

	return nil, fmt.Errorf("invalid token")
}

// HasScope checks whether tokenScope meets the requiredScope.
// Scope hierarchy: admin > write > read.
func (a *Auth) HasScope(tokenScope, requiredScope string) bool {
	return scopeLevel(tokenScope) >= scopeLevel(requiredScope)
}

func scopeLevel(scope string) int {
	switch scope {
	case "admin":
		return 3
	case "write":
		return 2
	case "read":
		return 1
	default:
		return 0
	}
}

// CheckRateLimit returns true if the request is within rate limits.
func (a *Auth) CheckRateLimit(tokenName string) bool {
	if a.rateRPM <= 0 {
		return true
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	now := time.Now()
	bucket, ok := a.limiters[tokenName]
	if !ok || now.Sub(bucket.windowAt) >= time.Minute {
		a.limiters[tokenName] = &rateBucket{count: 1, windowAt: now}
		return true
	}

	bucket.count++
	return bucket.count <= a.rateRPM
}
