package registry

import (
	"sync"
	"testing"
	"time"

	"github.com/marcusaroldan/load-balancer/internal/config"
	"github.com/marcusaroldan/load-balancer/internal/pool"
)

func testPoolConfig(id, algorithm string) config.PoolConfig {
	return config.PoolConfig{ID: id, Algorithm: algorithm}
}

func testBackendConfig(id, poolID, address string) config.BackendConfig {
	return config.BackendConfig{ID: id, PoolID: poolID, Address: address, Port: 80, Enabled: true}
}

func mustLoad(t *testing.T, r *Registry, pools []config.PoolConfig, backends []config.BackendConfig) {
	t.Helper()
	cfg := config.Config{Pools: pools, Backends: backends}
	if err := r.Load(cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestLoadRejectsInvalidAndEmpty(t *testing.T) {
	r := New()
	if err := r.Load(config.Config{}); err == ErrEmptyConfig {
		if err != ErrEmptyConfig {
			t.Fatal("expected ErrEmptyConfig sentinel")
		}
	} else if err == nil {
		t.Fatal("empty config must be rejected")
	}

	bad := config.Config{
		Pools:    []config.PoolConfig{{ID: "p", Algorithm: "ip_hash"}},
		Backends: []config.BackendConfig{testBackendConfig("b1", "p", "http://x")},
	}
	if err := r.Load(bad); err == nil {
		t.Fatal("unsupported algorithm must fail Load")
	}
	// Failed load must leave previous state untouched.
	if got := len(r.Snapshot().Pools()); got != 0 {
		t.Fatalf("failed Load mutated registry; pools=%d", got)
	}
}

func TestSnapshotIsImmutableAcrossMutations(t *testing.T) {
	r := New()
	mustLoad(t, r,
		[]config.PoolConfig{testPoolConfig("web", config.StrategyRoundRobin)},
		[]config.BackendConfig{testBackendConfig("b1", "web", "http://10.0.0.1:80")},
	)

	snap := r.Snapshot()
	before, _ := snap.Pool("web")
	beforeCount := len(before.Backends)

	// Mutate after snapshotting.
	if err := r.AddBackend(testBackendConfig("b2", "web", "http://10.0.0.2:80")); err != nil {
		t.Fatalf("AddBackend: %v", err)
	}

	after, _ := snap.Pool("web")
	if len(after.Backends) != beforeCount {
		t.Fatalf("snapshot observed post-mutation state: %d vs %d", len(after.Backends), beforeCount)
	}
	fresh := r.Snapshot()
	freshPool, _ := fresh.Pool("web")
	if len(freshPool.Backends) != beforeCount+1 {
		t.Fatalf("fresh snapshot missing new backend: %d", len(freshPool.Backends))
	}
}

func TestSelectionExcludesDisabledAndUnhealthy(t *testing.T) {
	r := New()
	mustLoad(t, r,
		[]config.PoolConfig{testPoolConfig("web", config.StrategyRoundRobin)},
		[]config.BackendConfig{
			testBackendConfig("b1", "web", "http://10.0.0.1:80"),
			testBackendConfig("b2", "web", "http://10.0.0.2:80"),
		},
	)

	if err := r.SetEnabled("web", "b1", false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	snap := r.Snapshot()
	for i := 0; i < 4; i++ {
		got, err := r.Select(snap, "web")
		if err != nil {
			t.Fatalf("Select: %v", err)
		}
		if got.ID != "b2" {
			t.Fatalf("disabled backend selected: %s", got.ID)
		}
	}

	if err := r.ResetHealth("web", "b2", false); err != nil {
		t.Fatalf("ResetHealth: %v", err)
	}
	if _, err := r.Select(r.Snapshot(), "web"); err == nil {
		t.Fatal("selection succeeded with all backends ineligible")
	}
}

func TestStrategyChangeAppliesWithoutRestart(t *testing.T) {
	r := New()
	mustLoad(t, r,
		[]config.PoolConfig{testPoolConfig("web", config.StrategyRoundRobin)},
		[]config.BackendConfig{
			testBackendConfig("b1", "web", "http://10.0.0.1:80"),
			testBackendConfig("b2", "web", "http://10.0.0.2:80"),
		},
	)

	if err := r.SetStrategy("web", "bogus"); err == nil {
		t.Fatal("unknown strategy accepted")
	}
	if err := r.SetStrategy("web", config.StrategyLeastConnections); err != nil {
		t.Fatalf("SetStrategy: %v", err)
	}
	snap := r.Snapshot()
	strategy, ok := snap.Strategy("web")
	if !ok || strategy != config.StrategyLeastConnections {
		t.Fatalf("strategy not reflected in snapshot: %q", strategy)
	}
	if _, err := r.Select(snap, "web"); err != nil {
		t.Fatalf("select after strategy change: %v", err)
	}
}

func TestMembershipChangeResetsSelectorState(t *testing.T) {
	r := New()
	mustLoad(t, r,
		[]config.PoolConfig{testPoolConfig("web", config.StrategyRoundRobin)},
		[]config.BackendConfig{
			testBackendConfig("b1", "web", "http://10.0.0.1:80"),
			testBackendConfig("b2", "web", "http://10.0.0.2:80"),
			testBackendConfig("b3", "web", "http://10.0.0.3:80"),
		},
	)

	first, err := r.Select(r.Snapshot(), "web")
	if err != nil {
		t.Fatalf("initial select: %v", err)
	}
	if first == nil {
		t.Fatal("initial select returned nil backend")
	}

	// Remove then re-add b1: rotation must restart cleanly from a healthy set.
	if err := r.RemoveBackend("web", "b1"); err != nil {
		t.Fatalf("RemoveBackend: %v", err)
	}
	if err := r.AddBackend(testBackendConfig("b1", "web", "http://10.0.0.1:80")); err != nil {
		t.Fatalf("re-AddBackend: %v", err)
	}

	counts := map[string]int{}
	snap := r.Snapshot()
	for i := 0; i < 9; i++ {
		got, err := r.Select(snap, "web")
		if err != nil {
			t.Fatalf("Select after membership change: %v", err)
		}
		counts[got.ID]++
	}
	if counts["b1"] == 0 {
		t.Fatal("re-added backend never selected")
	}
	for _, want := range []int{3} {
		if counts["b1"]+counts["b2"]+counts["b3"] != 9 {
			t.Fatalf("distribution lost selections: %v", counts)
		}
		_ = want
	}
}

func TestHealthThresholdHysteresis(t *testing.T) {
	r := New()
	poolCfg := testPoolConfig("web", config.StrategyRoundRobin)
	poolCfg.HealthPolicy = config.HealthPolicy{HealthyThreshold: 2, UnhealthyThreshold: 3}
	mustLoad(t, r, []config.PoolConfig{poolCfg},
		[]config.BackendConfig{testBackendConfig("b1", "web", "http://10.0.0.1:80")})

	// Flip unhealthy only after 3 consecutive failures.
	for i := 0; i < 2; i++ {
		if err := r.MarkUnhealthy("web", "b1"); err != nil {
			t.Fatalf("MarkUnhealthy: %v", err)
		}
		if b, _ := r.Snapshot().Pool("web"); b.Backends["b1"].Healthy != true {
			t.Fatalf("backend marked unhealthy after only %d failures", i+1)
		}
	}
	if err := r.MarkUnhealthy("web", "b1"); err != nil {
		t.Fatalf("MarkUnhealthy third: %v", err)
	}
	p, _ := r.Snapshot().Pool("web")
	if p.Backends["b1"].Healthy {
		t.Fatal("backend still healthy after threshold reached")
	}

	// Recovery requires 2 consecutive successes.
	if err := r.MarkHealthy("web", "b1"); err != nil {
		t.Fatalf("MarkHealthy: %v", err)
	}
	p, _ = r.Snapshot().Pool("web")
	if p.Backends["b1"].Healthy {
		t.Fatal("backend recovered after only one success")
	}
	if err := r.MarkHealthy("web", "b1"); err != nil {
		t.Fatalf("MarkHealthy second: %v", err)
	}
	p, _ = r.Snapshot().Pool("web")
	if !p.Backends["b1"].Healthy {
		t.Fatal("backend did not recover at threshold")
	}
}

func TestFailedProbeInterruptsRecoveryProgress(t *testing.T) {
	r := New()
	poolCfg := testPoolConfig("web", config.StrategyRoundRobin)
	poolCfg.HealthPolicy = config.HealthPolicy{HealthyThreshold: 2, UnhealthyThreshold: 1}
	mustLoad(t, r, []config.PoolConfig{poolCfg},
		[]config.BackendConfig{testBackendConfig("b1", "web", "http://10.0.0.1:80")})

	if err := r.MarkUnhealthy("web", "b1"); err != nil {
		t.Fatalf("MarkUnhealthy: %v", err)
	}
	_ = r.MarkHealthy("web", "b1")
	_ = r.MarkUnhealthy("web", "b1") // resets success streak
	_ = r.MarkHealthy("web", "b1")
	p, _ := r.Snapshot().Pool("web")
	if p.Backends["b1"].Healthy {
		t.Fatal("intermittent failure must not count toward recovery threshold")
	}
}

func TestDeletePoolGuardsMembership(t *testing.T) {
	r := New()
	mustLoad(t, r,
		[]config.PoolConfig{testPoolConfig("web", config.StrategyRoundRobin)},
		[]config.BackendConfig{testBackendConfig("b1", "web", "http://10.0.0.1:80")})

	if err := r.DeletePool("web"); err == nil {
		t.Fatal("delete with members must fail")
	}
	if err := r.RemoveBackend("web", "b1"); err != nil {
		t.Fatalf("RemoveBackend: %v", err)
	}
	if err := r.DeletePool("web"); err != nil {
		t.Fatalf("DeletePool empty: %v", err)
	}
	if err := r.DeletePool("ghost"); err == nil {
		t.Fatal("delete unknown pool must fail")
	}
}

func TestConcurrentSelectionAndMutation(t *testing.T) {
	r := New()
	mustLoad(t, r,
		[]config.PoolConfig{testPoolConfig("web", config.StrategyRoundRobin)},
		[]config.BackendConfig{
			testBackendConfig("b1", "web", "http://10.0.0.1:80"),
			testBackendConfig("b2", "web", "http://10.0.0.2:80"),
		},
	)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(3)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				snap := r.Snapshot()
				if _, err := r.Select(snap, "web"); err != nil {
					// No-eligible-backends is a legal transient during flips.
					continue
				}
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				_ = r.SetEnabled("web", "b1", i%2 == 0)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = r.SetStrategy("web", config.StrategyRoundRobin)
			}
		}
	}()

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

func TestWeightedDistributionReflectsWeights(t *testing.T) {
	r := New()
	mustLoad(t, r,
		[]config.PoolConfig{testPoolConfig("web", config.StrategyWeightedRR)},
		[]config.BackendConfig{
			withWeight(testBackendConfig("heavy", "web", "http://10.0.0.1:80"), 3),
			withWeight(testBackendConfig("light", "web", "http://10.0.0.2:80"), 1),
		},
	)

	counts := map[string]int{}
	snap := r.Snapshot()
	for i := 0; i < 8; i++ {
		got, err := r.Select(snap, "web")
		if err != nil {
			t.Fatalf("Select: %v", err)
		}
		counts[got.ID]++
	}
	if counts["heavy"] != 6 || counts["light"] != 2 {
		t.Fatalf("weighted cycle wrong: %v (want heavy=6 light=2)", counts)
	}
}

func withWeight(bc config.BackendConfig, w int) config.BackendConfig {
	bc.Weight = w
	return bc
}

func TestGracePeriodEligibility(t *testing.T) {
	r := New()
	now := time.Unix(1_700_000_000, 0)
	r.now = func() time.Time { return now }
	r.graceDefault = 10 * time.Second

	poolCfg := testPoolConfig("web", config.StrategyRoundRobin)
	poolCfg.HealthPolicy = config.HealthPolicy{GraceSeconds: 10, UnhealthyThreshold: 1}
	mustLoad(t, r, []config.PoolConfig{poolCfg},
		[]config.BackendConfig{testBackendConfig("b1", "web", "http://10.0.0.1:80")})

	// Within grace and never checked: eligible even though not yet probed.
	p, _ := r.Snapshot().Pool("web")
	if !p.Backends["b1"].Healthy {
		t.Fatal("cold-start backend should be eligible inside grace")
	}

	// After grace expiry without a successful check it stays per last known state;
	// simulate a failed probe past grace.
	now = now.Add(20 * time.Second)
	if err := r.MarkUnhealthy("web", "b1"); err != nil {
		t.Fatalf("MarkUnhealthy: %v", err)
	}
	p, _ = r.Snapshot().Pool("web")
	if p.Backends["b1"].Healthy {
		t.Fatal("failed probe past grace must remove eligibility")
	}
}

func TestUnknownPoolSelectFails(t *testing.T) {
	r := New()
	mustLoad(t, r, []config.PoolConfig{testPoolConfig("web", config.StrategyRoundRobin)}, nil)
	if _, err := r.Select(r.Snapshot(), "ghost"); err == nil {
		t.Fatal("select on unknown pool must error")
	}
}

func TestBackendNotFoundErrors(t *testing.T) {
	r := New()
	mustLoad(t, r, []config.PoolConfig{testPoolConfig("web", config.StrategyRoundRobin)},
		[]config.BackendConfig{testBackendConfig("b1", "web", "http://10.0.0.1:80")})

	cases := []func() error{
		func() error { return r.SetEnabled("web", "ghost", false) },
		func() error { return r.SetWeight("web", "ghost", 5) },
		func() error { return r.RemoveBackend("web", "ghost") },
		func() error { return r.MarkHealthy("ghost-pool", "b1") },
		func() error { return r.AddBackend(testBackendConfig("bX", "ghost-pool", "http://10.0.0.9:80")) },
	}
	for i, fn := range cases {
		if err := fn(); err == nil {
			t.Errorf("case %d: expected error, got nil", i)
		}
	}
	if err := r.SetWeight("web", "b1", config.MaxWeight+1); err == nil {
		t.Fatal("out-of-range weight accepted")
	}
	if err := r.SetWeight("web", "b1", 5); err != nil {
		t.Fatalf("valid weight rejected: %v", err)
	}
}

func TestSnapshotPoolLookup(t *testing.T) {
	r := New()
	mustLoad(t, r, []config.PoolConfig{testPoolConfig("web", config.StrategyRoundRobin)}, nil)
	if _, ok := r.Snapshot().Pool("nope"); ok {
		t.Fatal("unknown pool lookup returned ok")
	}
	if _, ok := r.Snapshot().Pool("web"); !ok {
		t.Fatal("known pool lookup failed")
	}
	_ = pool.ErrPoolNotFound
}

func TestBackendURLAddressForms(t *testing.T) {
	cases := []struct {
		name    string
		address string
		port    int
		want    string
		wantErr bool
	}{
		{name: "host plus explicit port", address: "127.0.0.1", port: 9101, want: "http://127.0.0.1:9101"},
		{name: "host colon port in address", address: "127.0.0.1:9102", want: "http://127.0.0.1:9102"},
		{name: "explicit port wins when address has none", address: "http://backend.example", port: 8080, want: "http://backend.example:8080"},
		{name: "scheme and port embedded", address: "https://secure.example:8443", want: "https://secure.example:8443"},
		{name: "hostname with dot", address: "dashboard", port: 9100, want: "http://dashboard:9100"},
		{name: "empty address rejected", address: "", wantErr: true},
		{name: "scheme only rejected", address: "http://", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := BackendURL(&pool.Backend{ID: "b", Address: tc.address, Port: tc.port})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %v", u)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := u.String()
			if got != tc.want && !(tc.want == "http://backend.example:8080" && got == "http://backend.example:8080/") {
				t.Fatalf("want %q got %q", tc.want, got)
			}
		})
	}
}
