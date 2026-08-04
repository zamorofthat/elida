package router

import (
	"sort"
	"strings"
	"testing"

	"elida/internal/config"
)

func TestAggregatedModels(t *testing.T) {
	rt, err := NewRouter(map[string]config.BackendConfig{
		"local":    {URL: "http://127.0.0.1:1", Type: "openai", Models: []string{"gemma", "qwen"}, Default: true},
		"nemotron": {URL: "http://127.0.0.1:2", Type: "openai", Models: []string{"nvidia/*"}, Model: "nvidia/llama-3.3-nemotron-super-49b-v1"},
		"mistral":  {URL: "http://127.0.0.1:3", Type: "mistral", Models: []string{"mistral-*"}, Model: "mistral-small-latest"},
	}, config.RoutingConfig{Methods: []string{"model", "default"}})
	if err != nil {
		t.Fatal(err)
	}
	models := rt.AggregatedModels()

	want := map[string]string{ // id -> owned_by
		"gemma":                                  "local",
		"qwen":                                   "local",
		"nvidia/llama-3.3-nemotron-super-49b-v1": "nemotron",
		"mistral-small-latest":                   "mistral",
	}
	if len(models) != len(want) {
		t.Fatalf("got %d models, want %d: %+v", len(models), len(want), models)
	}
	for _, m := range models {
		if want[m.ID] != m.OwnedBy {
			t.Errorf("model %q owned_by %q, want %q", m.ID, m.OwnedBy, want[m.ID])
		}
	}
	// Globs are never emitted as ids.
	for _, m := range models {
		if strings.ContainsAny(m.ID, "*?") {
			t.Errorf("glob leaked as model id: %q", m.ID)
		}
	}
	// Sorted by ID.
	if !sort.SliceIsSorted(models, func(i, j int) bool { return models[i].ID < models[j].ID }) {
		t.Error("models not sorted by ID")
	}
}
