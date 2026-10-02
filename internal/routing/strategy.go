package routing

import (
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/marcusaroldan/load-balancer/internal/pool"
)

const (
	StrategyRoundRobin       = "round_robin"
	StrategyWeighted         = "weighted_round_robin"
	StrategyLeastConnections = "least_connections"
)

// BackendPolicy contains strategy-specific metadata for a backend.
type BackendPolicy struct {
	Weight            int
	ActiveConnections int
	MaxConnections    int // NoConnectionLimit (-1) or 0 means unbounded
	Strategy          string
}

// NoConnectionLimit mirrors config.NoConnectionLimit; routing stays free of a
// config import by layering (registry maps desired state into policies).
const NoConnectionLimit = -1

// Strategy selects a backend from a pool according to a specific balancing algorithm.
type Strategy interface {
	Select(pool *pool.Pool) (*pool.Backend, error)
	Name() string
}

// Selector manages routing strategies and keeps state for each pool.
type Selector struct {
	mu              sync.Mutex
	states          map[string]*poolState
	defaultStrategy string
}

type poolState struct {
	roundRobinIndex int
	weightedIndex   int
	lastPicked      map[string]int
}

// NewSelector creates a selector that defaults to round robin.
func NewSelector() *Selector {
	return &Selector{
		states:          make(map[string]*poolState),
		defaultStrategy: StrategyRoundRobin,
	}
}

// Select chooses a backend from the pool using the configured strategy.
func (s *Selector) Select(p *pool.Pool, backendPolicy map[string]BackendPolicy) (*pool.Backend, error) {
	if p == nil {
		return nil, errors.New("pool is required")
	}

	cfgStrategy := s.defaultStrategy
	if poolStrategy, ok := p.Metadata["strategy"]; ok {
		cfgStrategy = poolStrategy
	}

	switch cfgStrategy {
	case StrategyWeighted:
		return NewWeightedStrategy(backendPolicy).Select(p)
	case StrategyLeastConnections:
		return NewLeastConnectionsStrategy(backendPolicy).Select(p)
	case StrategyRoundRobin, "":
		return NewRoundRobinStrategy().Select(p)
	default:
		return NewRoundRobinStrategy().Select(p)
	}
}

// RoundRobinStrategy selects healthy backends in round-robin order.
type RoundRobinStrategy struct {
	selector *Selector
}

func NewRoundRobinStrategy() *RoundRobinStrategy {
	return &RoundRobinStrategy{selector: NewSelector()}
}

func (s *RoundRobinStrategy) Name() string { return StrategyRoundRobin }

func (s *RoundRobinStrategy) Select(p *pool.Pool) (*pool.Backend, error) {
	if p == nil {
		return nil, errors.New("pool is required")
	}

	ids := make([]string, 0, len(p.Backends))
	for id := range p.Backends {
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, errors.New("no healthy backends available")
	}
	ids = slices.Clone(ids)
	slices.Sort(ids)

	healthy := make([]*pool.Backend, 0, len(p.Backends))
	for _, id := range ids {
		backend := p.Backends[id]
		if backend != nil && backend.Healthy {
			healthy = append(healthy, backend)
		}
	}
	if len(healthy) == 0 {
		return nil, errors.New("no healthy backends available")
	}

	s.selector.mu.Lock()
	state, ok := s.selector.states[p.ID]
	if !ok {
		state = &poolState{lastPicked: make(map[string]int)}
		s.selector.states[p.ID] = state
	}
	backend := healthy[state.roundRobinIndex%len(healthy)]
	state.roundRobinIndex = (state.roundRobinIndex + 1) % len(healthy)
	s.selector.mu.Unlock()
	return backend, nil
}

// WeightedStrategy applies a weighted round robin selection based on BackendPolicy.Weight.
type WeightedStrategy struct {
	policy map[string]BackendPolicy
	state  map[string]*poolState
	mu     sync.Mutex
}

func NewWeightedStrategy(policy map[string]BackendPolicy) *WeightedStrategy {
	return &WeightedStrategy{
		policy: policy,
		state:  make(map[string]*poolState),
	}
}

func (s *WeightedStrategy) Name() string { return StrategyWeighted }

func (s *WeightedStrategy) Select(p *pool.Pool) (*pool.Backend, error) {
	if p == nil {
		return nil, errors.New("pool is required")
	}

	ids := make([]string, 0, len(p.Backends))
	for id := range p.Backends {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	healthy := make([]*pool.Backend, 0, len(p.Backends))
	for _, id := range ids {
		backend := p.Backends[id]
		if backend != nil && backend.Healthy {
			healthy = append(healthy, backend)
		}
	}
	if len(healthy) == 0 {
		return nil, errors.New("no healthy backends available")
	}

	weighted := make([]*pool.Backend, 0, len(healthy))
	for _, backend := range healthy {
		weight := s.policy[backend.ID].Weight
		if weight <= 0 {
			weight = 1
		}
		for i := 0; i < weight; i++ {
			weighted = append(weighted, backend)
		}
	}
	if len(weighted) == 0 {
		return healthy[0], nil
	}

	s.mu.Lock()
	state, ok := s.state[p.ID]
	if !ok {
		state = &poolState{weightedIndex: 0}
		s.state[p.ID] = state
	}
	backend := weighted[state.weightedIndex%len(weighted)]
	state.weightedIndex = (state.weightedIndex + 1) % len(weighted)
	s.mu.Unlock()
	return backend, nil
}

// LeastConnectionsStrategy chooses the backend with the fewest active connections.
type LeastConnectionsStrategy struct {
	policy map[string]BackendPolicy
}

func NewLeastConnectionsStrategy(policy map[string]BackendPolicy) *LeastConnectionsStrategy {
	return &LeastConnectionsStrategy{policy: policy}
}

func (s *LeastConnectionsStrategy) Name() string { return StrategyLeastConnections }

func (s *LeastConnectionsStrategy) Select(p *pool.Pool) (*pool.Backend, error) {
	if p == nil {
		return nil, errors.New("pool is required")
	}

	ids := make([]string, 0, len(p.Backends))
	for id := range p.Backends {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	healthy := make([]*pool.Backend, 0, len(p.Backends))
	for _, id := range ids {
		backend := p.Backends[id]
		if backend != nil && backend.Healthy {
			healthy = append(healthy, backend)
		}
	}
	if len(healthy) == 0 {
		return nil, errors.New("no healthy backends available")
	}

	best := healthy[0]
	bestConnections := s.policy[best.ID].ActiveConnections
	for _, backend := range healthy[1:] {
		connections := s.policy[backend.ID].ActiveConnections
		if connections < bestConnections {
			best = backend
			bestConnections = connections
		}
	}
	return best, nil
}

func (s *Selector) SetDefaultStrategy(name string) {
	if name == "" {
		name = StrategyRoundRobin
	}
	s.defaultStrategy = name
}

func (s *Selector) StrategyForPool(p *pool.Pool) string {
	if p == nil {
		return s.defaultStrategy
	}
	if p.Metadata == nil {
		p.Metadata = make(map[string]string)
	}
	if strategy, ok := p.Metadata["strategy"]; ok && strategy != "" {
		return strategy
	}
	return s.defaultStrategy
}

func (s *Selector) SetPoolStrategy(p *pool.Pool, name string) error {
	if p == nil {
		return errors.New("pool is required")
	}
	if p.Metadata == nil {
		p.Metadata = make(map[string]string)
	}
	if name == "" {
		name = StrategyRoundRobin
	}
	switch name {
	case StrategyRoundRobin, StrategyWeighted, StrategyLeastConnections:
		p.Metadata["strategy"] = name
		return nil
	default:
		return fmt.Errorf("unknown strategy %q", name)
	}
}
