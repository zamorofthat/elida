package config

import "testing"

func hasProxyAuthWarning(c *Config) bool {
	for _, w := range c.Validate().Warnings {
		if w.Field == "proxy.auth" {
			return true
		}
	}
	return false
}

// Session identity comes from a header or the request body, so a caller who
// can reach the proxy port can reset its own risk history or join another
// caller's session. With no proxy authentication on a reachable listener that
// is the deployment's whole security posture, and it deserves a startup
// warning, not just a paragraph in the docs.
func TestValidateWarnsWhenReachableProxyHasNoAuth(t *testing.T) {
	for _, listen := range []string{":8080", "0.0.0.0:8080", "10.0.1.5:8080"} {
		c := defaults()
		c.Listen = listen
		c.Proxy.Auth.Enabled = false
		if !hasProxyAuthWarning(c) {
			t.Errorf("listen %q without proxy auth: want a proxy.auth warning", listen)
		}
	}
}

func TestValidateDoesNotWarnWhenProxyIsLoopbackOrAuthenticated(t *testing.T) {
	c := defaults()
	c.Listen = "127.0.0.1:8080"
	c.Proxy.Auth.Enabled = false
	if hasProxyAuthWarning(c) {
		t.Error("a loopback listener needs no proxy auth warning")
	}

	c = defaults()
	c.Listen = ":8080"
	c.Proxy.Auth.Enabled = true
	c.Proxy.Auth.APIKey = "k"
	if hasProxyAuthWarning(c) {
		t.Error("an authenticated proxy needs no proxy auth warning")
	}
}
