package registry

import (
	"fmt"
	"net/url"
	"strings"

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

// BackendURL is the exported helper used by the proxy and health layers:
// normalizes a backend address into a parseable URL, adding the default
// scheme when absent and folding in the port when the address omits it.
// Schemeless host[:port] forms (the common shape for the control-plane
// address+port fields) are ambiguous to url.Parse — "127.0.0.1:9102"
// fails with "first path segment cannot contain colon" — so the scheme is
// prepended before parsing instead of repairing the parse afterwards.
func BackendURL(b *pool.Backend) (*url.URL, error) {
	address := b.Address
	if address == "" {
		return nil, fmt.Errorf("backend %q has empty address", b.ID)
	}
	parseTarget := address
	if !strings.Contains(parseTarget, "://") {
		parseTarget = "http://" + parseTarget
	}
	parsed, err := url.Parse(parseTarget)
	if err != nil {
		return nil, fmt.Errorf("backend %q address %q: %w", b.ID, address, err)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("backend %q address %q: no host", b.ID, address)
	}
	if parsed.Port() == "" && b.Port > 0 {
		parsed.Host = fmt.Sprintf("%s:%d", parsed.Host, b.Port)
	}
	return parsed, nil
}
