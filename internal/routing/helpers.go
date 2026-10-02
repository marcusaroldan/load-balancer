package routing

import (
	"errors"
	"slices"

	"github.com/marcusaroldan/load-balancer/internal/pool"
)

// ErrNoHealthyBackends is returned when a pool has no eligible members.
var ErrNoHealthyBackends = errors.New("no healthy backends available")

// DefaultBackendWeight mirrors config.DefaultBackendWeight; kept as a local
// constant because routing must not import the config package (layering:
// registry composes both, they never depend on each other).
const DefaultBackendWeight = 1

// sortedHealthy returns healthy backends in deterministic ID order.
func sortedHealthy(p *pool.Pool) []*pool.Backend {
	healthy := make([]*pool.Backend, 0, len(p.Backends))
	for _, backend := range p.Backends {
		if backend != nil && backend.Healthy {
			healthy = append(healthy, backend)
		}
	}
	slices.SortFunc(healthy, func(a, b *pool.Backend) int {
		return compareStrings(a.ID, b.ID)
	})
	return healthy
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// leastHealthy picks the healthy backend with the fewest active connections.
// Ties break by backend ID for determinism.
func leastHealthy(p *pool.Pool, policy map[string]BackendPolicy) (*pool.Backend, error) {
	healthy := sortedHealthy(p)
	if len(healthy) == 0 {
		return nil, ErrNoHealthyBackends
	}
	best := healthy[0]
	bestConnections := policy[best.ID].ActiveConnections
	for _, backend := range healthy[1:] {
		connections := policy[backend.ID].ActiveConnections
		if connections < bestConnections || (connections == bestConnections && backend.ID < best.ID) {
			best = backend
			bestConnections = connections
		}
	}
	return best, nil
}
