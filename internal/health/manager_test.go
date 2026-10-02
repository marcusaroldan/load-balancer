package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marcusaroldan/load-balancer/internal/config"
	"github.com/marcusaroldan/load-balancer/internal/registry"
)

func managerRegistry(t *testing.T, backends []config.BackendConfig, policy config.HealthPolicy) *registry.Registry {
	t.Helper()
	poolCfg := config.PoolConfig{ID: "web", Algorithm: config.StrategyRoundRobin, HealthPolicy: policy}
	reg := registry.New()
	if err := reg.Load(config.Config{Pools: []config.PoolConfig{poolCfg}, Backends: backends}); err != nil {
		t.Fatalf("registry Load: %v", err)
	}
	return reg
}

func liveBackend(t *testing.T, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
}

func TestManagerProbeClassifiesOutcomes(t *testing.T) {
	ok := liveBackend(t, nil)
	defer ok.Close()

	policy := config.HealthPolicy{Path: "/health", TimeoutSeconds: 1}
	m := NewManager(registry.New(), time.Minute, nil)

	cases := []struct {
		name    string
		address string
		want    bool
	}{
		{"200 is healthy", ok.URL, true},
		{"unreachable transport error is failure", "http://127.0.0.1:1", false},
		{"empty address fails cleanly", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := m.Probe(context.Background(), tc.address, policy)
			if got.Healthy != tc.want {
				t.Fatalf("healthy=%v want %v", got.Healthy, tc.want)
			}
		})
	}
}

func TestManagerProbeTimeoutCountsAsFailure(t *testing.T) {
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer stalled.Close()

	m := NewManager(registry.New(), time.Minute, nil)
	policy := config.HealthPolicy{Path: "/health", TimeoutSeconds: 1}

	// Parent context shorter than the server stall proves timeout classification
	// at second granularity (policy timeout alone would be 1s > 300ms stall).
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if res := m.Probe(ctx, stalled.URL, policy); res.Healthy {
		t.Fatal("stalled probe must classify as failure when parent deadline fires")
	}

	// Default-timeout path succeeds against the same server within 300ms.
	def := config.HealthPolicy{Path: "/health"}
	if res := m.Probe(context.Background(), stalled.URL, def); !res.Healthy {
		t.Fatal("300ms stall under default 2s timeout must be healthy")
	}
}

func TestManagerNon2xxAnd5xxAreFailures(t *testing.T) {
	var codes = map[string]int{"notfound": http.StatusNotFound, "boom": http.StatusInternalServerError}
	servers := map[string]*httptest.Server{}
	for name, code := range codes {
		c := code
		servers[name] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c)
		}))
		defer servers[name].Close()
	}
	m := NewManager(registry.New(), time.Minute, nil)
	policy := config.HealthPolicy{Path: "/", TimeoutSeconds: 1}
	for name, srv := range servers {
		if res := m.Probe(context.Background(), srv.URL, policy); res.Healthy {
			t.Fatalf("%s: status %d must be a failed probe", name, codes[name])
		}
	}
}

func TestManagerSweepDrivesThresholdTransitions(t *testing.T) {
	var alive atomic.Bool
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if alive.Load() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	alive.Store(true)

	policy := config.HealthPolicy{
		Path: "/health", IntervalSeconds: 5, TimeoutSeconds: 1,
		HealthyThreshold: 2, UnhealthyThreshold: 3,
	}
	bc := config.BackendConfig{ID: "b1", PoolID: "web", Address: server.URL, Enabled: true}
	reg := managerRegistry(t, []config.BackendConfig{bc}, policy)
	m := NewManager(reg, time.Minute, nil)

	ctx := context.Background()

	// Flip unhealthy only after 3 consecutive failed sweeps.
	alive.Store(false)
	for i := 0; i < 2; i++ {
		m.SweepOnce(ctx)
		if h, _ := reg.BackendHealth("web", "b1"); !h {
			t.Fatalf("backend went unhealthy after only %d failures", i+1)
		}
	}
	m.SweepOnce(ctx)
	if h, _ := reg.BackendHealth("web", "b1"); h {
		t.Fatal("backend still healthy after reaching unhealthy threshold")
	}

	// Recovery requires 2 consecutive successes.
	alive.Store(true)
	m.SweepOnce(ctx)
	if h, _ := reg.BackendHealth("web", "b1"); h {
		t.Fatal("backend recovered after a single success")
	}
	m.SweepOnce(ctx)
	if h, _ := reg.BackendHealth("web", "b1"); !h {
		t.Fatal("backend did not recover at healthy threshold")
	}

	transitions := m.Transitions()
	if len(transitions) != 2 {
		t.Fatalf("want exactly 2 recorded transitions, got %d: %+v", len(transitions), transitions)
	}
	if transitions[0].Healthy || !transitions[1].Healthy {
		t.Fatalf("transition flags wrong: want unhealthy-then-healthy, got %+v", transitions)
	}
}

func TestManagerRemovalDuringCheckIsTolerated(t *testing.T) {
	server := liveBackend(t, nil)
	defer server.Close()

	policy := config.HealthPolicy{Path: "/health", TimeoutSeconds: 1}
	bc := config.BackendConfig{ID: "b1", PoolID: "web", Address: server.URL, Enabled: true}
	reg := managerRegistry(t, []config.BackendConfig{bc}, policy)
	m := NewManager(reg, time.Minute, nil)

	// Snapshot taken before removal; sweep applies results afterward.
	snapBefore := reg.Snapshot()
	_ = snapBefore
	if err := reg.RemoveBackend("web", "b1"); err != nil {
		t.Fatalf("RemoveBackend: %v", err)
	}
	m.SweepOnce(context.Background()) // must not panic or error out the loop
	if len(m.Transitions()) != 0 {
		t.Fatal("removed backend must not record transitions")
	}
}

func TestManagerStartStopRestartRaceFree(t *testing.T) {
	var hits atomic.Int64
	server := liveBackend(t, &hits)
	defer server.Close()

	policy := config.HealthPolicy{Path: "/health", TimeoutSeconds: 1}
	bc := config.BackendConfig{ID: "b1", PoolID: "web", Address: server.URL, Enabled: true}
	reg := managerRegistry(t, []config.BackendConfig{bc}, policy)
	m := NewManager(reg, 10*time.Millisecond, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for round := 0; round < 3; round++ {
		if err := m.Start(ctx); err != nil {
			t.Fatalf("start round %d: %v", round, err)
		}
		if err := m.Start(ctx); err != nil { // double start is a no-op
			t.Fatalf("double start round %d: %v", round, err)
		}
		time.Sleep(35 * time.Millisecond)
		if err := m.Stop(); err != nil {
			t.Fatalf("stop round %d: %v", round, err)
		}
		if m.Running() {
			t.Fatalf("running after stop round %d", round)
		}
	}
	if err := m.Stop(); err == nil {
		t.Fatal("stop on idle manager must report ErrNotRunning")
	}
	if hits.Load() == 0 {
		t.Fatal("no probes observed across lifecycle rounds")
	}
}

func TestManagerContextCancellationStopsLoop(t *testing.T) {
	var hits atomic.Int64
	server := liveBackend(t, &hits)
	defer server.Close()

	policy := config.HealthPolicy{Path: "/health", TimeoutSeconds: 1}
	bc := config.BackendConfig{ID: "b1", PoolID: "web", Address: server.URL, Enabled: true}
	reg := managerRegistry(t, []config.BackendConfig{bc}, policy)
	m := NewManager(reg, 10*time.Millisecond, nil)

	ctx, cancel := context.WithCancel(context.Background())
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	cancel()
	time.Sleep(30 * time.Millisecond)
	before := hits.Load()
	time.Sleep(50 * time.Millisecond)
	if after := hits.Load(); after != before {
		t.Fatalf("loop kept probing after ctx cancel: %d -> %d", before, after)
	}
	_ = m.Stop()
}

func TestJoinProbeURL(t *testing.T) {
	cases := []struct{ base, path, want string }{
		{"http://10.0.0.1:80", "/health", "http://10.0.0.1:80/health"},
		{"http://10.0.0.1:80/", "/health", "http://10.0.0.1:80/health"},
		{"http://10.0.0.1:80/base", "/health", "http://10.0.0.1:80/base/health"},
	}
	for _, tc := range cases {
		got, err := joinProbeURL(tc.base, tc.path)
		if err != nil {
			t.Fatalf("joinProbeURL(%q): %v", tc.base, err)
		}
		if got != tc.want {
			t.Errorf("joinProbeURL(%q,%q)=%q want %q", tc.base, tc.path, got, tc.want)
		}
	}
	if _, err := joinProbeURL("://bad", "/x"); err == nil {
		t.Error("malformed address must error")
	}
	if got, err := joinProbeURL("http://h", ""); err != nil || !strings.HasSuffix(got, defaultHealthCheckPath) {
		t.Errorf("empty path should use default: %q %v", got, err)
	}
}
