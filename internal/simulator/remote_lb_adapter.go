package simulator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/marcusaroldan/load-balancer/internal/routing"
)

// ErrRemoteLBUnreachable marks control-plane communication failures.
var ErrRemoteLBUnreachable = errors.New("remote load balancer unreachable")

const (
	remoteLBTimeout     = 2 * time.Second
	remoteLBPoolName    = "sim"
	remoteLBSnapshotTTL = 250 * time.Millisecond
	idleScenarioName    = "idle"
)

// RemoteLBConfig points the simulator at an out-of-process load balancer:
// the control-plane base URL (mutations + metrics) and the data-plane URL
// that synthetic clients target.
type RemoteLBConfig struct {
	ControlURL string
	DataURL    string
	// Timeout optionally overrides remoteLBTimeout (tests).
	Timeout time.Duration
}

// RemoteLoadBalancerAdapter drives a real, separate load-balancer process
// through its public control-plane API (B6). The simulator owns simulated
// backends (listeners, latency, failure behavior); the LB owns routing.
// Health/latency/failure knobs are deliberately NOT pushed to the LB:
// health is OBSERVED by the LB's own probes against the simulated listeners,
// and latency/failure behavior lives in the environment. Pushing "pretend
// unhealthy" flags would fake the exact feedback loop the demo showcases.
type RemoteLoadBalancerAdapter struct {
	cfg    RemoteLBConfig
	client *http.Client

	mu        sync.RWMutex
	name      string                   // last applied scenario name
	strategy  string                   // last applied scenario strategy
	applied   map[string]BackendConfig // backend id -> last applied desired state
	baseline  remoteLBCounters         // counters snapshot at scenario start (deltas)
	lastSnap  LoadBalancerSnapshot
	lastFetch time.Time
}

type remoteLBCounters struct {
	Total     int64   `json:"total"`
	Success   int64   `json:"success"`
	Gateway   int64   `json:"gateway_failures"`
	Route     int64   `json:"route_failures"`
	LatencyMS float64 `json:"average_latency_ms"`
}

type remoteMetricsResponse struct {
	Requests          remoteLBCounters `json:"requests"`
	HealthyBackends   int              `json:"healthy_backends"`
	TotalBackends     int              `json:"total_backends"`
	PoolCount         int              `json:"pool_count"`
	ActiveConnections int              `json:"active_connections"`
}

// NewRemoteLoadBalancerAdapter constructs the adapter; Start performs the
// first reachability check.
func NewRemoteLoadBalancerAdapter(cfg RemoteLBConfig) (*RemoteLoadBalancerAdapter, error) {
	if strings.TrimSpace(cfg.ControlURL) == "" || strings.TrimSpace(cfg.DataURL) == "" {
		return nil, fmt.Errorf("remote load balancer requires both control and data URLs")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = remoteLBTimeout
	}
	return &RemoteLoadBalancerAdapter{
		cfg:      cfg,
		client:   &http.Client{Timeout: timeout},
		strategy: routing.StrategyRoundRobin,
		applied:  make(map[string]BackendConfig),
	}, nil
}

// NewRemoteAdapterFactory wires the remote adapter into the harness options.
func NewRemoteAdapterFactory(cfg RemoteLBConfig) AdapterFactory {
	return func() (LoadBalancerAdapter, error) {
		return NewRemoteLoadBalancerAdapter(cfg)
	}
}

func (a *RemoteLoadBalancerAdapter) Start() error {
	return a.getJSON("/healthz", nil)
}

func (a *RemoteLoadBalancerAdapter) Stop() error {
	// The LB service outlives the simulator session (scale-to-zero is the
	// EMPTY scenario, not a stopped process); nothing to tear down here.
	return nil
}

func (a *RemoteLoadBalancerAdapter) Endpoint() string { return a.cfg.DataURL }

// ConfigureScenario reconciles the LB toward the scenario's desired backend
// set through the control-plane API: create missing pools/members, update
// weights, delete what is gone, apply the strategy to every pool.
func (a *RemoteLoadBalancerAdapter) ConfigureScenario(scenario ScenarioConfig) error {
	target := make(map[string]BackendConfig, len(scenario.Backends))
	pools := make(map[string]string) // simulator pool -> lb pool id
	for _, b := range scenario.Backends {
		lbID := lbToken(b.ID)
		lbPool := lbToken(b.Pool)
		if lbPool == "" {
			lbPool = remoteLBPoolName
		}
		pools[b.Pool] = lbPool
		target[lbID] = b
	}

	strategy := strings.TrimSpace(scenario.Strategy)
	if strategy == "" {
		strategy = routing.StrategyRoundRobin
	}

	// Ensure pools exist first (409 = already present is success).
	seen := map[string]bool{}
	var errs []error
	for _, lbPool := range pools {
		if seen[lbPool] {
			continue
		}
		seen[lbPool] = true
		if err := a.ensurePool(lbPool); err != nil {
			errs = append(errs, err)
		}
	}

	a.mu.Lock()
	a.name = strings.TrimSpace(scenario.Name)
	previous := a.applied
	next := make(map[string]BackendConfig, len(target))
	for id, b := range target {
		next[id] = b
	}
	a.applied = next
	a.strategy = strategy
	a.baseline = remoteLBCounters{} // re-baselined on next successful metrics fetch
	a.mu.Unlock()

	for id, b := range target {
		lbPool := pools[b.Pool]
		if lbPool == "" {
			lbPool = remoteLBPoolName
		}
		old, existed := previous[id]
		host, port := backendHostPort(b)
		sameEndpoint := existed && old.Address == b.Address && old.Port == b.Port && old.Pool == b.Pool
		switch {
		case !existed:
			if err := a.postJSON("/backend", map[string]any{
				"backend_id": id, "pool": lbPool, "address": host, "port": port,
				"weight": max1(b.Weight),
			}); err != nil {
				errs = append(errs, err)
			}
		case sameEndpoint && old.Weight != b.Weight:
			// Weight-only change: in-place update, no drain/recreate.
			if err := a.putJSON("/backend/"+id+"/weight", map[string]any{
				"weight": max1(b.Weight),
			}); err != nil {
				errs = append(errs, err)
			}
		case existed:
			// Membership/endpoint change requires recreate (drain-then-create
			// keeps in-flight traffic correct via the LB's drain semantics).
			_ = a.deleteBackend(id)
			if err := a.postJSON("/backend", map[string]any{
				"backend_id": id, "pool": lbPool, "address": host, "port": port,
				"weight": max1(b.Weight),
			}); err != nil {
				errs = append(errs, err)
			}
		}
	}

	for id := range previous {
		if _, keep := target[id]; keep {
			continue
		}
		if err := a.deleteBackend(id); err != nil {
			errs = append(errs, err)
		}
	}

	// Strategy applies to every pool in the scenario.
	for _, lbPool := range dedupeStrings(pools) {
		if err := a.putJSON("/config/algorithm", map[string]any{
			"pool": lbPool, "algorithm": strategy,
		}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (a *RemoteLoadBalancerAdapter) SetStrategy(strategy string) error {
	strategy = strings.TrimSpace(strategy)
	if strategy == "" {
		strategy = routing.StrategyRoundRobin
	}
	a.mu.Lock()
	a.strategy = strategy
	a.mu.Unlock()

	// Apply to all pools discovered via /config.
	cfg, err := a.fetchConfigPoolIDs()
	if err != nil {
		return err
	}
	var errs []error
	for _, poolID := range cfg {
		if err := a.putJSON("/config/algorithm", map[string]any{
			"pool": poolID, "algorithm": strategy,
		}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (a *RemoteLoadBalancerAdapter) SetBackendPool(backendID, pool string) error {
	a.mu.Lock()
	b, ok := a.applied[lbToken(backendID)]
	if ok {
		b.Pool = pool
	}
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("backend %q not registered with load balancer", backendID)
	}
	return a.recreateBackend(backendID, b)
}

func (a *RemoteLoadBalancerAdapter) SetBackendWeight(backendID string, weight int) error {
	if err := a.putJSON("/backend/"+lbToken(backendID)+"/weight", map[string]any{
		"weight": max1(weight),
	}); err != nil {
		return err
	}
	a.mu.Lock()
	if b, ok := a.applied[lbToken(backendID)]; ok {
		b.Weight = weight
		a.applied[lbToken(backendID)] = b
	}
	a.mu.Unlock()
	return nil
}

// SetBackendHealth is intentionally a no-op against a remote LB (see type
// doc): the LB's probes observe the simulated listener's /health response.
func (a *RemoteLoadBalancerAdapter) SetBackendHealth(string, bool) error { return nil }

// SetBackendLatency and SetBackendFailureRate are environment-side too.
func (a *RemoteLoadBalancerAdapter) SetBackendLatency(string, time.Duration) error { return nil }

func (a *RemoteLoadBalancerAdapter) SetBackendFailureRate(string, float64) error { return nil }

func (a *RemoteLoadBalancerAdapter) RemoveBackend(backendID string) error {
	id := lbToken(backendID)
	a.mu.Lock()
	delete(a.applied, id)
	a.mu.Unlock()
	return a.deleteBackend(id)
}

// Snapshot observes the real service: /metrics counters (delta against the
// scenario baseline so per-scenario numbers read cleanly) plus /config for
// authoritative strategy. On LB errors, the last good observation is
// returned (dashboard should keep showing the most recent reality).
func (a *RemoteLoadBalancerAdapter) Snapshot() LoadBalancerSnapshot {
	a.mu.RLock()
	last := a.lastSnap
	fresh := time.Since(a.lastFetch) < remoteLBSnapshotTTL
	a.mu.RUnlock()
	if fresh {
		return last
	}

	metrics, err := a.fetchMetrics()
	if err != nil {
		return last
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.baseline.Total == 0 && a.baseline.Success == 0 && a.baseline.Gateway == 0 && a.baseline.Route == 0 && a.baseline.LatencyMS == 0 {
		a.baseline = metrics.Requests // first observation after (re)baseline
	}
	delta := func(cur, base int64) int64 {
		if cur < base { // LB restarted: counters reset
			a.baseline = metrics.Requests
			return 0
		}
		return cur - base
	}
	total := delta(metrics.Requests.Total, a.baseline.Total)
	success := delta(metrics.Requests.Success, a.baseline.Success)
	failed := delta(metrics.Requests.Gateway+metrics.Requests.Route, a.baseline.Gateway+a.baseline.Route)

	snap := LoadBalancerSnapshot{
		Name: a.nameLocked(), Strategy: a.strategy,
		PoolCount: metrics.PoolCount, BackendCount: metrics.TotalBackends,
		HealthyBackends: metrics.HealthyBackends,
		RequestCount:    total, SuccessfulRequests: success, FailedRequests: failed,
		AverageLatencyMS:    int64(metrics.Requests.LatencyMS),
		ObservedAt:          time.Now(),
		AvailableStrategies: availableStrategies(),
	}
	a.lastSnap = snap
	a.lastFetch = time.Now()
	return snap
}

// nameLocked reports the applied scenario name; an empty applied set after
// a scenario transition reads as the idle state.
func (a *RemoteLoadBalancerAdapter) nameLocked() string {
	if a.name == "" {
		return idleScenarioName
	}
	return a.name
}

// recreateBackend delete-then-create through the API (drain semantics are
// the LB's job and happen inside DELETE).
func (a *RemoteLoadBalancerAdapter) recreateBackend(backendID string, b BackendConfig) error {
	host, port := backendHostPort(b)
	poolID := lbToken(b.Pool)
	if poolID == "" {
		poolID = remoteLBPoolName
	}
	if err := a.ensurePool(poolID); err != nil {
		return err
	}
	if err := a.deleteBackend(lbToken(backendID)); err != nil && !errors.Is(err, errRemoteNotFound) {
		return err
	}
	return a.postJSON("/backend", map[string]any{
		"backend_id": lbToken(backendID), "pool": poolID,
		"address": host, "port": port, "weight": max1(b.Weight),
	})
}

// ---- HTTP plumbing -------------------------------------------------------

var errRemoteNotFound = errors.New("remote lb not found")

func (a *RemoteLoadBalancerAdapter) ensurePool(poolID string) error {
	err := a.postJSON("/pool", map[string]any{"pool_id": poolID})
	if err != nil && strings.Contains(err.Error(), "409") {
		return nil // exists
	}
	return err
}

func (a *RemoteLoadBalancerAdapter) deleteBackend(id string) error {
	return a.do("DELETE", "/backend/"+id, nil, false)
}

func (a *RemoteLoadBalancerAdapter) postJSON(path string, body any) error {
	return a.do(http.MethodPost, path, body, true)
}

func (a *RemoteLoadBalancerAdapter) putJSON(path string, body any) error {
	return a.do(http.MethodPut, path, body, true)
}

func (a *RemoteLoadBalancerAdapter) do(method, path string, body any, expectResponse bool) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, a.cfg.ControlURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s %s: %v", ErrRemoteLBUnreachable, method, path, err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode == http.StatusNotFound {
		return errRemoteNotFound
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("remote lb %s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	if expectResponse && strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		var discard map[string]any
		return json.Unmarshal(payload, &discard)
	}
	return nil
}

func (a *RemoteLoadBalancerAdapter) getJSON(path string, out any) error {
	resp, err := a.client.Get(a.cfg.ControlURL + path)
	if err != nil {
		return fmt.Errorf("%w: GET %s: %v", ErrRemoteLBUnreachable, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("remote lb GET %s: status %d", path, resp.StatusCode)
	}
	if out == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return err
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

func (a *RemoteLoadBalancerAdapter) fetchMetrics() (remoteMetricsResponse, error) {
	var m remoteMetricsResponse
	err := a.getJSON("/metrics", &m)
	return m, err
}

// fetchConfigPoolIDs reads pool ids from GET /config. The real envelope is
// {"config": {"pools": [{"id": ...}]}} (config.PoolConfig json keys), NOT a
// flat pool list -- matching the production shape here is the point.
func (a *RemoteLoadBalancerAdapter) fetchConfigPoolIDs() ([]string, error) {
	var resp struct {
		Config struct {
			Pools []struct {
				ID string `json:"id"`
			} `json:"pools"`
		} `json:"config"`
	}
	if err := a.getJSON("/config", &resp); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(resp.Config.Pools))
	for _, p := range resp.Config.Pools {
		out = append(out, p.ID)
	}
	return out, nil
}

// ---- helpers --------------------------------------------------------------

var lbTokenPattern = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// lbToken maps simulator display names to control-plane-safe ids (the LB's
// idPattern); deterministic so repeated scenarios reconcile against the
// same identities.
func lbToken(name string) string {
	t := lbTokenPattern.ReplaceAllString(strings.TrimSpace(strings.ToLower(name)), "-")
	t = strings.Trim(t, "-.")
	if t == "" {
		return remoteLBPoolName
	}
	return t
}

// backendHostPort normalizes a scenario backend into control-plane
// address/port fields (scheme prefix tolerated; port field authoritative
// when the address omits one).
func backendHostPort(b BackendConfig) (string, int) {
	addr := strings.TrimSpace(b.Address)
	if i := strings.Index(addr, "://"); i >= 0 {
		addr = addr[i+3:]
	}
	if i := strings.LastIndex(addr, ":"); i >= 0 && !strings.Contains(addr[i:], "]") {
		if p := strings.TrimSpace(addr[i+1:]); p != "" {
			var port int
			if _, err := fmt.Sscanf(p, "%d", &port); err == nil && port > 0 {
				return strings.TrimSpace(addr[:i]), port
			}
		}
	}
	return addr, b.Port
}

func max1(v int) int {
	if v < 1 {
		return 1
	}
	return v
}

func dedupeStrings(m map[string]string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(m))
	for _, v := range m {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
