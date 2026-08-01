package proxy

import (
	"net/http/httptest"
	"testing"

	"elida/internal/config"
)

func trustedProxy(t *testing.T, cidrs []string) *Proxy {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Proxy.Auth.Enabled = true
	cfg.Proxy.Auth.APIKey = "sekret"
	cfg.Proxy.Auth.TrustedNetworks = cidrs
	nets, err := parseTrustedNetworks(cidrs)
	if err != nil {
		t.Fatal(err)
	}
	return &Proxy{config: cfg, trustedNets: nets}
}

// Feedback #4b: requests from a trusted CIDR skip the Bearer check entirely —
// this is what keeps un-keyed auxiliary agent calls (compressors, title
// generators) working on a trusted network.
func TestTrustedNetworkSkipsAuth(t *testing.T) {
	p := trustedProxy(t, []string{"172.17.0.0/16", "127.0.0.1/32", "::1/128"})

	tests := []struct {
		remoteAddr string
		trusted    bool
	}{
		{"172.17.0.1:52011", true}, // docker bridge gateway — the case a naive loopback check misses
		{"127.0.0.1:9999", true},
		{"[::1]:8080", true},
		{"192.168.1.50:1234", false}, // LAN client — must still present the key
		{"10.0.0.7:5555", false},
	}
	for _, tt := range tests {
		r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		r.RemoteAddr = tt.remoteAddr
		if got := p.isTrustedClient(r); got != tt.trusted {
			t.Errorf("isTrustedClient(%s) = %v, want %v", tt.remoteAddr, got, tt.trusted)
		}
	}
}

// SECURITY INVARIANT: X-Forwarded-For must never influence the trust
// decision — the direct TCP peer is the only input. A spoofed header from
// an untrusted address must not bypass auth.
func TestXForwardedForCannotSpoofTrust(t *testing.T) {
	p := trustedProxy(t, []string{"127.0.0.1/32"})
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.RemoteAddr = "203.0.113.9:4444" // untrusted direct peer
	r.Header.Set("X-Forwarded-For", "127.0.0.1")
	r.Header.Set("X-Real-IP", "127.0.0.1")
	if p.isTrustedClient(r) {
		t.Fatal("X-Forwarded-For spoofed trust bypass — direct peer must be the only input")
	}
}

// No trusted_networks configured -> nobody is exempt (current behavior).
func TestNoTrustedNetworksMeansNoBypass(t *testing.T) {
	p := trustedProxy(t, nil)
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.RemoteAddr = "127.0.0.1:9999"
	if p.isTrustedClient(r) {
		t.Fatal("empty trusted_networks must not exempt anyone")
	}
}

// Invalid CIDR fails parsing (startup fails closed rather than silently
// skipping an entry).
func TestInvalidCIDRFailsClosed(t *testing.T) {
	if _, err := parseTrustedNetworks([]string{"not-a-cidr"}); err == nil {
		t.Fatal("invalid CIDR must be a startup error")
	}
}
