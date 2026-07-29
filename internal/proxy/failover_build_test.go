package proxy

import (
	"testing"

	"elida/internal/config"
	"elida/internal/router"
)

func buildTestRouter(t *testing.T) *router.Router {
	t.Helper()
	rt, err := router.NewRouter(map[string]config.BackendConfig{
		"local":    {URL: "http://127.0.0.1:1", Type: "openai", Default: true},
		"nemotron": {URL: "http://127.0.0.1:2", Type: "openai", Model: "nvidia/x"},
		"mistral":  {URL: "http://127.0.0.1:3", Type: "mistral"},
	}, config.RoutingConfig{Methods: []string{"default"}})
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

func TestBuildFailoverControllerDisabled(t *testing.T) {
	if fc := BuildFailoverController(config.FailoverConfig{Enabled: false}, buildTestRouter(t)); fc != nil {
		t.Fatal("disabled failover must build nil controller")
	}
}

func TestBuildFailoverControllerRegistersAllBackends(t *testing.T) {
	fc := BuildFailoverController(config.FailoverConfig{
		Enabled: true, MaxRetries: 2, FallbackOrder: []string{"local", "nemotron"},
	}, buildTestRouter(t))
	if fc == nil || !fc.IsEnabled() {
		t.Fatal("enabled failover must build an enabled controller")
	}
	for _, name := range []string{"local", "nemotron", "mistral"} {
		if _, ok := fc.GetBackend(name); !ok {
			t.Errorf("backend %q not registered", name)
		}
	}
	// Priority follows fallback_order; unlisted backends go last.
	local, _ := fc.GetBackend("local")
	nemo, _ := fc.GetBackend("nemotron")
	mist, _ := fc.GetBackend("mistral")
	if local.Priority >= nemo.Priority || nemo.Priority >= mist.Priority {
		t.Errorf("priorities wrong: local=%d nemotron=%d mistral=%d", local.Priority, nemo.Priority, mist.Priority)
	}
}
