package simulator

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"slices"
	"strings"
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
	Pool        string
}

// Backend is a running simulated backend.
type Backend struct {
	ID                string
	Name              string
	URL               string
	Weight            int
	Latency           time.Duration
	Healthy           bool
	Failing           bool
	FailureRate       float64
	Pool              string
	ActiveConnections int
	mu                sync.RWMutex
	server            *http.Server
	listener          net.Listener
}

// SimulationState is the live, environment-only view of the scenario visible to the frontend.
//
// The simulator presents the health and load profile of the backends and the current scenario
// configuration, but it intentionally does not expose any internal load-balancer routing state,
// selected target, request destination, or routing decisions. Those belong to the real LB.
type SimulationState struct {
	Name        string         `json:"name"`
	Strategy    string         `json:"strategy"`
	Clients     int            `json:"clients"`
	RequestRate int            `json:"request_rate"`
	Pools       []string       `json:"pools"`
	Backends    []BackendState `json:"backends"`
}

// BackendState is a serialized subset of backend runtime state for the UI.
// It is gathered from the backend listeners themselves, not from the load balancer.
type BackendState struct {
	ID                string        `json:"id"`
	Name              string        `json:"name"`
	URL               string        `json:"url"`
	Weight            int           `json:"weight"`
	Latency           time.Duration `json:"latency"`
	Healthy           bool          `json:"healthy"`
	Failing           bool          `json:"failing"`
	FailureRate       float64       `json:"failure_rate"`
	Pool              string        `json:"pool"`
	ActiveConnections int           `json:"active_connections"`
	LoadPercent       int           `json:"load_percent"`
}

// Harness manages multiple simulated backend servers and scenario presets.
type Harness struct {
	mu          sync.RWMutex
	name        string
	strategy    string
	clients     int
	requestRate int
	pools       map[string]struct{}
	backends    map[string]*Backend
	servers     []*http.Server
	stop        chan struct{}
}

// PresetScenarioConfigs contains ready-made network and balancing conditions.
var PresetScenarioConfigs = map[string]ScenarioConfig{
	"stable": {
		Name:     "stable",
		Strategy: "round_robin",
		Backends: []BackendConfig{
			{ID: "backend-1", Address: "127.0.0.1", Port: 18081, Weight: 1, Latency: 20 * time.Millisecond, Healthy: true, Pool: "default"},
			{ID: "backend-2", Address: "127.0.0.1", Port: 18082, Weight: 1, Latency: 20 * time.Millisecond, Healthy: true, Pool: "default"},
			{ID: "backend-3", Address: "127.0.0.1", Port: 18083, Weight: 1, Latency: 30 * time.Millisecond, Healthy: true, Pool: "default"},
		},
	},
	"high_latency": {
		Name:     "high_latency",
		Strategy: "least_connections",
		Backends: []BackendConfig{
			{ID: "backend-1", Address: "127.0.0.1", Port: 18091, Weight: 1, Latency: 250 * time.Millisecond, Healthy: true, Pool: "default"},
			{ID: "backend-2", Address: "127.0.0.1", Port: 18092, Weight: 1, Latency: 400 * time.Millisecond, Healthy: true, Pool: "default"},
			{ID: "backend-3", Address: "127.0.0.1", Port: 18093, Weight: 1, Latency: 500 * time.Millisecond, Healthy: true, Pool: "default"},
		},
	},
	"partial_failure": {
		Name:     "partial_failure",
		Strategy: "weighted_round_robin",
		Backends: []BackendConfig{
			{ID: "backend-1", Address: "127.0.0.1", Port: 18101, Weight: 3, Latency: 40 * time.Millisecond, Healthy: true, Pool: "default"},
			{ID: "backend-2", Address: "127.0.0.1", Port: 18102, Weight: 1, Latency: 50 * time.Millisecond, Healthy: true, FailureRate: 0.4, Pool: "default"},
			{ID: "backend-3", Address: "127.0.0.1", Port: 18103, Weight: 2, Latency: 80 * time.Millisecond, Healthy: true, Pool: "default"},
		},
	},
	"partition": {
		Name:     "partition",
		Strategy: "round_robin",
		Backends: []BackendConfig{
			{ID: "backend-1", Address: "127.0.0.1", Port: 18111, Weight: 1, Latency: 20 * time.Millisecond, Healthy: true, Pool: "default"},
			{ID: "backend-2", Address: "127.0.0.1", Port: 18112, Weight: 1, Latency: 20 * time.Millisecond, Healthy: false, Pool: "default"},
			{ID: "backend-3", Address: "127.0.0.1", Port: 18113, Weight: 1, Latency: 20 * time.Millisecond, Healthy: true, Pool: "default"},
		},
	},
	"weighted": {
		Name:     "weighted",
		Strategy: "weighted_round_robin",
		Backends: []BackendConfig{
			{ID: "backend-1", Address: "127.0.0.1", Port: 18121, Weight: 5, Latency: 35 * time.Millisecond, Healthy: true, Pool: "default"},
			{ID: "backend-2", Address: "127.0.0.1", Port: 18122, Weight: 2, Latency: 40 * time.Millisecond, Healthy: true, Pool: "default"},
			{ID: "backend-3", Address: "127.0.0.1", Port: 18123, Weight: 1, Latency: 60 * time.Millisecond, Healthy: true, Pool: "default"},
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
		name:        cfg.Name,
		strategy:    cfg.Strategy,
		clients:     3,
		requestRate: 30,
		pools:       make(map[string]struct{}),
		backends:    make(map[string]*Backend),
		stop:        make(chan struct{}),
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
		if backendCfg.Pool == "" {
			backendCfg.Pool = "default"
		}
		if !backendCfg.Healthy {
			backendCfg.Failing = true
		}

		backend := &Backend{
			ID:          backendCfg.ID,
			Name:        backendCfg.ID,
			Weight:      backendCfg.Weight,
			Latency:     backendCfg.Latency,
			Healthy:     backendCfg.Healthy,
			Failing:     backendCfg.Failing,
			FailureRate: backendCfg.FailureRate,
			Pool:        backendCfg.Pool,
		}
		h.pools[backendCfg.Pool] = struct{}{}

		listener, err := listenForBackend(backendCfg.Address, backendCfg.Port)
		if err != nil {
			return nil, fmt.Errorf("listen on %s:%d for backend %s: %w", backendCfg.Address, backendCfg.Port, backendCfg.ID, err)
		}

		backend.listener = listener
		backend.URL = "http://" + listener.Addr().String()
		backend.server = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			backend.mu.Lock()
			backend.ActiveConnections++
			latency := backend.Latency
			healthy := backend.Healthy
			failureRate := backend.FailureRate
			failing := backend.Failing
			backend.mu.Unlock()

			defer func() {
				backend.mu.Lock()
				if backend.ActiveConnections > 0 {
					backend.ActiveConnections--
				}
				backend.mu.Unlock()
			}()

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
			if processingDelay := randomProcessingDelay(); processingDelay > 0 {
				time.Sleep(processingDelay)
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
	go h.runTrafficLoop()
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

// State returns the current live simulation state for the frontend.
func (h *Harness) State() SimulationState {
	h.mu.RLock()
	defer h.mu.RUnlock()

	pools := make([]string, 0, len(h.pools))
	for pool := range h.pools {
		pools = append(pools, pool)
	}
	slices.Sort(pools)

	state := SimulationState{
		Name:        h.name,
		Strategy:    h.strategy,
		Clients:     h.clients,
		RequestRate: h.requestRate,
		Pools:       pools,
		Backends:    make([]BackendState, 0, len(h.backends)),
	}

	// The simulator is only a model of the environment. It does not compute
	// load-balancer decisions or route requests; it reports the current backend
	// state observed from the running backend listeners and client configuration.
	backendIDs := make([]string, 0, len(h.backends))
	for id := range h.backends {
		backendIDs = append(backendIDs, id)
	}
	slices.Sort(backendIDs)
	for _, id := range backendIDs {
		backend := h.backends[id]
		backend.mu.RLock()
		activeConnections := max(0, backend.ActiveConnections)
		loadPercent := min(100, max(0, activeConnections*10))
		if !backend.Healthy {
			loadPercent = 0
		}
		state.Backends = append(state.Backends, BackendState{
			ID:                backend.ID,
			Name:              backend.Name,
			URL:               backend.URL,
			Weight:            backend.Weight,
			Latency:           backend.Latency,
			Healthy:           backend.Healthy,
			Failing:           backend.Failing,
			FailureRate:       backend.FailureRate,
			Pool:              backend.Pool,
			ActiveConnections: activeConnections,
			LoadPercent:       loadPercent,
		})
		backend.mu.RUnlock()
	}
	return state
}

// SetStrategy updates the balancing strategy used by the simulated scenario.
//
// This is the only routing-level control the simulator may expose. It models the LB's config,
// not the destination chosen for a given request.
func (h *Harness) SetStrategy(strategy string) error {
	if strategy == "" {
		return fmt.Errorf("strategy is required")
	}

	h.mu.Lock()
	h.strategy = strategy
	h.mu.Unlock()
	return nil
}

// SetRequestRate updates the simulated client request rate.
func (h *Harness) SetRequestRate(rate int) error {
	if rate < 0 {
		return fmt.Errorf("request rate cannot be negative")
	}

	h.mu.Lock()
	h.requestRate = rate
	h.mu.Unlock()
	return nil
}

// AddClient adds a simulated client to the demo.
func (h *Harness) AddClient() error {
	h.mu.Lock()
	h.clients = max(1, h.clients+1)
	h.mu.Unlock()
	return nil
}

// RemoveClient removes a simulated client from the demo.
func (h *Harness) RemoveClient() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients = max(1, h.clients-1)
	return nil
}

// AddPool adds a named backend pool group.
func (h *Harness) AddPool(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("pool name is required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pools[name] = struct{}{}
	return nil
}

// SetBackendPool reassigns a backend to a named pool.
func (h *Harness) SetBackendPool(id, pool string) error {
	backend, err := h.GetBackend(id)
	if err != nil {
		return err
	}
	pool = strings.TrimSpace(pool)
	if pool == "" {
		pool = "default"
	}
	backend.mu.Lock()
	backend.Pool = pool
	backend.mu.Unlock()
	h.mu.Lock()
	h.pools[pool] = struct{}{}
	h.mu.Unlock()
	return nil
}

// SetWeight updates a backend weight.
func (h *Harness) SetWeight(id string, weight int) error {
	backend, err := h.GetBackend(id)
	if err != nil {
		return err
	}
	if weight <= 0 {
		return fmt.Errorf("weight must be greater than 0")
	}
	backend.mu.Lock()
	backend.Weight = weight
	backend.mu.Unlock()
	return nil
}

// AddBackend starts a new simulated backend and adds it to the harness.
func (h *Harness) AddBackend(id, address string, port int, weight int, latency time.Duration, healthy bool) (*Backend, error) {
	if id == "" {
		return nil, fmt.Errorf("backend id is required")
	}
	if address == "" {
		address = "127.0.0.1"
	}
	if weight <= 0 {
		weight = 1
	}

	backend := &Backend{
		ID:      id,
		Name:    id,
		URL:     fmt.Sprintf("http://%s:%d", address, port),
		Weight:  weight,
		Latency: latency,
		Healthy: healthy,
		Failing: !healthy,
		Pool:    "default",
	}

	listener, err := listenForBackend(address, port)
	if err != nil {
		return nil, fmt.Errorf("listen on %s:%d for backend %s: %w", address, port, id, err)
	}

	backend.listener = listener
	backend.URL = "http://" + listener.Addr().String()
	backend.server = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backend.mu.Lock()
		backend.ActiveConnections++
		latency := backend.Latency
		healthy := backend.Healthy
		failureRate := backend.FailureRate
		failing := backend.Failing
		backend.mu.Unlock()

		defer func() {
			backend.mu.Lock()
			if backend.ActiveConnections > 0 {
				backend.ActiveConnections--
			}
			backend.mu.Unlock()
		}()

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
		if processingDelay := randomProcessingDelay(); processingDelay > 0 {
			time.Sleep(processingDelay)
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

	h.mu.Lock()
	h.backends[id] = backend
	h.pools[backend.Pool] = struct{}{}
	h.servers = append(h.servers, backend.server)
	h.mu.Unlock()
	return backend, nil
}

// SetName updates the display name for a backend.
func (h *Harness) SetName(id, name string) error {
	backend, err := h.GetBackend(id)
	if err != nil {
		return err
	}
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		trimmed = backend.ID
	}
	backend.mu.Lock()
	backend.Name = trimmed
	backend.mu.Unlock()
	return nil
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

// SetFailureRate updates the simulated upstream failure probability.
func (h *Harness) SetFailureRate(id string, rate float64) error {
	backend, err := h.GetBackend(id)
	if err != nil {
		return err
	}
	if rate < 0 || rate > 1 {
		return fmt.Errorf("failure rate must be between 0 and 1")
	}

	backend.mu.Lock()
	backend.FailureRate = rate
	backend.mu.Unlock()
	return nil
}

// RemoveBackend removes a backend and closes its listener.
func (h *Harness) RemoveBackend(id string) error {
	backend, err := h.GetBackend(id)
	if err != nil {
		return err
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if backend.server != nil {
		_ = backend.server.Close()
	}
	if backend.listener != nil {
		_ = backend.listener.Close()
	}
	delete(h.backends, id)
	return nil
}

// ResetScenario reinitializes the harness to a preset scenario state.
func (h *Harness) ResetScenario(name string) error {
	cfg, ok := PresetScenarioConfigs[name]
	if !ok {
		return fmt.Errorf("unknown scenario %q", name)
	}

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

	if h.stop != nil {
		close(h.stop)
	}
	h.name = cfg.Name
	h.strategy = cfg.Strategy
	h.clients = 3
	h.requestRate = 30
	h.pools = make(map[string]struct{})
	h.backends = make(map[string]*Backend)
	h.servers = nil
	h.stop = make(chan struct{})

	for _, backendCfg := range cfg.Backends {
		if backendCfg.Pool == "" {
			backendCfg.Pool = "default"
		}
		h.pools[backendCfg.Pool] = struct{}{}

		backend := &Backend{
			ID:          backendCfg.ID,
			Weight:      backendCfg.Weight,
			Latency:     backendCfg.Latency,
			Healthy:     backendCfg.Healthy,
			Failing:     backendCfg.Failing,
			FailureRate: backendCfg.FailureRate,
			Pool:        backendCfg.Pool,
		}

		listener, err := listenForBackend(backendCfg.Address, backendCfg.Port)
		if err != nil {
			return fmt.Errorf("listen on %s:%d for backend %s: %w", backendCfg.Address, backendCfg.Port, backendCfg.ID, err)
		}

		backend.listener = listener
		backend.URL = "http://" + listener.Addr().String()
		backend.server = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			backend.mu.Lock()
			backend.ActiveConnections++
			latency := backend.Latency
			healthy := backend.Healthy
			failureRate := backend.FailureRate
			failing := backend.Failing
			backend.mu.Unlock()

			defer func() {
				backend.mu.Lock()
				if backend.ActiveConnections > 0 {
					backend.ActiveConnections--
				}
				backend.mu.Unlock()
			}()

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
			if processingDelay := randomProcessingDelay(); processingDelay > 0 {
				time.Sleep(processingDelay)
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
	go h.runTrafficLoop()
	return nil
}

// Stop shuts down all running simulated backends.
func (h *Harness) Stop() {
	h.mu.Lock()
	if h.stop != nil {
		close(h.stop)
		h.stop = nil
	}
	for _, server := range h.servers {
		_ = server.Close()
	}
	for _, backend := range h.backends {
		if backend.listener != nil {
			_ = backend.listener.Close()
		}
	}
	h.pools = make(map[string]struct{})
	h.backends = make(map[string]*Backend)
	h.servers = nil
	h.mu.Unlock()
}

func (h *Harness) runTrafficLoop() {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-ticker.C:
			h.mu.RLock()
			if h.requestRate <= 0 || h.clients <= 0 || len(h.backends) == 0 {
				h.mu.RUnlock()
				continue
			}
			backends := make([]*Backend, 0, len(h.backends))
			for _, backend := range h.backends {
				if backend != nil && backend.Healthy {
					backends = append(backends, backend)
				}
			}
			clients := h.clients
			requestRate := h.requestRate
			h.mu.RUnlock()
			if len(backends) == 0 {
				continue
			}
			for i := 0; i < max(1, requestRate/5); i++ {
				backend := backends[i%len(backends)]
				go func(backend *Backend) {
					if backend == nil {
						return
					}
					resp, err := http.Get(backend.URL)
					if err != nil {
						return
					}
					_ = resp.Body.Close()
				}(backend)
			}
			_ = clients
		}
	}
}

func randomProcessingDelay() time.Duration {
	const (
		mean  = 200
		sigma = 75.0
		minMs = 50.0
		maxMs = 350.0
	)
	for {
		value := rand.NormFloat64()*sigma + mean
		if value >= minMs && value <= maxMs {
			return time.Duration(value) * time.Millisecond
		}
	}
}

func listenForBackend(address string, port int) (net.Listener, error) {
	if address == "" {
		address = "127.0.0.1"
	}
	if port > 0 {
		listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", address, port))
		if err == nil {
			return listener, nil
		}
	}
	return net.Listen("tcp", fmt.Sprintf("%s:0", address))
}

// NewHTTPHandler exposes the simulation state via a simple HTTP API for the dashboard.
func NewHTTPHandler(h *Harness) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(h.State())
	})
}
