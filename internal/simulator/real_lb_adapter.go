package simulator

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/marcusaroldan/load-balancer/internal/pool"
	"github.com/marcusaroldan/load-balancer/internal/routing"
)

// RealLoadBalancerAdapter runs the production routing and proxy path in-process.
// It owns only runtime lifecycle, configuration translation, and aggregate metrics.
type RealLoadBalancerAdapter struct {
	mu       sync.RWMutex
	name     string
	strategy string
	configs  map[string]BackendConfig
	store    *pool.InMemoryStore
	selector *routing.Selector
	server   *http.Server
	listener net.Listener
	endpoint string

	requestCount       atomic.Int64
	successfulRequests atomic.Int64
	failedRequests     atomic.Int64
	totalLatencyNanos  atomic.Int64
}

// NewRealLoadBalancerAdapter creates a real in-process LB runtime.
func NewRealLoadBalancerAdapter() *RealLoadBalancerAdapter {
	return &RealLoadBalancerAdapter{
		strategy: routing.StrategyRoundRobin,
		configs:  make(map[string]BackendConfig),
		store:    pool.NewInMemoryStore(),
		selector: routing.NewSelector(),
	}
}

func (a *RealLoadBalancerAdapter) Start() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.server != nil {
		return nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen for simulator load balancer: %w", err)
	}
	a.listener = listener
	a.endpoint = "http://" + listener.Addr().String()
	a.server = &http.Server{Handler: http.HandlerFunc(a.serveHTTP)}
	go func(server *http.Server, listener net.Listener) {
		_ = server.Serve(listener)
	}(a.server, listener)
	return nil
}

func (a *RealLoadBalancerAdapter) Stop() error {
	a.mu.Lock()
	server := a.server
	a.server = nil
	a.listener = nil
	a.endpoint = ""
	a.mu.Unlock()
	if server == nil {
		return nil
	}
	return server.Close()
}

func (a *RealLoadBalancerAdapter) Endpoint() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.endpoint
}

func (a *RealLoadBalancerAdapter) ConfigureScenario(scenario ScenarioConfig) error {
	mapped, err := mapScenarioToLoadBalancer(scenario)
	if err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.name = mapped.Name
	a.strategy = mapped.Strategy
	a.configs = mapped.Backends
	return a.rebuildStoreLocked()
}

type loadBalancerScenario struct {
	Name     string
	Strategy string
	Backends map[string]BackendConfig
}

// mapScenarioToLoadBalancer translates simulator input into the configuration
// consumed by the real pool and routing implementations. It performs all
// validation before the adapter is mutated, so a rejected scenario cannot leave
// the running LB partially reconfigured.
func mapScenarioToLoadBalancer(scenario ScenarioConfig) (loadBalancerScenario, error) {
	name := strings.TrimSpace(scenario.Name)
	if name == "" {
		return loadBalancerScenario{}, fmt.Errorf("scenario name is required")
	}
	strategy := strings.TrimSpace(scenario.Strategy)
	if strategy == "" {
		strategy = routing.StrategyRoundRobin
	}
	if !validStrategy(strategy) {
		return loadBalancerScenario{}, fmt.Errorf("unknown strategy %q", strategy)
	}

	configs := make(map[string]BackendConfig, len(scenario.Backends))
	for _, backend := range scenario.Backends {
		backend.ID = strings.TrimSpace(backend.ID)
		if backend.ID == "" {
			return loadBalancerScenario{}, fmt.Errorf("backend id is required")
		}
		if _, exists := configs[backend.ID]; exists {
			return loadBalancerScenario{}, fmt.Errorf("backend %q is duplicated", backend.ID)
		}
		backend.Address = strings.TrimSpace(backend.Address)
		if backend.Address == "" {
			backend.Address = defaultLoopbackAddress
		}
		if backend.Port < 0 || backend.Port > 65535 {
			return loadBalancerScenario{}, fmt.Errorf("backend %q port must be between 0 and 65535", backend.ID)
		}
		if backend.Weight <= 0 {
			backend.Weight = defaultBackendWeight
		}
		if backend.Latency < 0 {
			return loadBalancerScenario{}, fmt.Errorf("backend %q latency cannot be negative", backend.ID)
		}
		if backend.FailureRate < 0 || backend.FailureRate > 1 {
			return loadBalancerScenario{}, fmt.Errorf("backend %q failure rate must be between 0 and 1", backend.ID)
		}
		backend.Pool = strings.TrimSpace(backend.Pool)
		if backend.Pool == "" {
			backend.Pool = defaultPoolName
		}
		configs[backend.ID] = backend
	}

	return loadBalancerScenario{Name: name, Strategy: strategy, Backends: configs}, nil
}

func (a *RealLoadBalancerAdapter) SetStrategy(strategy string) error {
	if !validStrategy(strategy) {
		return fmt.Errorf("unknown strategy %q", strategy)
	}
	a.mu.Lock()
	previousStrategy := a.strategy
	a.strategy = strategy
	if err := a.rebuildStoreLocked(); err != nil {
		a.strategy = previousStrategy
		a.mu.Unlock()
		return err
	}
	a.mu.Unlock()
	return nil
}

func (a *RealLoadBalancerAdapter) SetBackendPool(backendID, backendPool string) error {
	if strings.TrimSpace(backendPool) == "" {
		backendPool = defaultPoolName
	}
	return a.updateBackend(backendID, func(backend *BackendConfig) {
		backend.Pool = backendPool
	})
}

func (a *RealLoadBalancerAdapter) SetBackendWeight(backendID string, weight int) error {
	if weight <= 0 {
		return fmt.Errorf("weight must be greater than 0")
	}
	return a.updateBackend(backendID, func(backend *BackendConfig) {
		backend.Weight = weight
	})
}

func (a *RealLoadBalancerAdapter) SetBackendHealth(backendID string, healthy bool) error {
	return a.updateBackend(backendID, func(backend *BackendConfig) {
		backend.Healthy = healthy
		backend.Failing = !healthy
	})
}

func (a *RealLoadBalancerAdapter) SetBackendLatency(backendID string, latency time.Duration) error {
	if latency < 0 {
		return fmt.Errorf("latency cannot be negative")
	}
	return a.updateBackend(backendID, func(backend *BackendConfig) {
		backend.Latency = latency
	})
}

func (a *RealLoadBalancerAdapter) SetBackendFailureRate(backendID string, rate float64) error {
	if rate < 0 || rate > 1 {
		return fmt.Errorf("failure rate must be between 0 and 1")
	}
	return a.updateBackend(backendID, func(backend *BackendConfig) {
		backend.FailureRate = rate
	})
}

func (a *RealLoadBalancerAdapter) RemoveBackend(backendID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.configs[backendID]; !ok {
		return fmt.Errorf("backend %s not found", backendID)
	}
	delete(a.configs, backendID)
	return a.rebuildStoreLocked()
}

func (a *RealLoadBalancerAdapter) Snapshot() LoadBalancerSnapshot {
	a.mu.RLock()
	name := a.name
	strategy := a.strategy
	configs := make([]BackendConfig, 0, len(a.configs))
	pools := make(map[string]struct{})
	for _, config := range a.configs {
		configs = append(configs, config)
		pools[config.Pool] = struct{}{}
	}
	a.mu.RUnlock()

	healthy := 0
	for _, config := range configs {
		if config.Healthy {
			healthy++
		}
	}
	requests := a.requestCount.Load()
	averageLatency := int64(0)
	if requests > 0 {
		averageLatency = (a.totalLatencyNanos.Load() / requests) / int64(time.Millisecond)
	}
	return LoadBalancerSnapshot{
		Name:                name,
		Strategy:            strategy,
		PoolCount:           len(pools),
		BackendCount:        len(configs),
		HealthyBackends:     healthy,
		RequestCount:        requests,
		SuccessfulRequests:  a.successfulRequests.Load(),
		FailedRequests:      a.failedRequests.Load(),
		AverageLatencyMS:    averageLatency,
		ObservedAt:          time.Now(),
		AvailableStrategies: availableStrategies(),
	}
}

func (a *RealLoadBalancerAdapter) updateBackend(backendID string, update func(*BackendConfig)) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	backend, ok := a.configs[backendID]
	if !ok {
		return fmt.Errorf("backend %s not found", backendID)
	}
	update(&backend)
	a.configs[backendID] = backend
	if err := a.rebuildStoreLocked(); err != nil {
		return err
	}
	return nil
}

func (a *RealLoadBalancerAdapter) rebuildStoreLocked() error {
	store := pool.NewInMemoryStore()
	for _, config := range a.configs {
		if err := store.AddBackend(config.Pool, pool.Backend{
			ID:      config.ID,
			Address: backendURL(config),
			Port:    config.Port,
			Healthy: config.Healthy,
		}); err != nil {
			return fmt.Errorf("add backend %s to pool %s: %w", config.ID, config.Pool, err)
		}
	}
	a.store = store
	a.selector = routing.NewSelector()
	return nil
}

func (a *RealLoadBalancerAdapter) serveHTTP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	a.requestCount.Add(1)
	status := http.StatusBadGateway

	a.mu.RLock()
	store := a.store
	strategy := a.strategy
	a.mu.RUnlock()

	configuredPools := store.ListPools()
	if len(configuredPools) == 0 {
		http.Error(w, "no backend pool configured", http.StatusServiceUnavailable)
		a.failedRequests.Add(1)
		a.totalLatencyNanos.Add(time.Since(started).Nanoseconds())
		return
	}
	selectedPool := configuredPools[0]
	metadata := make(map[string]string, len(selectedPool.Metadata)+1)
	for key, value := range selectedPool.Metadata {
		metadata[key] = value
	}
	metadata["strategy"] = strategy
	selectedPool.Metadata = metadata
	policy := make(map[string]routing.BackendPolicy, len(selectedPool.Backends))
	a.mu.RLock()
	for id, config := range a.configs {
		if config.Pool == selectedPool.ID {
			policy[id] = routing.BackendPolicy{Weight: config.Weight}
		}
	}
	a.mu.RUnlock()

	a.mu.RLock()
	selector := a.selector
	a.mu.RUnlock()
	selected, err := selector.Select(&selectedPool, policy)
	if err != nil {
		http.Error(w, "no healthy backend available", http.StatusServiceUnavailable)
		a.failedRequests.Add(1)
		a.totalLatencyNanos.Add(time.Since(started).Nanoseconds())
		return
	}
	target, err := url.Parse(selected.Address)
	if err != nil {
		http.Error(w, "invalid backend address", http.StatusInternalServerError)
		a.failedRequests.Add(1)
		a.totalLatencyNanos.Add(time.Since(started).Nanoseconds())
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(response http.ResponseWriter, request *http.Request, proxyErr error) {
		http.Error(response, "upstream request failed", http.StatusBadGateway)
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		status = response.StatusCode
		return nil
	}
	proxy.ServeHTTP(w, r)
	if status >= http.StatusOK && status < http.StatusBadRequest {
		a.successfulRequests.Add(1)
	} else {
		a.failedRequests.Add(1)
	}
	a.totalLatencyNanos.Add(time.Since(started).Nanoseconds())
}

func backendURL(config BackendConfig) string {
	return "http://" + net.JoinHostPort(config.Address, fmt.Sprintf("%d", config.Port))
}

func validStrategy(strategy string) bool {
	switch strategy {
	case routing.StrategyRoundRobin, routing.StrategyWeighted, routing.StrategyLeastConnections:
		return true
	default:
		return false
	}
}
