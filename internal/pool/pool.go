package pool

import (
	"errors"
	"sync"
)

var (
	ErrPoolRequired           = errors.New("pool id is required")
	ErrBackendRequired        = errors.New("backend id is required")
	ErrBackendAddressRequired = errors.New("backend address is required")
	ErrPoolNotFound           = errors.New("pool not found")
	ErrBackendNotFound        = errors.New("backend not found")
	ErrBackendAlreadyExists   = errors.New("backend already exists")
)

// Backend represents a single upstream server in a pool.
type Backend struct {
	ID      string
	Address string
	Port    int
	PoolID  string
	Healthy bool
}

// Pool contains a collection of backends for a named load-balancing group.
type Pool struct {
	ID       string
	Name     string
	Metadata map[string]string
	Backends map[string]*Backend
}

// InMemoryStore is an in-memory registry of pools and their backends.
type InMemoryStore struct {
	mu    sync.RWMutex
	pools map[string]Pool
}

// NewInMemoryStore creates a new empty in-memory store.
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		pools: make(map[string]Pool),
	}
}

// AddBackend adds a backend to a pool. It creates the pool if it does not exist.
func (s *InMemoryStore) AddBackend(poolID string, backend Backend) error {
	if poolID == "" {
		return ErrPoolRequired
	}
	if backend.ID == "" {
		return ErrBackendRequired
	}
	if backend.Address == "" {
		return ErrBackendAddressRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	pool, exists := s.pools[poolID]
	if !exists {
		pool = Pool{
			ID:       poolID,
			Name:     poolID,
			Metadata: make(map[string]string),
			Backends: make(map[string]*Backend),
		}
	}

	if _, found := pool.Backends[backend.ID]; found {
		return ErrBackendAlreadyExists
	}

	backendCopy := backend
	backendCopy.PoolID = poolID
	pool.Backends[backend.ID] = &backendCopy
	s.pools[poolID] = pool

	return nil
}

// RemoveBackend removes a backend from a pool by its ID.
func (s *InMemoryStore) RemoveBackend(poolID, backendID string) error {
	if poolID == "" {
		return ErrPoolRequired
	}
	if backendID == "" {
		return ErrBackendRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	pool, exists := s.pools[poolID]
	if !exists {
		return ErrPoolNotFound
	}

	if _, found := pool.Backends[backendID]; !found {
		return ErrBackendNotFound
	}

	delete(pool.Backends, backendID)
	s.pools[poolID] = pool
	return nil
}

// GetPool returns a pool by ID if it exists.
func (s *InMemoryStore) GetPool(poolID string) (Pool, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	pool, exists := s.pools[poolID]
	return pool, exists
}

// ListPools returns all pools currently in the store.
func (s *InMemoryStore) ListPools() []Pool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	pools := make([]Pool, 0, len(s.pools))
	for _, pool := range s.pools {
		pools = append(pools, pool)
	}
	return pools
}

// ListBackends returns all backends for a pool.
func (s *InMemoryStore) ListBackends(poolID string) []*Backend {
	s.mu.RLock()
	defer s.mu.RUnlock()

	pool, exists := s.pools[poolID]
	if !exists {
		return nil
	}

	backends := make([]*Backend, 0, len(pool.Backends))
	for _, backend := range pool.Backends {
		backends = append(backends, backend)
	}
	return backends
}
