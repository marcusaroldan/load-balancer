package health

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/marcusaroldan/load-balancer/internal/pool"
)

const (
	defaultHealthCheckInterval = 5 * time.Second
	defaultHealthCheckPath     = "/health"
	defaultHTTPTimeout         = 2 * time.Second
)

// DefaultSweepInterval reports the documented default probe cadence, so the
// runtime flag default and the manager fallback share one value.
func DefaultSweepInterval() time.Duration { return defaultHealthCheckInterval }

// Checker periodically probes backend health and updates the in-memory state.
type Checker struct {
	store    *pool.InMemoryStore
	interval time.Duration
	path     string
	client   *http.Client

	mu       sync.Mutex
	stopCh   chan struct{}
	stopped  bool
	stopOnce sync.Once
}

// NewChecker creates a checker that probes backends with the configured interval and path.
func NewChecker(store *pool.InMemoryStore, interval time.Duration, path string) *Checker {
	if interval <= 0 {
		interval = defaultHealthCheckInterval
	}
	if path == "" {
		path = defaultHealthCheckPath
	}

	return &Checker{
		store:    store,
		interval: interval,
		path:     path,
		client:   &http.Client{Timeout: defaultHTTPTimeout},
		stopCh:   make(chan struct{}),
	}
}

// Start begins the periodic health-check loop.
func (c *Checker) Start(ctx context.Context) {
	c.mu.Lock()
	if c.stopped {
		c.stopCh = make(chan struct{})
		c.stopped = false
		c.stopOnce = sync.Once{}
	}
	c.mu.Unlock()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-c.stopCh:
				return
			case <-time.After(c.interval):
				c.checkAllPools()
			}
		}
	}()
}

// Stop cancels the health-check loop.
func (c *Checker) Stop() {
	c.mu.Lock()
	if c.stopCh != nil {
		c.stopOnce.Do(func() {
			close(c.stopCh)
		})
	}
	c.stopped = true
	c.mu.Unlock()
}

// CheckPool probes every backend in the pool and updates its health status in the store.
func (c *Checker) CheckPool(poolID string) error {
	p, ok := c.store.GetPool(poolID)
	if !ok {
		return fmt.Errorf("pool %q not found", poolID)
	}

	for _, backend := range p.Backends {
		if backend == nil {
			continue
		}
		if err := c.CheckBackend(p.ID, backend); err != nil {
			return err
		}
	}
	return nil
}

// CheckBackend probes a single backend and records the outcome in the store.
// Health state is written only through InMemoryStore.SetBackendHealth so the
// check loop never mutates Backend pointers shared with readers (race-free).
func (c *Checker) CheckBackend(poolID string, backend *pool.Backend) error {
	if backend == nil {
		return nil
	}

	target := backend.Address + c.path
	resp, err := c.client.Get(target)
	if err != nil {
		return c.store.SetBackendHealth(poolID, backend.ID, false)
	}
	defer resp.Body.Close()

	healthy := resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusBadRequest
	return c.store.SetBackendHealth(poolID, backend.ID, healthy)
}

func (c *Checker) checkAllPools() {
	for _, p := range c.store.ListPools() {
		_ = c.CheckPool(p.ID)
	}
}
