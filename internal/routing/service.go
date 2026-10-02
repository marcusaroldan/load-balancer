package routing

import (
	"errors"
	"fmt"
	"sync"

	"github.com/marcusaroldan/load-balancer/internal/pool"
)

// ErrUnknownStrategy is returned when a strategy name has no implementation.
// Selection never falls back silently; callers must handle the error.
var ErrUnknownStrategy = errors.New("unknown routing strategy")

// Service is the stateful production routing service. It owns per-pool
// selector state for all strategies, so rotation progress survives across
// requests while remaining isolated per pool. Membership or health changes
// require ResetPool to drop stale state (PROD-002 acceptance: "pool membership
// changes do not corrupt selector state").
type Service struct {
	mu     sync.Mutex
	states map[string]*serviceState
}

type serviceState struct {
	roundRobinIndex int
	weightedIndex   int
}

// NewService creates an empty routing service.
func NewService() *Service {
	return &Service{states: make(map[string]*serviceState)}
}

// Select routes one request within p using the named strategy. An empty name
// defaults to round robin (the documented system default, not a fallback for
// unknown values — those return ErrUnknownStrategy).
func (s *Service) Select(strategy string, p *pool.Pool, policy map[string]BackendPolicy) (*pool.Backend, error) {
	if p == nil {
		return nil, errors.New("pool is required")
	}
	p = filteredPool(p, policy)
	switch strategy {
	case "", StrategyRoundRobin:
		return s.roundRobin(p)
	case StrategyWeighted:
		return s.weightedRoundRobin(p, policy)
	case StrategyLeastConnections:
		return leastHealthy(p, policy)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownStrategy, strategy)
	}
}

// ResetPool drops selection state after membership or health changes.
func (s *Service) ResetPool(poolID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.states, poolID)
}

// ResetAll clears every pool's selection state (full registry reload).
func (s *Service) ResetAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states = make(map[string]*serviceState)
}

func (s *Service) stateFor(poolID string) *serviceState {
	state, ok := s.states[poolID]
	if !ok {
		state = &serviceState{}
		s.states[poolID] = state
	}
	return state
}

func (s *Service) roundRobin(p *pool.Pool) (*pool.Backend, error) {
	healthy := sortedHealthy(p)
	if len(healthy) == 0 {
		return nil, ErrNoHealthyBackends
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.stateFor(p.ID)
	backend := healthy[state.roundRobinIndex%len(healthy)]
	state.roundRobinIndex++
	return backend, nil
}

func (s *Service) weightedRoundRobin(p *pool.Pool, policy map[string]BackendPolicy) (*pool.Backend, error) {
	healthy := sortedHealthy(p)
	if len(healthy) == 0 {
		return nil, ErrNoHealthyBackends
	}
	weighted := make([]*pool.Backend, 0, len(healthy))
	for _, backend := range healthy {
		weight := policy[backend.ID].Weight
		if weight <= 0 {
			weight = DefaultBackendWeight
		}
		for i := 0; i < weight; i++ {
			weighted = append(weighted, backend)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.stateFor(p.ID)
	backend := weighted[state.weightedIndex%len(weighted)]
	state.weightedIndex++
	return backend, nil
}
