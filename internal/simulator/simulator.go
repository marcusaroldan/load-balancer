package simulator

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// ScenarioConfig represents a preset simulator configuration for a demo scenario.
type ScenarioConfig struct {
	Name     string
	Strategy string
	Backends []BackendConfig
}

// BackendConfig defines one simulated upstream backend for a load balancer scenario.
type BackendConfig struct {
	ID          string
	Address     string
	Port        int
	Weight      int
	Latency     time.Duration
	Healthy     bool
	Failing     bool
	FailureRate float64
}

// Backend is a running simulated backend.
type Backend struct {
	ID          string
	URL         string
	Weight      int
	Latency     time.Duration
	Healthy     bool
	Failing     bool
	FailureRate float64
	mu          sync.RWMutex
	server      *http.Server
	listener    net.Listener
}

// Harness manages multiple simulated backend servers and scenario presets.
type Harness struct {
	mu       sync.RWMutex
	name     string
	strategy string
	backends map[string]*Backend
	servers  []*http.Server
}

// PresetScenarioConfigs contains ready-made network and balancing conditions.
var PresetScenarioConfigs = map[string]ScenarioConfig{
	"stable": {
		Name:     "stable",
		Strategy: "round_robin",
		Backends: []BackendConfig{
			{ID: "backend-1", Address: "127.0.0.1", Port: 18081, Weight: 1, Latency: 20 * time.Millisecond, Healthy: true},
			{ID: "backend-2", Address: "127.0.0.1", Port: 18082, Weight: 1, Latency: 20 * time.Millisecond, Healthy: true},
			{ID: "backend-3", Address: "127.0.0.1", Port: 18083, Weight: 1, Latency: 30 * time.Millisecond, Healthy: true},
		},
	},
	"high_latency": {
		Name:     "high_latency",
		Strategy: "least_connections",
		Backends: []BackendConfig{
			{ID: "backend-1", Address: "127.0.0.1", Port: 18091, Weight: 1, Latency: 250 * time.Millisecond, Healthy: true},
			{ID: "backend-2", Address: "127.0.0.1", Port: 18092, Weight: 1, Latency: 400 * time.Millisecond, Healthy: true},
			{ID: "backend-3", Address: "127.0.0.1", Port: 18093, Weight: 1, Latency: 500 * time.Millisecond, Healthy: true},
		},
	},
	"partial_failure": {
		Name:     "partial_failure",
		Strategy: "weighted_round_robin",
		Backends: []BackendConfig{
			{ID: "backend-1", Address: "127.0.0.1", Port: 18101, Weight: 3, Latency: 40 * time.Millisecond, Healthy: true},
			{ID: "backend-2", Address: "127.0.0.1", Port: 18102, Weight: 1, Latency: 50 * time.Millisecond, Healthy: true, FailureRate: 0.4},
			{ID: "backend-3", Address: "127.0.0.1", Port: 18103, Weight: 2, Latency: 80 * time.Millisecond, Healthy: true},
		},
	},
	"partition": {
		Name:     "partition",
		Strategy: "round_robin",
		Backends: []BackendConfig{
			{ID: "backend-1", Address: "127.0.0.1", Port: 18111, Weight: 1, Latency: 20 * time.Millisecond, Healthy: true},
			{ID: "backend-2", Address: "127.0.0.1", Port: 18112, Weight: 1, Latency: 20 * time.Millisecond, Healthy: false},
			{ID: "backend-3", Address: "127.0.0.1", Port: 18113, Weight: 1, Latency: 20 * time.Millisecond, Healthy: true},
		},
	},
	"weighted": {
		Name:     "weighted",
		Strategy: "weighted_round_robin",
		Backends: []BackendConfig{
			{ID: "backend-1", Address: "127.0.0.1", Port: 18121, Weight: 5, Latency: 35 * time.Millisecond, Healthy: true},
			{ID: "backend-2", Address: "127.0.0.1", Port: 18122, Weight: 2, Latency: 40 * time.Millisecond, Healthy: true},
			{ID: "backend-3", Address: "127.0.0.1", Port: 18123, Weight: 1, Latency: 60 * time.Millisecond, Healthy: true},
		},
	},
}

// NewHarness creates a simulator harness for a preset scenario.
func NewHarness(cfg ScenarioConfig) (*Harness, error) {
	if cfg.Name == "" {
		cfg.Name = "custom"
	}
	if cfg.Strategy == "" {
		cfg.Strategy = "round_robin"
	}

	h := &Harness{
		name:     cfg.Name,
		strategy: cfg.Strategy,
		backends: make(map[string]*Backend),
	}

	for _, backendCfg := range cfg.Backends {
		if backendCfg.ID == "" {
			return nil, fmt.Errorf("backend id is required")
		}
		if backendCfg.Address == "" {
			backendCfg.Address = "127.0.0.1"
		}
		if backendCfg.Port == 0 {
			backendCfg.Port = 18080 + len(h.backends)
		}
		if backendCfg.Weight <= 0 {
			backendCfg.Weight = 1
		}
		if !backendCfg.Healthy {
			backendCfg.Failing = true
		}

		backend := &Backend{
			ID:          backendCfg.ID,
			Weight:      backendCfg.Weight,
			Latency:     backendCfg.Latency,
			Healthy:     backendCfg.Healthy,
			Failing:     backendCfg.Failing,
			FailureRate: backendCfg.FailureRate,
		}

		listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", backendCfg.Address, backendCfg.Port))
		if err != nil {
			return nil, fmt.Errorf("listen on %s:%d for backend %s: %w", backendCfg.Address, backendCfg.Port, backendCfg.ID, err)
		}

		backend.listener = listener
		backend.URL = "http://" + listener.Addr().String()
		backend.server = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			backend.mu.RLock()
			latency := backend.Latency
			healthy := backend.Healthy
			failureRate := backend.FailureRate
			failing := backend.Failing
			backend.mu.RUnlock()

			if r.URL.Path == "/health" {
				if !healthy {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("ok"))
				return
			}

			if latency > 0 {
				time.Sleep(latency)
			}

			if failing || failureRate > 0 {
				if failing || failureRate >= 1.0 || (failureRate > 0 && time.Now().UnixNano()%100 < int64(failureRate*100)) {
					w.WriteHeader(http.StatusBadGateway)
					_, _ = w.Write([]byte("simulated failure"))
					return
				}
			}

			if !healthy {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			response := map[string]any{"id": backend.ID, "status": "ok"}
			_ = json.NewEncoder(w).Encode(response)
		})}

		go func() {
			_ = backend.server.Serve(listener)
		}()

		h.backends[backend.ID] = backend
		h.servers = append(h.servers, backend.server)
	}

	return h, nil
}

// Name returns the simulator name.
func (h *Harness) Name() string { return h.name }

// Strategy returns the active balancing strategy for the scenario.
func (h *Harness) Strategy() string { return h.strategy }

// Backends returns the running simulated backends.
func (h *Harness) Backends() []*Backend {
	h.mu.RLock()
	defer h.mu.RUnlock()

	result := make([]*Backend, 0, len(h.backends))
	for _, backend := range h.backends {
		result = append(result, backend)
	}
	return result
}

// GetBackend searches for a backend by ID.
func (h *Harness) GetBackend(id string) (*Backend, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	backend, ok := h.backends[id]
	if !ok {
		return nil, fmt.Errorf("backend %s not found", id)
	}
	return backend, nil
}

// SetHealthy toggles a backend's liveness state.
func (h *Harness) SetHealthy(id string, healthy bool) error {
	backend, err := h.GetBackend(id)
	if err != nil {
		return err
	}

	backend.mu.Lock()
	backend.Healthy = healthy
	backend.Failing = !healthy
	backend.mu.Unlock()
	return nil
}

// SetLatency updates the simulated response latency for a backend.
func (h *Harness) SetLatency(id string, latency time.Duration) error {
	backend, err := h.GetBackend(id)
	if err != nil {
		return err
	}

	backend.mu.Lock()
	backend.Latency = latency
	backend.mu.Unlock()
	return nil
}

// Stop shuts down all running simulated backends.
func (h *Harness) Stop() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, server := range h.servers {
		_ = server.Close()
	}
	for _, backend := range h.backends {
		if backend.listener != nil {
			_ = backend.listener.Close()
		}
	}
	h.servers = nil
}
