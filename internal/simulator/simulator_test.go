package simulator

import (
	"net/http"
	"testing"
	"time"
)

func TestScenarioPresetsExist(t *testing.T) {
	for _, name := range []string{"stable", "high_latency", "partial_failure", "partition", "weighted"} {
		cfg, ok := PresetScenarioConfigs[name]
		if !ok {
			t.Fatalf("missing preset scenario %q", name)
		}
		if cfg.Name == "" {
			t.Fatalf("scenario %q missing name", name)
		}
		if cfg.Strategy == "" {
			t.Fatalf("scenario %q missing strategy", name)
		}
		if len(cfg.Backends) == 0 {
			t.Fatalf("scenario %q has no backends", name)
		}
	}
}

func TestHarnessStartsAndResponds(t *testing.T) {
	cfg := PresetScenarioConfigs["stable"]
	h, err := NewHarness(cfg)
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}
	defer h.Stop()

	backends := h.Backends()
	if len(backends) != len(cfg.Backends) {
		t.Fatalf("expected %d backends, got %d", len(cfg.Backends), len(backends))
	}

	for _, backend := range backends {
		resp, err := http.Get(backend.URL + "/health")
		if err != nil {
			t.Fatalf("fetch health for %s: %v", backend.ID, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("backend %s health endpoint returned %d", backend.ID, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestHarnessCanToggleState(t *testing.T) {
	cfg := PresetScenarioConfigs["stable"]
	h, err := NewHarness(cfg)
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}
	defer h.Stop()

	if err := h.SetHealthy("backend-1", false); err != nil {
		t.Fatalf("mark backend unhealthy: %v", err)
	}
	if err := h.SetLatency("backend-1", 250*time.Millisecond); err != nil {
		t.Fatalf("set latency: %v", err)
	}

	backend, err := h.GetBackend("backend-1")
	if err != nil {
		t.Fatalf("get backend: %v", err)
	}
	if backend.Healthy {
		t.Fatal("backend should be unhealthy after toggle")
	}
	if backend.Latency != 250*time.Millisecond {
		t.Fatal("latency not updated")
	}
}
