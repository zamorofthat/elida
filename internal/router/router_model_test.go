package router

import (
	"testing"

	"elida/internal/config"
)

func TestRouterBackendCarriesModel(t *testing.T) {
	r, err := NewRouter(map[string]config.BackendConfig{
		"nemo": {URL: "http://127.0.0.1:1", Type: "openai", Model: "nvidia/llama-3.3-nemotron-super-49b-v1", Default: true},
	}, config.RoutingConfig{Methods: []string{"default"}})
	if err != nil {
		t.Fatal(err)
	}
	b, ok := r.GetBackend("nemo")
	if !ok || b.Model != "nvidia/llama-3.3-nemotron-super-49b-v1" {
		t.Fatalf("router backend lost the model field: %+v", b)
	}
}
