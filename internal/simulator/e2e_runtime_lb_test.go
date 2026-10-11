package simulator

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/marcusaroldan/load-balancer/internal/registry"
	"github.com/marcusaroldan/load-balancer/internal/runtime"
)

// TestEndToEndHarnessThroughRuntimeLB is the B6 proof in one process pair:
// a REAL load-balancer service (runtime: registry + health + proxy + control
// plane on live loopback listeners) driven by the simulator harness through
// the public HTTP APIs only -- exactly the VM compose topology, with the
// loopback interface standing in for the private network.
func TestEndToEndHarnessThroughRuntimeLB(t *testing.T) {
	reg := registry.New()
	rt := runtime.New(runtime.Config{
		DataAddr:        "127.0.0.1:0",
		ControlAddr:     "127.0.0.1:0",
		HealthInterval:  100 * time.Millisecond,
		ShutdownTimeout: 2 * time.Second,
	}, reg)
	ctx, cancel := context.WithCancel(context.Background())
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		sctx, scancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer scancel()
		_ = rt.Shutdown(sctx)
		cancel()
	}()

	harness, err := NewHarness(PresetScenarioConfigs["stable"], WithAdapterFactory(
		NewRemoteAdapterFactory(RemoteLBConfig{
			ControlURL: "http://" + rt.ControlPlaneAddr(),
			DataURL:    "http://" + rt.DataPlaneAddr(),
		}),
	))
	if err != nil {
		t.Fatalf("harness with remote LB: %v", err)
	}
	defer harness.Stop()

	// Desired state must land in the LB registry with the advertised
	// (loopback here) addresses the LB can actually dial.
	deadline := time.Now().Add(2 * time.Second)
	for {
		cfg := reg.Config()
		if len(cfg.Backends) >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("backends never registered with LB: %+v", cfg.Backends)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Synthetic traffic must produce successful outcomes observable through
	// the public metrics path (the snapshot polls LB /metrics).
	deadline = time.Now().Add(5 * time.Second)
	var snap LoadBalancerSnapshot
	for {
		snap = harness.loadBalancer.Snapshot()
		if snap.RequestCount > 0 && snap.SuccessfulRequests > 0 && snap.FailedRequests == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no successful LB-routed traffic observed: %+v", snap)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if snap.HealthyBackends == 0 {
		t.Fatalf("LB never reported healthy backends: %+v", snap)
	}

	// Strategy switch through the UI path applies in the real service.
	if err := harness.SetStrategy("least_connections"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		found := false
		for _, pool := range reg.Config().Pools {
			if pool.Algorithm == "least_connections" {
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("strategy not applied to LB pools: %+v", reg.Config().Pools)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Scale to zero: idle removes everything from the live service.
	if err := harness.EnterIdleMode(); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		if len(reg.Config().Backends) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("idle did not deregister LB backends: %+v", reg.Config().Backends)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// With no members left, the real service answers 503 (nothing to route)
	// while staying up and serving its control plane.
	idleResp, err := http.Get("http://" + rt.DataPlaneAddr() + "/")
	if err != nil {
		t.Fatalf("data plane should stay reachable: %v", err)
	}
	defer idleResp.Body.Close()
	if idleResp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("idle data plane: want 503, got %d", idleResp.StatusCode)
	}
	healthzResp, err := http.Get("http://" + rt.ControlPlaneAddr() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer healthzResp.Body.Close()
	if healthzResp.StatusCode != http.StatusOK {
		t.Fatalf("service must stay alive at zero: %d", healthzResp.StatusCode)
	}
}
