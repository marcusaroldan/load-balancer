package simulator

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultStrategy            = "round_robin"
	defaultCustomScenarioName  = "custom"
	defaultPoolName            = "default"
	defaultClients             = 3
	defaultRequestRate         = 30
	defaultLoopbackAddress     = "127.0.0.1"
	defaultPortOffset          = 18080
	defaultBackendWeight       = 1
	trafficLoopInterval        = 200 * time.Millisecond
	trafficBurstFactor         = 5
	failureRateScale           = 100.0
	simulatorLoadPercentScale  = 10
	simulatorLoadPercentMax    = 100
	processingDelayMeanMS      = 125.0
	processingDelaySigma       = 75.0
	processingDelayMinMS       = 50.0
	processingDelayMaxMS       = 200.0
	stableScenarioBasePort     = 18081
	highLatencyBasePort        = 18091
	partialFailureBasePort     = 18101
	partitionScenarioBasePort  = 18111
	weightedScenarioBasePort   = 18121
	failureCheckThresholdScale = 100
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
	Name         string               `json:"name"`
	Strategy     string               `json:"strategy"`
	Clients      int                  `json:"clients"`
	RequestRate  int                  `json:"request_rate"`
	Pools        []string             `json:"pools"`
	Backends     []BackendState       `json:"backends"`
	LoadBalancer LoadBalancerSnapshot `json:"load_balancer"`
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
	mu           sync.RWMutex
	name         string
	strategy     string
	clients      int
	requestRate  int
	pools        map[string]struct{}
	backends     map[string]*Backend
	servers      []*http.Server
	stop         chan struct{}
	loadBalancer LoadBalancerAdapter
	binding      BackendBinding
	factory      AdapterFactory
}

// BackendBinding constrains where simulated backends listen and which host
// they are advertised under. Simulated backends are the simulator's own
// servers: an operator sets this via process flags once at startup, and the
// public HTTP API can never steer it (bind-address abuse would let a browser
// open listeners on arbitrary interfaces of a shared host).
//
// BindHost: host listeners bind to (default loopback).
// AdvertiseHost: host reported in backend URLs; empty falls back to BindHost.
// Set it to the container's service DNS name when an out-of-process load
// balancer must reach the simulated backends across the compose network.
type BackendBinding struct {
	BindHost      string
	AdvertiseHost string
}

// bindHost returns the effective listener host (never empty).
func (b BackendBinding) bindHost() string {
	if b.BindHost == "" {
		return defaultLoopbackAddress
	}
	return b.BindHost
}

// advertiseHost returns the effective URL host. A wildcard bind is never a
// dialable name, so advertise falls back to loopback in that case.
func (b BackendBinding) advertiseHost() string {
	if b.AdvertiseHost != "" {
		return b.AdvertiseHost
	}
	host := b.bindHost()
	if host == "0.0.0.0" || host == "::" || host == "" || host == "*" {
		return defaultLoopbackAddress
	}
	return host
}

// HarnessOption customizes harness construction; keeps NewHarness backward
// compatible for tests and embedded callers.
type HarnessOption func(*Harness)

// WithAdapterFactory selects a different load-balancer adapter (e.g. the
// remote out-of-process adapter for the VM topology).
func WithAdapterFactory(f AdapterFactory) HarnessOption {
	return func(h *Harness) { h.factory = f }
}

// WithBackendBinding sets the listener bind/advertise policy.
func WithBackendBinding(b BackendBinding) HarnessOption {
	return func(h *Harness) { h.binding = b }
}

// PresetScenarioConfigs contains ready-made network and balancing conditions.
var PresetScenarioConfigs = map[string]ScenarioConfig{
	"stable": {
		Name:     "stable",
		Strategy: defaultStrategy,
		Backends: []BackendConfig{
			{ID: "backend-1", Address: defaultLoopbackAddress, Port: stableScenarioBasePort, Weight: 1, Latency: 20 * time.Millisecond, Healthy: true, Pool: defaultPoolName},
			{ID: "backend-2", Address: defaultLoopbackAddress, Port: stableScenarioBasePort + 1, Weight: 1, Latency: 20 * time.Millisecond, Healthy: true, Pool: defaultPoolName},
			{ID: "backend-3", Address: defaultLoopbackAddress, Port: stableScenarioBasePort + 2, Weight: 1, Latency: 30 * time.Millisecond, Healthy: true, Pool: defaultPoolName},
		},
	},
	"high_latency": {
		Name:     "high_latency",
		Strategy: "least_connections",
		Backends: []BackendConfig{
			{ID: "backend-1", Address: defaultLoopbackAddress, Port: highLatencyBasePort, Weight: 1, Latency: 250 * time.Millisecond, Healthy: true, Pool: defaultPoolName},
			{ID: "backend-2", Address: defaultLoopbackAddress, Port: highLatencyBasePort + 1, Weight: 1, Latency: 400 * time.Millisecond, Healthy: true, Pool: defaultPoolName},
			{ID: "backend-3", Address: defaultLoopbackAddress, Port: highLatencyBasePort + 2, Weight: 1, Latency: 500 * time.Millisecond, Healthy: true, Pool: defaultPoolName},
		},
	},
	"partial_failure": {
		Name:     "partial_failure",
		Strategy: "weighted_round_robin",
		Backends: []BackendConfig{
			{ID: "backend-1", Address: defaultLoopbackAddress, Port: partialFailureBasePort, Weight: 3, Latency: 40 * time.Millisecond, Healthy: true, Pool: defaultPoolName},
			{ID: "backend-2", Address: defaultLoopbackAddress, Port: partialFailureBasePort + 1, Weight: 1, Latency: 50 * time.Millisecond, Healthy: true, FailureRate: 0.4, Pool: defaultPoolName},
			{ID: "backend-3", Address: defaultLoopbackAddress, Port: partialFailureBasePort + 2, Weight: 2, Latency: 80 * time.Millisecond, Healthy: true, Pool: defaultPoolName},
		},
	},
	"partition": {
		Name:     "partition",
		Strategy: defaultStrategy,
		Backends: []BackendConfig{
			{ID: "backend-1", Address: defaultLoopbackAddress, Port: partitionScenarioBasePort, Weight: 1, Latency: 20 * time.Millisecond, Healthy: true, Pool: defaultPoolName},
			{ID: "backend-2", Address: defaultLoopbackAddress, Port: partitionScenarioBasePort + 1, Weight: 1, Latency: 20 * time.Millisecond, Healthy: false, Pool: defaultPoolName},
			{ID: "backend-3", Address: defaultLoopbackAddress, Port: partitionScenarioBasePort + 2, Weight: 1, Latency: 20 * time.Millisecond, Healthy: true, Pool: defaultPoolName},
		},
	},
	"weighted": {
		Name:     "weighted",
		Strategy: "weighted_round_robin",
		Backends: []BackendConfig{
			{ID: "backend-1", Address: defaultLoopbackAddress, Port: weightedScenarioBasePort, Weight: 5, Latency: 35 * time.Millisecond, Healthy: true, Pool: defaultPoolName},
			{ID: "backend-2", Address: defaultLoopbackAddress, Port: weightedScenarioBasePort + 1, Weight: 2, Latency: 40 * time.Millisecond, Healthy: true, Pool: defaultPoolName},
			{ID: "backend-3", Address: defaultLoopbackAddress, Port: weightedScenarioBasePort + 2, Weight: 1, Latency: 60 * time.Millisecond, Healthy: true, Pool: defaultPoolName},
		},
	},
}

// NewHarness creates a simulator harness for a preset scenario.
func NewHarness(cfg ScenarioConfig, opts ...HarnessOption) (*Harness, error) {
	if cfg.Name == "" {
		cfg.Name = defaultCustomScenarioName
	}
	if cfg.Strategy == "" {
		cfg.Strategy = defaultStrategy
	}

	h := &Harness{
		name:        cfg.Name,
		strategy:    cfg.Strategy,
		clients:     defaultClients,
		requestRate: defaultRequestRate,
		pools:       make(map[string]struct{}),
		backends:    make(map[string]*Backend),
		stop:        make(chan struct{}),
		factory:     NewAdapterFactory(),
	}
	for _, opt := range opts {
		opt(h)
	}
	adapter, err := h.factory()
	if err != nil {
		return nil, fmt.Errorf("create load balancer adapter: %w", err)
	}
	h.loadBalancer = adapter

	for _, backendCfg := range cfg.Backends {
		backend, err := newBackend(backendCfg, h.binding)
		if err != nil {
			return nil, err
		}
		h.pools[backend.Pool] = struct{}{}
		h.backends[backend.ID] = backend
		h.servers = append(h.servers, backend.server)
	}
	if err := h.configureLoadBalancer(); err != nil {
		h.Stop()
		return nil, err
	}
	if err := h.loadBalancer.Start(); err != nil {
		h.Stop()
		return nil, err
	}
	go h.runTrafficLoop(h.stop)
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

	// Backend state describes the simulated environment. Aggregate LB outcomes
	// are reported separately so the UI can distinguish conditions from results.
	backendIDs := make([]string, 0, len(h.backends))
	for id := range h.backends {
		backendIDs = append(backendIDs, id)
	}
	slices.Sort(backendIDs)
	for _, id := range backendIDs {
		backend := h.backends[id]
		backend.mu.RLock()
		activeConnections := max(0, backend.ActiveConnections)
		loadPercent := min(simulatorLoadPercentMax, max(0, activeConnections*simulatorLoadPercentScale))
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
	state.LoadBalancer = h.loadBalancer.Snapshot()
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

	if err := h.loadBalancer.SetStrategy(strategy); err != nil {
		return err
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
		pool = defaultPoolName
	}
	backend.mu.Lock()
	previousPool := backend.Pool
	backend.Pool = pool
	backend.mu.Unlock()
	if err := h.loadBalancer.SetBackendPool(id, pool); err != nil {
		backend.mu.Lock()
		backend.Pool = previousPool
		backend.mu.Unlock()
		return err
	}
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
	if err := h.loadBalancer.SetBackendWeight(id, weight); err != nil {
		return err
	}
	return nil
}

// AddBackend starts a new simulated backend and adds it to the harness.
func (h *Harness) AddBackend(id, address string, port int, weight int, latency time.Duration, healthy bool) (*Backend, error) {
	backendCfg := BackendConfig{
		ID:      id,
		Address: address,
		Port:    port,
		Weight:  weight,
		Latency: latency,
		Healthy: healthy,
		Pool:    defaultPoolName,
	}
	backend, err := newBackend(backendCfg, h.binding)
	if err != nil {
		return nil, err
	}

	h.mu.Lock()
	h.backends[id] = backend
	h.pools[backend.Pool] = struct{}{}
	h.servers = append(h.servers, backend.server)
	h.mu.Unlock()
	if err := h.configureLoadBalancer(); err != nil {
		_ = h.RemoveBackend(id)
		return nil, err
	}
	return backend, nil
}

// AddManagedBackend is the public-API entry point for creating simulated
// backends. Unlike AddBackend it accepts no address or port at all: the
// listener binds per the harness policy and the port is kernel-assigned.
// Browser-supplied bind targets must never reach backend creation (SSRF /
// bind-abuse boundary, S1).
func (h *Harness) AddManagedBackend(id, pool string, weight int, latency time.Duration, healthy bool) (*Backend, error) {
	backendCfg := BackendConfig{
		ID:      id,
		Address: h.binding.bindHost(), // advisory only; newBackend binds from policy
		Port:    0,
		Weight:  weight,
		Latency: latency,
		Healthy: healthy,
		Pool:    strings.TrimSpace(pool),
	}
	if backendCfg.Pool == "" {
		backendCfg.Pool = defaultPoolName
	}
	backend, err := newBackend(backendCfg, h.binding)
	if err != nil {
		return nil, err
	}

	h.mu.Lock()
	if _, exists := h.backends[id]; exists {
		h.mu.Unlock()
		_ = backend.server.Close()
		return nil, fmt.Errorf("backend %q already exists", id)
	}
	h.backends[id] = backend
	h.pools[backend.Pool] = struct{}{}
	h.servers = append(h.servers, backend.server)
	h.mu.Unlock()
	if err := h.configureLoadBalancer(); err != nil {
		_ = h.RemoveBackend(id)
		return nil, err
	}
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
	previousHealthy := backend.Healthy
	previousFailing := backend.Failing
	backend.Healthy = healthy
	backend.Failing = !healthy
	backend.mu.Unlock()
	if err := h.loadBalancer.SetBackendHealth(id, healthy); err != nil {
		backend.mu.Lock()
		backend.Healthy = previousHealthy
		backend.Failing = previousFailing
		backend.mu.Unlock()
		return err
	}
	return nil
}

// SetLatency updates the simulated response latency for a backend.
func (h *Harness) SetLatency(id string, latency time.Duration) error {
	backend, err := h.GetBackend(id)
	if err != nil {
		return err
	}

	backend.mu.Lock()
	previousLatency := backend.Latency
	backend.Latency = latency
	backend.mu.Unlock()
	if err := h.loadBalancer.SetBackendLatency(id, latency); err != nil {
		backend.mu.Lock()
		backend.Latency = previousLatency
		backend.mu.Unlock()
		return err
	}
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
	previousRate := backend.FailureRate
	backend.FailureRate = rate
	backend.mu.Unlock()
	if err := h.loadBalancer.SetBackendFailureRate(id, rate); err != nil {
		backend.mu.Lock()
		backend.FailureRate = previousRate
		backend.mu.Unlock()
		return err
	}
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
	if err := h.loadBalancer.RemoveBackend(id); err != nil {
		return err
	}

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
	return h.applyScenario(cfg)
}

// EnterIdleMode scales the whole demo to zero (S2): simulated listeners
// close, synthetic traffic stops, and every backend is deregistered from the
// load balancer (an empty scenario reconciles to no members). It is the
// watchdog's teardown path and is safe to call repeatedly.
func (h *Harness) EnterIdleMode() error {
	h.mu.RLock()
	strategy, alreadyIdle := h.strategy, len(h.backends) == 0 && h.name == idleScenarioName
	h.mu.RUnlock()
	if alreadyIdle {
		return nil
	}
	return h.applyScenario(ScenarioConfig{Name: idleScenarioName, Strategy: strategy})
}

// applyScenario rebuilds the environment from cfg in one shot: close all
// listeners, stop traffic, install the new backends, reconfigure the load
// balancer, restart the loop.
func (h *Harness) applyScenario(cfg ScenarioConfig) error {
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
	h.clients = defaultClients
	h.requestRate = defaultRequestRate
	h.pools = make(map[string]struct{})
	h.backends = make(map[string]*Backend)
	h.servers = nil
	h.stop = make(chan struct{})

	for _, backendCfg := range cfg.Backends {
		backend, err := newBackend(backendCfg, h.binding)
		if err != nil {
			return err
		}
		h.pools[backend.Pool] = struct{}{}
		h.backends[backend.ID] = backend
		h.servers = append(h.servers, backend.server)
	}
	if err := h.configureLoadBalancerLocked(); err != nil {
		return err
	}
	go h.runTrafficLoop(h.stop)
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
	if h.loadBalancer != nil {
		_ = h.loadBalancer.Stop()
	}
	h.pools = make(map[string]struct{})
	h.backends = make(map[string]*Backend)
	h.servers = nil
	h.mu.Unlock()
}

func (h *Harness) runTrafficLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(trafficLoopInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			h.mu.RLock()
			if h.requestRate <= 0 || h.clients <= 0 || len(h.backends) == 0 {
				h.mu.RUnlock()
				continue
			}
			requestRate := h.requestRate
			endpoint := h.loadBalancer.Endpoint()
			h.mu.RUnlock()
			if endpoint == "" {
				continue
			}
			for i := 0; i < max(1, requestRate/trafficBurstFactor); i++ {
				go func() {
					resp, err := http.Get(endpoint)
					if err != nil {
						return
					}
					_ = resp.Body.Close()
				}()
			}
		}
	}
}

func (h *Harness) configureLoadBalancer() error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.configureLoadBalancerLocked()
}

func (h *Harness) configureLoadBalancerLocked() error {
	scenario := ScenarioConfig{Name: h.name, Strategy: h.strategy, Backends: make([]BackendConfig, 0, len(h.backends))}
	for _, backend := range h.backends {
		backend.mu.RLock()
		parsed, err := url.Parse(backend.URL)
		if err != nil {
			backend.mu.RUnlock()
			return fmt.Errorf("parse backend %s URL: %w", backend.ID, err)
		}
		port, err := strconv.Atoi(parsed.Port())
		if err != nil {
			backend.mu.RUnlock()
			return fmt.Errorf("parse backend %s port: %w", backend.ID, err)
		}
		scenario.Backends = append(scenario.Backends, BackendConfig{
			ID:          backend.ID,
			Address:     parsed.Hostname(),
			Port:        port,
			Weight:      backend.Weight,
			Latency:     backend.Latency,
			Healthy:     backend.Healthy,
			Failing:     backend.Failing,
			FailureRate: backend.FailureRate,
			Pool:        backend.Pool,
		})
		backend.mu.RUnlock()
	}
	return h.loadBalancer.ConfigureScenario(scenario)
}

func randomProcessingDelay() time.Duration {
	for {
		value := rand.NormFloat64()*processingDelaySigma + processingDelayMeanMS
		if value >= processingDelayMinMS && value <= processingDelayMaxMS {
			return time.Duration(value) * time.Millisecond
		}
	}
}

// newBackend starts one simulated backend. The listener host ALWAYS comes
// from the harness binding policy (never from per-backend config, so even a
// misused API layer cannot steer bind addresses); cfg.Address remains only
// as descriptive input. The advertised URL host comes from the policy too,
// which is how an out-of-process load balancer reaches the listeners.
func newBackend(cfg BackendConfig, binding BackendBinding) (*Backend, error) {
	if cfg.ID == "" {
		return nil, fmt.Errorf("backend id is required")
	}
	listenHost := binding.bindHost()
	if cfg.Port == 0 {
		cfg.Port = 0
	}
	if cfg.Weight <= 0 {
		cfg.Weight = defaultBackendWeight
	}
	if cfg.Pool == "" {
		cfg.Pool = defaultPoolName
	}
	if !cfg.Healthy {
		cfg.Failing = true
	}

	listener, err := listenForBackend(listenHost, cfg.Port)
	if err != nil {
		return nil, fmt.Errorf("listen on %s:%d for backend %s: %w", listenHost, cfg.Port, cfg.ID, err)
	}
	advertisePort := listenerPortOf(listener)

	backend := &Backend{
		ID:          cfg.ID,
		Name:        cfg.ID,
		Weight:      cfg.Weight,
		Latency:     cfg.Latency,
		Healthy:     cfg.Healthy,
		Failing:     cfg.Failing,
		FailureRate: cfg.FailureRate,
		Pool:        cfg.Pool,
		listener:    listener,
		URL:         "http://" + net.JoinHostPort(binding.advertiseHost(), strconv.Itoa(advertisePort)),
	}
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
			if failing || failureRate >= 1.0 || (failureRate > 0 && time.Now().UnixNano()%int64(failureRateScale) < int64(failureRate*failureRateScale)) {
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

	return backend, nil
}

// listenerPortOf extracts the concrete port a listener bound to (needed for
// advertise URLs because kernel-assigned ports are not known until bind).
func listenerPortOf(l net.Listener) int {
	if tcpAddr, ok := l.Addr().(*net.TCPAddr); ok {
		return tcpAddr.Port
	}
	host, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		return 0
	}
	_ = host
	n, err := strconv.Atoi(port)
	if err != nil {
		return 0
	}
	return n
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
