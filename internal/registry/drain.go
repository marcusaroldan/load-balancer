package registry

import (
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/marcusaroldan/load-balancer/internal/config"
	"github.com/marcusaroldan/load-balancer/internal/pool"
)

// observedState tracks live counters for one backend. It is never persisted:
// desired state lives in backendView.cfg, observations stay here (PROD plan
// separation rule).
type observedState struct {
	mu                sync.Mutex
	activeConnections int
}

// connTracker hands out bounded connection tickets. Acquire is atomic; the
// returned func releases exactly once (sync.Once guards double-release so an
// upstream error path and a defer can both call it safely).
type connTracker struct {
	max      int // config.NoConnectionLimit means unbounded
	observed *observedState
}

func (t *connTracker) acquire() (release func(), ok bool) {
	t.observed.mu.Lock()
	if t.max != config.NoConnectionLimit && t.observed.activeConnections >= t.max {
		t.observed.mu.Unlock()
		return nil, false
	}
	t.observed.activeConnections++
	t.observed.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			t.observed.mu.Lock()
			t.observed.activeConnections--
			t.observed.mu.Unlock()
		})
	}, true
}

// ActiveConnections reports current in-flight requests on a backend.
func (r *Registry) ActiveConnections(poolID, backendID string) (int, error) {
	r.mu.RLock()
	tracker, err := r.trackerLocked(poolID, backendID)
	r.mu.RUnlock()
	if err != nil {
		return 0, err
	}
	tracker.observed.mu.Lock()
	defer tracker.observed.mu.Unlock()
	return tracker.observed.activeConnections, nil
}

func (r *Registry) trackerLocked(poolID, backendID string) (*connTracker, error) {
	pv, ok := r.pools[poolID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", pool.ErrPoolNotFound, poolID)
	}
	v, ok := pv.backends[backendID]
	if !ok {
		return nil, fmt.Errorf("%w: backend %q in pool %q", pool.ErrBackendNotFound, backendID, poolID)
	}
	if v.trackers == nil {
		v.trackers = &connTracker{max: v.cfg.MaxConnections, observed: &observedState{}}
	}
	return v.trackers, nil
}

// AcquireConnection reserves one connection slot against the backend's
// max_connections limit. The caller MUST invoke the returned release when the
// request completes (it is idempotent).
func (r *Registry) AcquireConnection(poolID, backendID string) (release func(), err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tracker, err := r.trackerLocked(poolID, backendID)
	if err != nil {
		return nil, err
	}
	release, ok := tracker.acquire()
	if !ok {
		return nil, ErrConnectionLimitReached
	}
	return release, nil
}

// BeginDrain marks a backend draining: no new selections are made, existing
// connections may finish. Removal of a draining backend is blocked until its
// active count reaches zero or the drain deadline passes.
func (r *Registry) BeginDrain(poolID, backendID string) (drainedConnections int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pv, ok := r.pools[poolID]
	if !ok {
		return 0, fmt.Errorf("%w: %s", pool.ErrPoolNotFound, poolID)
	}
	v, ok := pv.backends[backendID]
	if !ok {
		return 0, fmt.Errorf("%w: backend %q in pool %q", pool.ErrBackendNotFound, backendID, poolID)
	}
	v.draining = true
	r.version++
	r.route.ResetPool(poolID)
	if v.trackers != nil {
		v.trackers.observed.mu.Lock()
		drainedConnections = v.trackers.observed.activeConnections
		v.trackers.observed.mu.Unlock()
	}
	return drainedConnections, nil
}

// RemoveDrained deletes a backend that has finished draining. It refuses while
// connections remain unless force is set (deadline exceeded), preventing the
// counter-leak class of bug where removal resets counts mid-request.
func (r *Registry) RemoveDrained(poolID, backendID string, force bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	pv, ok := r.pools[poolID]
	if !ok {
		return fmt.Errorf("%w: %s", pool.ErrPoolNotFound, poolID)
	}
	v, ok := pv.backends[backendID]
	if !ok {
		return fmt.Errorf("%w: backend %q in pool %q", pool.ErrBackendNotFound, backendID, poolID)
	}
	if !v.draining && !force {
		return fmt.Errorf("backend %q is not draining; call BeginDrain first", backendID)
	}
	if v.trackers != nil {
		v.trackers.observed.mu.Lock()
		active := v.trackers.observed.activeConnections
		v.trackers.observed.mu.Unlock()
		if active > 0 && !force {
			return ErrDrainPending
		}
	}
	delete(pv.backends, backendID)
	r.version++
	r.route.ResetPool(poolID)
	return nil
}

// DrainPendingError distinguishes "still serving in-flight requests" from
// hard failures so handlers can report drain status instead of lying with 200.
var ErrDrainPending = fmt.Errorf("drain pending")

// ConnectionLimitReached signals a backend at capacity; the data plane should
// try another backend or fail with 503 rather than queueing.
var ErrConnectionLimitReached = fmt.Errorf("connection limit reached")

// backendURL normalizes a backend address into a parseable URL, adding a
// scheme when absent and folding the port in when the address omits it.
func backendURL(b *pool.Backend) (*url.URL, error) {
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

// WaitDrain polls the backend's active connections until they reach zero or
// the deadline elapses. Returns the last observed count and whether it fully
// drained. Poll interval is fixed to keep this dependency-free.
func (r *Registry) WaitDrain(poolID, backendID string, timeout time.Duration) (remaining int, complete bool) {
	deadline := time.Now().Add(timeout)
	for {
		count, err := r.ActiveConnections(poolID, backendID)
		if err != nil || count == 0 {
			return count, true
		}
		if time.Now().After(deadline) {
			return count, false
		}
		time.Sleep(drainPollInterval)
	}
}

const drainPollInterval = 25 * time.Millisecond
