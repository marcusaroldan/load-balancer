package health

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/marcusaroldan/load-balancer/internal/pool"
)

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
		interval = 5 * time.Second
	}
	if path == "" {
		path = "/health"
	}

	return &Checker{
		store:    store,
		interval: interval,
		path:     path,
		client:   &http.Client{Timeout: 2 * time.Second},
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
		if err := c.CheckBackend(backend); err != nil {
			return err
		}
	}
	return nil
}

// CheckBackend probes a single backend and updates the in-memory state.
func (c *Checker) CheckBackend(backend *pool.Backend) error {
	if backend == nil {
		return nil
	}

	url := backend.Address + c.path
	resp, err := c.client.Get(url)
	if err != nil {
		backend.Healthy = false
		return err
	}
	defer resp.Body.Close()

	healthy := resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusBadRequest
	backend.Healthy = healthy
	return nil
}

func (c *Checker) checkAllPools() {
	for _, p := range c.store.ListPools() {
		_ = c.CheckPool(p.ID)
	}
}
