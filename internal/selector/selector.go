package selector

import (
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/marcusaroldan/load-balancer/internal/pool"
)

var ErrNoHealthyBackends = errors.New("no healthy backends available")

// RoundRobin keeps per-pool selection state and separate healthy/unhealthy caches.
type RoundRobin struct {
	mu     sync.Mutex
	states map[string]*poolState
}

type poolState struct {
	index     int
	healthy   []*pool.Backend
	unhealthy map[string]*pool.Backend
}

// NewRoundRobin creates a selector with independent state per pool.
func NewRoundRobin() *RoundRobin {
	return &RoundRobin{
		states: make(map[string]*poolState),
	}
}

// Refresh rebuilds the cached healthy and unhealthy backend lists for a pool.
func (r *RoundRobin) Refresh(p *pool.Pool) {
	if p == nil {
		return
	}

	state := r.getOrCreateState(p.ID)
	state.healthy = state.healthy[:0]
	state.unhealthy = make(map[string]*pool.Backend)

	ids := make([]string, 0, len(p.Backends))
	for id := range p.Backends {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		backend := p.Backends[id]
		if backend == nil {
			continue
		}
		if backend.Healthy {
			state.healthy = append(state.healthy, backend)
			continue
		}
		state.unhealthy[backend.ID] = backend
	}

	if len(state.healthy) == 0 {
		state.index = 0
		return
	}
	if state.index >= len(state.healthy) {
		state.index = 0
	}
}

// SetHealth marks a backend healthy or unhealthy and updates the cached pool state.
func (r *RoundRobin) SetHealth(p *pool.Pool, backendID string, healthy bool) error {
	if p == nil {
		return errors.New("pool is required")
	}
	if backendID == "" {
		return errors.New("backend id is required")
	}

	backend, exists := p.Backends[backendID]
	if !exists || backend == nil {
		return fmt.Errorf("backend %q not found", backendID)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	backend.Healthy = healthy
	state := r.getOrCreateState(p.ID)
	state.healthy = state.healthy[:0]
	state.unhealthy = make(map[string]*pool.Backend)

	ids := make([]string, 0, len(p.Backends))
	for id := range p.Backends {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		item := p.Backends[id]
		if item == nil {
			continue
		}
		if item.Healthy {
			state.healthy = append(state.healthy, item)
			continue
		}
		state.unhealthy[item.ID] = item
	}

	if len(state.healthy) == 0 {
		state.index = 0
		return nil
	}
	if state.index >= len(state.healthy) {
		state.index = 0
	}
	return nil
}

// Next selects the next healthy backend for the pool using round-robin order.
func (r *RoundRobin) Next(p *pool.Pool) (*pool.Backend, error) {
	if p == nil {
		return nil, errors.New("pool is required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	state := r.getOrCreateState(p.ID)
	if len(state.healthy) == 0 {
		state.healthy = make([]*pool.Backend, 0)
		ids := make([]string, 0, len(p.Backends))
		for id := range p.Backends {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			backend := p.Backends[id]
			if backend != nil && backend.Healthy {
				state.healthy = append(state.healthy, backend)
			}
		}
		if len(state.healthy) == 0 {
			return nil, ErrNoHealthyBackends
		}
	}

	backend := state.healthy[state.index%len(state.healthy)]
	state.index = (state.index + 1) % len(state.healthy)
	return backend, nil
}

func (r *RoundRobin) getOrCreateState(poolID string) *poolState {
	if state, exists := r.states[poolID]; exists {
		return state
	}
	state := &poolState{
		healthy:   make([]*pool.Backend, 0),
		unhealthy: make(map[string]*pool.Backend),
	}
	r.states[poolID] = state
	return state
}
