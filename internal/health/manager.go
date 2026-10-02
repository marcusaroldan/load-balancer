package health

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/marcusaroldan/load-balancer/internal/config"
	"github.com/marcusaroldan/load-balancer/internal/pool"
	"github.com/marcusaroldan/load-balancer/internal/registry"
)

// ErrNotRunning reports lifecycle operations on an idle manager.
var ErrNotRunning = errors.New("health manager not running")

// Transition records one observed health state change for operators/tests.
type Transition struct {
	PoolID    string
	BackendID string
	Healthy   bool
	At        time.Time
}

// ProbeResult is the outcome of a single backend probe.
type ProbeResult struct {
	Healthy  bool
	Elapsed  time.Duration
	Attempts int // probes consumed before the transition decision
}

// Manager drives active health checks against the registry: per-pool policy
// from canonical config, request timeouts, threshold hysteresis, startup
// grace, and race-free Start/Stop/re-Start lifecycle (context-cancel based;
// replaces the legacy stopCh close/restart pattern that raced).
type Manager struct {
	registry *registry.Registry
	client   *http.Client
	interval time.Duration // tick between sweep rounds
	log      func(Transition)

	mu          sync.Mutex
	running     bool
	cancel      context.CancelFunc
	done        chan struct{}
	transitions []Transition
	probeCount  map[string]int // backendKey -> probes completed (for tests/ops)
}

// NewManager creates a checker bound to a registry. interval <= 0 selects the
// documented default sweep cadence. logFn may be nil.
func NewManager(reg *registry.Registry, interval time.Duration, logFn func(Transition)) *Manager {
	if interval <= 0 {
		interval = defaultHealthCheckInterval
	}
	return &Manager{
		registry:   reg,
		client:     &http.Client{},
		interval:   interval,
		log:        logFn,
		probeCount: make(map[string]int),
	}
}

// Start launches the periodic sweep loop. Idempotent-safe: starting a running
// manager returns ErrAlreadyRunning-style no-op success via running flag.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.done = make(chan struct{})
	m.running = true
	m.mu.Unlock()

	go m.loop(runCtx, m.done)
	return nil
}

// Stop halts the loop and waits for the goroutine to exit. Safe when idle.
func (m *Manager) Stop() error {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return ErrNotRunning
	}
	cancel, done := m.cancel, m.done
	m.running = false
	m.mu.Unlock()

	cancel()
	<-done
	return nil
}

// Running reports loop status (used by /readyz wiring later).
func (m *Manager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// Transitions returns a copy of recorded state changes.
func (m *Manager) Transitions() []Transition {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Transition, len(m.transitions))
	copy(out, m.transitions)
	return out
}

func (m *Manager) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.SweepOnce(ctx)
		}
	}
}

// SweepOnce probes every backend once through current policy. Exported for
// deterministic tests and manual operator triggers.
func (m *Manager) SweepOnce(ctx context.Context) {
	snap := m.registry.Snapshot()
	for _, poolID := range snap.Pools() {
		policy, ok := snap.HealthPolicyOf(poolID)
		if !ok {
			continue
		}
		p, ok := snap.Pool(poolID)
		if !ok {
			continue
		}
		for _, id := range sortedIDs(p) {
			b := p.Backends[id]
			result := m.Probe(ctx, b.Address, policy)
			m.applyResult(poolID, id, result)
		}
	}
}

// applyResult feeds probe outcomes into registry threshold logic and records
// transitions. Removal-during-check is tolerated: registry errors on missing
// resources are swallowed here because the backend simply no longer exists.
func (m *Manager) applyResult(poolID, backendID string, res ProbeResult) {
	m.mu.Lock()
	m.probeCount[poolID+"/"+backendID]++
	m.mu.Unlock()

	before, err := m.registry.BackendHealth(poolID, backendID)
	if err != nil {
		return
	}
	if res.Healthy {
		err = m.registry.MarkHealthy(poolID, backendID)
	} else {
		err = m.registry.MarkUnhealthy(poolID, backendID)
	}
	if err != nil {
		return // backend/pool removed mid-sweep: nothing to record
	}
	after, _ := m.registry.BackendHealth(poolID, backendID)

	if before != after {
		transition := Transition{PoolID: poolID, BackendID: backendID, Healthy: after, At: time.Now()}
		m.mu.Lock()
		m.transitions = append(m.transitions, transition)
		m.mu.Unlock()
		if m.log != nil {
			m.log(transition)
		}
	}
}

// Probe executes one HTTP GET with the policy timeout and classifies results:
// transport error or timeout = failure; non-2xx/3xx = failure; else success.
func (m *Manager) Probe(ctx context.Context, address string, policy config.HealthPolicy) ProbeResult {
	timeout := policy.Timeout()
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	target, err := joinProbeURL(address, policy.Path)
	if err != nil {
		return ProbeResult{Healthy: false}
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target, nil)
	if err != nil {
		return ProbeResult{Healthy: false}
	}

	started := time.Now()
	resp, err := m.client.Do(req)
	elapsed := time.Since(started)
	if err != nil {
		return ProbeResult{Healthy: false, Elapsed: elapsed}
	}
	defer resp.Body.Close()

	healthy := resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusBadRequest
	return ProbeResult{Healthy: healthy, Elapsed: elapsed}
}

// sortedIDs lists backend IDs deterministically so sweeps are reproducible.
func sortedIDs(p *pool.Pool) []string {
	ids := make([]string, 0, len(p.Backends))
	for id := range p.Backends {
		ids = append(ids, id)
	}
	sortStrings(ids)
	return ids
}

func sortStrings(ids []string) {
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
}

func joinProbeURL(address, path string) (string, error) {
	if path == "" {
		path = defaultHealthCheckPath
	}
	base, err := url.Parse(address)
	if err != nil {
		return "", fmt.Errorf("probe address %q: %w", address, err)
	}
	base.Path = strings.TrimSuffix(base.Path, "/") + path
	return base.String(), nil
}
