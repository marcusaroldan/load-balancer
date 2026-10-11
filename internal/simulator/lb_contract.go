package simulator

import (
	"fmt"
	"github.com/marcusaroldan/load-balancer/internal/routing"
	"time"
)

// LoadBalancerAdapter defines the minimal public contract between the simulator
// and the real load balancer. The simulator is allowed to control configuration
// and environment conditions only through this boundary; it must never reach
// into private routing state or duplicate the selection logic.
type LoadBalancerAdapter interface {
	Start() error
	Stop() error
	Endpoint() string
	ConfigureScenario(scenario ScenarioConfig) error
	SetStrategy(strategy string) error
	SetBackendPool(backendID, pool string) error
	SetBackendWeight(backendID string, weight int) error
	SetBackendHealth(backendID string, healthy bool) error
	SetBackendLatency(backendID string, latency time.Duration) error
	SetBackendFailureRate(backendID string, rate float64) error
	RemoveBackend(backendID string) error
	Snapshot() LoadBalancerSnapshot
}

// LoadBalancerSnapshot is the public observation model for the simulator UI.
// It intentionally omits any internal routing state, route trace, or selected
// backend identity so the simulator remains an environment-focused dashboard.
type LoadBalancerSnapshot struct {
	Name                string    `json:"name"`
	Strategy            string    `json:"strategy"`
	PoolCount           int       `json:"pool_count"`
	BackendCount        int       `json:"backend_count"`
	HealthyBackends     int       `json:"healthy_backends"`
	RequestCount        int64     `json:"request_count"`
	SuccessfulRequests  int64     `json:"successful_requests"`
	FailedRequests      int64     `json:"failed_requests"`
	AverageLatencyMS    int64     `json:"average_latency_ms"`
	ObservedAt          time.Time `json:"observed_at"`
	AvailableStrategies []string  `json:"available_strategies"`
}

// AdapterFactory creates a concrete LB adapter for the simulator.
type AdapterFactory func() (LoadBalancerAdapter, error)

// availableStrategies is the single source of truth for the UI's strategy
// picker; every adapter reports the same list (both real adapters route via
// the registry, which rejects anything outside this set).
func availableStrategies() []string {
	return []string{routing.StrategyRoundRobin, routing.StrategyWeighted, routing.StrategyLeastConnections}
}

// NewAdapterFactory returns the default adapter factory for the real load balancer.
func NewAdapterFactory() AdapterFactory {
	return func() (LoadBalancerAdapter, error) {
		return NewRealLoadBalancerAdapter(), nil
	}
}

// NoopLoadBalancerAdapter remains available for contract-only consumers.
type NoopLoadBalancerAdapter struct{}

func (a *NoopLoadBalancerAdapter) Start() error { return nil }

func (a *NoopLoadBalancerAdapter) Stop() error { return nil }

func (a *NoopLoadBalancerAdapter) Endpoint() string { return "" }

func (a *NoopLoadBalancerAdapter) ConfigureScenario(scenario ScenarioConfig) error {
	if scenario.Name == "" {
		return fmt.Errorf("scenario name is required")
	}
	return nil
}

func (a *NoopLoadBalancerAdapter) SetStrategy(strategy string) error {
	if strategy == "" {
		return fmt.Errorf("strategy is required")
	}
	return nil
}

func (a *NoopLoadBalancerAdapter) SetBackendPool(backendID, pool string) error {
	if backendID == "" {
		return fmt.Errorf("backend id is required")
	}
	return nil
}

func (a *NoopLoadBalancerAdapter) SetBackendWeight(backendID string, weight int) error {
	if backendID == "" {
		return fmt.Errorf("backend id is required")
	}
	if weight <= 0 {
		return fmt.Errorf("weight must be greater than 0")
	}
	return nil
}

func (a *NoopLoadBalancerAdapter) SetBackendHealth(backendID string, healthy bool) error {
	if backendID == "" {
		return fmt.Errorf("backend id is required")
	}
	return nil
}

func (a *NoopLoadBalancerAdapter) SetBackendLatency(backendID string, latency time.Duration) error {
	if backendID == "" {
		return fmt.Errorf("backend id is required")
	}
	return nil
}

func (a *NoopLoadBalancerAdapter) SetBackendFailureRate(backendID string, rate float64) error {
	if backendID == "" {
		return fmt.Errorf("backend id is required")
	}
	if rate < 0 || rate > 1 {
		return fmt.Errorf("failure rate must be between 0 and 1")
	}
	return nil
}

func (a *NoopLoadBalancerAdapter) RemoveBackend(backendID string) error {
	if backendID == "" {
		return fmt.Errorf("backend id is required")
	}
	return nil
}

func (a *NoopLoadBalancerAdapter) Snapshot() LoadBalancerSnapshot {
	return LoadBalancerSnapshot{
		Name:                "not-started",
		Strategy:            "round_robin",
		AvailableStrategies: availableStrategies(),
		ObservedAt:          time.Now(),
	}
}
