package routing

import (
	"testing"

	"github.com/marcusaroldan/load-balancer/internal/pool"
)

func TestRoundRobinStrategySelectsHealthyBackends(t *testing.T) {
	p := &pool.Pool{
		ID:       "pool-a",
		Metadata: map[string]string{"strategy": StrategyRoundRobin},
		Backends: map[string]*pool.Backend{
			"b1": {ID: "b1", Healthy: true},
			"b2": {ID: "b2", Healthy: true},
			"b3": {ID: "b3", Healthy: true},
		},
	}

	strategy := NewRoundRobinStrategy()
	for i := 0; i < 6; i++ {
		backend, err := strategy.Select(p)
		if err != nil {
			t.Fatalf("Select returned err: %v", err)
		}
		if backend == nil || !backend.Healthy {
			t.Fatal("expected healthy backend")
		}
	}
}

func TestWeightedStrategyPrefersHigherWeight(t *testing.T) {
	p := &pool.Pool{
		ID:       "pool-a",
		Metadata: map[string]string{"strategy": StrategyWeighted},
		Backends: map[string]*pool.Backend{
			"b1": {ID: "b1", Healthy: true},
			"b2": {ID: "b2", Healthy: true},
		},
	}
	policy := map[string]BackendPolicy{
		"b1": {Weight: 5},
		"b2": {Weight: 1},
	}

	strategy := NewWeightedStrategy(policy)
	backend, err := strategy.Select(p)
	if err != nil {
		t.Fatalf("Select returned err: %v", err)
	}
	if backend == nil {
		t.Fatal("expected backend")
	}
	if backend.ID == "b2" {
		t.Fatal("expected higher weight backend to be selected more often")
	}
}

func TestLeastConnectionsStrategyChoosesLowestUsage(t *testing.T) {
	p := &pool.Pool{
		ID:       "pool-a",
		Metadata: map[string]string{"strategy": StrategyLeastConnections},
		Backends: map[string]*pool.Backend{
			"b1": {ID: "b1", Healthy: true},
			"b2": {ID: "b2", Healthy: true},
		},
	}
	policy := map[string]BackendPolicy{
		"b1": {ActiveConnections: 4},
		"b2": {ActiveConnections: 1},
	}

	strategy := NewLeastConnectionsStrategy(policy)
	backend, err := strategy.Select(p)
	if err != nil {
		t.Fatalf("Select returned err: %v", err)
	}
	if backend == nil || backend.ID != "b2" {
		t.Fatalf("expected least-connections backend b2, got %v", backend)
	}
}

func TestSelectorDefaultsToRoundRobin(t *testing.T) {
	selector := NewSelector()
	p := &pool.Pool{
		ID: "pool-a",
		Backends: map[string]*pool.Backend{
			"b1": {ID: "b1", Healthy: true},
			"b2": {ID: "b2", Healthy: true},
		},
	}

	backend, err := selector.Select(p, nil)
	if err != nil {
		t.Fatalf("Select returned err: %v", err)
	}
	if backend == nil || !backend.Healthy {
		t.Fatal("expected round robin fallback selection")
	}
}

func TestSetPoolStrategyAllowsDynamicUpdate(t *testing.T) {
	selector := NewSelector()
	p := &pool.Pool{
		ID:       "pool-a",
		Metadata: map[string]string{},
		Backends: map[string]*pool.Backend{
			"b1": {ID: "b1", Healthy: true},
		},
	}

	if err := selector.SetPoolStrategy(p, StrategyWeighted); err != nil {
		t.Fatalf("SetPoolStrategy returned err: %v", err)
	}
	if selector.StrategyForPool(p) != StrategyWeighted {
		t.Fatalf("expected strategy to be %q, got %q", StrategyWeighted, selector.StrategyForPool(p))
	}
}
