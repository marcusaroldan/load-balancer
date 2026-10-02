package registry

import (
	"fmt"
	"net/url"

	"github.com/marcusaroldan/load-balancer/internal/pool"
	"github.com/marcusaroldan/load-balancer/internal/routing"
)

// BackendRef is a selection result: the chosen backend plus its desired-state
// config needed by the data plane. Read-only by construction; the proxy layer
// never mutates registry state through it.
type BackendRef struct {
	Backend *pool.Backend
	Weight  int
}

// ID returns the backend identifier.
func (b *BackendRef) ID() string { return b.Backend.ID }

// URL builds the proxy target for this backend, normalizing scheme and port.
func (b *BackendRef) URL() (*url.URL, error) { return BackendURL(b.Backend) }

// Select chooses one eligible backend from a pool using the snapshot's
// strategy and policy maps. Atomic: reads only snapshot state.
func (s *Snapshot) Select(poolID string) (*BackendRef, error) {
	p, ok := s.pools[poolID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", pool.ErrPoolNotFound, poolID)
	}
	strategy := s.strategies[poolID]
	policy := make(map[string]routing.BackendPolicy, len(p.Backends))
	for id := range p.Backends {
		bc, has := s.backendConfigs[backendKey{poolID, id}]
		entry := routing.BackendPolicy{Weight: bc.Weight}
		if !has {
			entry.Weight = routing.DefaultBackendWeight
		}
		policy[id] = entry
	}
	backend, err := s.route.Select(strategy, p, policy)
	if err != nil {
		return nil, err
	}
	return &BackendRef{Backend: backend}, nil
}

// BackendURL is the exported helper used by the proxy layer: normalizes a
// backend address into a parseable URL, adding a scheme when absent and
// folding in the port when the address omits it.
func BackendURL(b *pool.Backend) (*url.URL, error) {
	address := b.Address
	if address == "" {
		return nil, fmt.Errorf("backend %q has empty address", b.ID)
	}
	parsed, err := url.Parse(address)
	if err != nil {
		return nil, fmt.Errorf("backend %q address %q: %w", b.ID, address, err)
	}
	if parsed.Scheme == "" {
		parsed.Scheme = "http"
	}
	if parsed.Port() == "" && b.Port > 0 {
		parsed.Host = fmt.Sprintf("%s:%d", parsed.Host, b.Port)
	}
	return parsed, nil
}
