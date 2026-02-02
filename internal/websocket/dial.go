package websocket

import (
	"context"
	"net/http"
	"net/url"

	"github.com/coder/websocket"

	"elida/internal/config"
	"elida/internal/router"
)

// HeadersToForward lists headers that should be forwarded to the backend
var HeadersToForward = []string{
	"Authorization",
	"X-Api-Key",
	"X-Session-ID",
	"X-Request-ID",
	"User-Agent",
	"Accept-Language",
	"Cookie",
	// OpenAI-specific headers
	"OpenAI-Beta",
	"OpenAI-Organization",
	// Anthropic-specific headers
	"X-API-Key",
	"Anthropic-Version",
	"Anthropic-Beta",
}

// DialBackend establishes a WebSocket connection to the backend
func DialBackend(ctx context.Context, backend *router.Backend, origReq *http.Request, cfg *config.WebSocketConfig) (*websocket.Conn, error) {
	// Build the backend WebSocket URL with the original path and query
	backendURL := buildBackendURL(backend.WSURL, origReq.URL)

	// Build dial options
	dialOpts := &websocket.DialOptions{
		HTTPHeader: copyHeaders(origReq.Header),
	}

	// Set handshake timeout if configured
	if cfg.HandshakeTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.HandshakeTimeout)
		defer cancel()
	}

	// Dial the backend
	conn, resp, err := websocket.Dial(ctx, backendURL.String(), dialOpts)
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	return conn, err
}

// buildBackendURL constructs the full WebSocket URL for the backend
// using the backend's base URL and the original request's path/query
func buildBackendURL(baseURL *url.URL, origURL *url.URL) *url.URL {
	result := *baseURL // Copy base URL

	// Use original request path
	result.Path = origURL.Path
	result.RawPath = origURL.RawPath
	result.RawQuery = origURL.RawQuery
	result.Fragment = origURL.Fragment

	return &result
}

// copyHeaders creates a new header map with forwarded headers
func copyHeaders(src http.Header) http.Header {
	dst := make(http.Header)

	for _, header := range HeadersToForward {
		if values := src.Values(header); len(values) > 0 {
			for _, v := range values {
				dst.Add(header, v)
			}
		}
	}

	return dst
}

// TransformURL converts an HTTP URL to a WebSocket URL
// http:// -> ws://, https:// -> wss://
func TransformURL(httpURL *url.URL) *url.URL {
	wsURL := *httpURL // Copy
	switch wsURL.Scheme {
	case "http":
		wsURL.Scheme = "ws"
	case "https":
		wsURL.Scheme = "wss"
	}
	return &wsURL
}
