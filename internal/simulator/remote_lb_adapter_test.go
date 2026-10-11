package simulator

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marcusaroldan/load-balancer/internal/routing"
)

// fakeLB emulates the load balancer's control plane for adapter tests:
// records requests, serves configurable metrics, and applies simple rules.
type fakeLB struct {
	t       *testing.T
	mu      sync.Mutex
	calls   []string // "METHOD /path body"
	state   map[string]map[string]any
	poolIDs map[string]bool

	metricsTotal    atomic.Int64
	metricsSuccess  atomic.Int64
	metricsGateway  atomic.Int64
	metricsRoute    atomic.Int64
	metricsLatency  atomic.Int64
	metricsHealthy  atomic.Int64
	metricsTotalBk  atomic.Int64
	metricsPools    atomic.Int64
	failNextRequest atomic.Bool
}

func newFakeLB(t *testing.T) (*fakeLB, *httptest.Server) {
	t.Helper()
	f := &fakeLB{state: make(map[string]map[string]any), poolIDs: make(map[string]bool)}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	return f, srv
}

func (f *fakeLB) handle(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	bodyJSON := ""
	if body != nil {
		if data, err := json.Marshal(body); err == nil {
			bodyJSON = string(data) // map keys marshal sorted: deterministic fragments
		}
	}
	f.mu.Lock()
	f.calls = append(f.calls, fmt.Sprintf("%s %s %s", r.Method, r.URL.Path, bodyJSON))
	f.mu.Unlock()

	if f.failNextRequest.CompareAndSwap(true, false) {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	switch {
	case r.URL.Path == "/healthz":
		w.WriteHeader(http.StatusOK)
	case r.URL.Path == "/pool" && r.Method == http.MethodPost:
		id, _ := body["pool_id"].(string)
		f.mu.Lock()
		f.poolIDs[id] = true
		f.mu.Unlock()
		if f.duplicate(id) {
			http.Error(w, "duplicate", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusCreated)
	case r.URL.Path == "/backend" && r.Method == http.MethodPost:
		id, _ := body["backend_id"].(string)
		if f.duplicate(id) {
			http.Error(w, "duplicate", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusCreated)
	case strings.HasPrefix(r.URL.Path, "/backend/") && strings.HasSuffix(r.URL.Path, "/weight") && r.Method == http.MethodPut:
		weight, _ := body["weight"].(float64)
		if weight < 1 {
			http.Error(w, "invalid weight", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	case strings.HasPrefix(r.URL.Path, "/backend/") && r.Method == http.MethodDelete:
		id := strings.TrimPrefix(r.URL.Path, "/backend/")
		f.mu.Lock()
		_, existed := f.state[id]
		if id == "ghost" {
			existed = false
		}
		delete(f.state, id)
		f.mu.Unlock()
		if !existed {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	case r.URL.Path == "/config/algorithm" && r.Method == http.MethodPut:
		w.WriteHeader(http.StatusOK)
	case r.URL.Path == "/config":
		f.mu.Lock()
		ids := make([]map[string]any, 0, len(f.poolIDs))
		for id := range f.poolIDs {
			ids = append(ids, map[string]any{"id": id})
		}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"config": map[string]any{"pools": ids},
		})
	case r.URL.Path == "/metrics":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"requests": map[string]any{
				"total": f.metricsTotal.Load(), "success": f.metricsSuccess.Load(),
				"gateway_failures": f.metricsGateway.Load(), "route_failures": f.metricsRoute.Load(),
				"average_latency_ms": f.metricsLatency.Load(),
			},
			"healthy_backends": f.metricsHealthy.Load(),
			"total_backends":   f.metricsTotalBk.Load(),
			"pool_count":       f.metricsPools.Load(),
		})
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusTeapot)
	}
}

func (f *fakeLB) duplicate(id string) bool {
	if id == "" {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, seen := f.state[id]; seen {
		return true
	}
	f.state[id] = map[string]any{}
	return false
}

func (f *fakeLB) reset() {
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
}

func (f *fakeLB) calledWith(method, pathFragment, bodyFragment string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, method+" ") && strings.Contains(c, pathFragment) && strings.Contains(c, bodyFragment) {
			return true
		}
	}
	return false
}

func scenarioWith(backends ...BackendConfig) ScenarioConfig {
	return ScenarioConfig{Name: "test", Strategy: routing.StrategyRoundRobin, Backends: backends}
}

func TestRemoteAdapterRequiresBothURLs(t *testing.T) {
	if _, err := NewRemoteLoadBalancerAdapter(RemoteLBConfig{ControlURL: "http://x"}); err == nil {
		t.Fatal("missing data URL accepted")
	}
	if _, err := NewRemoteLoadBalancerAdapter(RemoteLBConfig{DataURL: "http://x"}); err == nil {
		t.Fatal("missing control URL accepted")
	}
}

func TestRemoteAdapterStartFailsWhenLBDown(t *testing.T) {
	f, srv := newFakeLB(t)
	_ = f
	a, err := NewRemoteLoadBalancerAdapter(RemoteLBConfig{ControlURL: srv.URL, DataURL: "http://data"})
	if err != nil {
		t.Fatal(err)
	}
	srv.Close()
	if err := a.Start(); !errors.Is(err, ErrRemoteLBUnreachable) {
		t.Fatalf("want ErrRemoteLBUnreachable, got %v", err)
	}
}

func TestRemoteAdapterConfigureScenarioCreatesEverything(t *testing.T) {
	f, srv := newFakeLB(t)
	defer srv.Close()
	a := mustAdapter(t, srv.URL)

	err := a.ConfigureScenario(scenarioWith(
		BackendConfig{ID: "b1", Pool: "default", Address: "127.0.0.1", Port: 9101, Weight: 2},
		BackendConfig{ID: "b2", Pool: "default", Address: "127.0.0.1", Port: 9102, Weight: 1},
	))
	if err != nil {
		t.Fatal(err)
	}
	if !f.calledWith("POST", "/pool ", `"pool_id":"default"`) {
		t.Fatalf("pool not ensured: %v", f.calls)
	}
	if !f.calledWith("POST", "/backend ", `"port":9101`) {
		t.Fatalf("backend b1 not registered: %v", f.calls)
	}
	if !f.calledWith("PUT", "/config/algorithm", `"algorithm":"round_robin"`) {
		t.Fatalf("strategy not applied: %v", f.calls)
	}
	if got := a.Endpoint(); got != "http://data" {
		t.Fatalf("endpoint: %q", got)
	}

	// SetStrategy must discover the real pool via /config envelope and PUT.
	f.reset()
	if err := a.SetStrategy("least_connections"); err != nil {
		t.Fatal(err)
	}
	if !f.calledWith("PUT", "/config/algorithm", `"algorithm":"least_connections"`) {
		t.Fatalf("strategy not applied to discovered pool: %v", f.calls)
	}
	if f.calledWith("PUT", "/config/algorithm", `"pool":""`) {
		t.Fatalf("strategy PUT lost the pool id: %v", f.calls)
	}
}

func TestRemoteAdapterReconcilesDiffs(t *testing.T) {
	f, srv := newFakeLB(t)
	defer srv.Close()
	a := mustAdapter(t, srv.URL)

	f.reset()
	err := a.ConfigureScenario(scenarioWith(
		BackendConfig{ID: "b1", Pool: "default", Address: "127.0.0.1", Port: 9101, Weight: 2},
		BackendConfig{ID: "b2", Pool: "default", Address: "127.0.0.1", Port: 9102, Weight: 1},
	))
	if err != nil {
		t.Fatal(err)
	}

	f.reset()
	err = a.ConfigureScenario(scenarioWith(
		BackendConfig{ID: "b2", Pool: "default", Address: "127.0.0.1", Port: 9102, Weight: 7}, // weight bump
		BackendConfig{ID: "b3", Pool: "default", Address: "127.0.0.1", Port: 9103, Weight: 1}, // new
	))
	if err != nil {
		t.Fatal(err)
	}
	if !f.calledWith("DELETE", "/backend/b1", "") {
		t.Fatalf("removed backend not deleted: %v", f.calls)
	}
	if !f.calledWith("PUT", "/backend/b2/weight", `"weight":7`) {
		t.Fatalf("weight change should use in-place PUT, calls: %v", f.calls)
	}
	if f.calledWith("DELETE", "/backend/b2", "") {
		t.Fatalf("weight-only change must not recreate b2: %v", f.calls)
	}
	if !f.calledWith("POST", "/backend ", `"backend_id":"b3"`) {
		t.Fatalf("new backend not created: %v", f.calls)
	}

	// Idle (empty) scenario deletes everything previously applied.
	f.reset()
	if err := a.ConfigureScenario(ScenarioConfig{Name: "idle"}); err != nil {
		t.Fatal(err)
	}
	if !f.calledWith("DELETE", "/backend/b2", "") || !f.calledWith("DELETE", "/backend/b3", "") {
		t.Fatalf("empty scenario must deregister all: %v", f.calls)
	}
}

func TestRemoteAdapterEndpointChangeRecreates(t *testing.T) {
	f, srv := newFakeLB(t)
	defer srv.Close()
	a := mustAdapter(t, srv.URL)
	if err := a.ConfigureScenario(scenarioWith(BackendConfig{ID: "b1", Pool: "default", Address: "127.0.0.1", Port: 9101, Weight: 1})); err != nil {
		t.Fatal(err)
	}
	f.reset()
	if err := a.ConfigureScenario(scenarioWith(BackendConfig{ID: "b1", Pool: "default", Address: "127.0.0.1", Port: 9999, Weight: 1})); err != nil {
		t.Fatal(err)
	}
	if !f.calledWith("DELETE", "/backend/b1", "") || !f.calledWith("POST", "/backend ", `"port":9999`) {
		t.Fatalf("endpoint change should delete+create: %v", f.calls)
	}
}

func TestRemoteAdapterEnvironmentKnobsAreNoOps(t *testing.T) {
	_, srv := newFakeLB(t)
	defer srv.Close()
	a := mustAdapter(t, srv.URL)
	if err := a.SetBackendHealth("b1", false); err != nil {
		t.Fatal(err)
	}
	if err := a.SetBackendLatency("b1", time.Second); err != nil {
		t.Fatal(err)
	}
	if err := a.SetBackendFailureRate("b1", 0.5); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteAdapterSnapshotDeltasAndCache(t *testing.T) {
	f, srv := newFakeLB(t)
	defer srv.Close()
	a := mustAdapter(t, srv.URL)
	if err := a.ConfigureScenario(scenarioWith(BackendConfig{ID: "b1", Pool: "default", Address: "127.0.0.1", Port: 9101})); err != nil {
		t.Fatal(err)
	}

	f.metricsTotal.Store(100)
	f.metricsSuccess.Store(96)
	f.metricsGateway.Store(4)
	f.metricsLatency.Store(12)
	f.metricsHealthy.Store(3)
	f.metricsTotalBk.Store(3)
	f.metricsPools.Store(1)
	s1 := a.Snapshot()
	if s1.RequestCount != 0 {
		t.Fatalf("first observation must baseline to zero delta: %+v", s1)
	}

	time.Sleep(remoteLBSnapshotTTL + 10*time.Millisecond)
	f.metricsTotal.Store(150)
	f.metricsSuccess.Store(144)
	f.metricsGateway.Store(6)
	s2 := a.Snapshot()
	if s2.RequestCount != 50 || s2.SuccessfulRequests != 48 || s2.FailedRequests != 2 {
		t.Fatalf("delta wrong: %+v", s2)
	}
	if s2.BackendCount != 3 || s2.HealthyBackends != 3 || s2.Strategy != routing.StrategyRoundRobin {
		t.Fatalf("gauges wrong: %+v", s2)
	}

	// LB restart: counters roll back below baseline -> clamped + re-baselined.
	time.Sleep(remoteLBSnapshotTTL + 10*time.Millisecond)
	f.metricsTotal.Store(10)
	f.metricsSuccess.Store(10)
	s3 := a.Snapshot()
	if s3.RequestCount != 0 {
		t.Fatalf("counter reset should clamp: %+v", s3)
	}

	// LB error: cached last-good observation returned.
	time.Sleep(remoteLBSnapshotTTL + 10*time.Millisecond)
	f.failNextRequest.Store(true)
	s4 := a.Snapshot()
	if s4.ObservedAt != s3.ObservedAt {
		t.Fatalf("failed fetch should keep cached snapshot: %+v vs %+v", s4, s3)
	}
}

func TestRemoteAdapterRemoveAndPoolMove(t *testing.T) {
	f, srv := newFakeLB(t)
	defer srv.Close()
	a := mustAdapter(t, srv.URL)
	if err := a.ConfigureScenario(scenarioWith(BackendConfig{ID: "b1", Pool: "default", Address: "127.0.0.1", Port: 9101})); err != nil {
		t.Fatal(err)
	}

	f.reset()
	if err := a.RemoveBackend("b1"); err != nil {
		t.Fatal(err)
	}
	if !f.calledWith("DELETE", "/backend/b1", "") {
		t.Fatalf("remove missing: %v", f.calls)
	}

	// Moving a (now-absent) backend errors cleanly instead of half-applying.
	if err := a.SetBackendPool("b1", "api"); err == nil {
		t.Fatal("expected error moving unregistered backend")
	}

	if err := a.ConfigureScenario(scenarioWith(BackendConfig{ID: "b1", Pool: "default", Address: "127.0.0.1", Port: 9101})); err != nil {
		t.Fatal(err)
	}
	f.reset()
	if err := a.SetBackendPool("b1", "api"); err != nil {
		t.Fatal(err)
	}
	if !f.calledWith("POST", "/backend ", `"pool":"api"`) || !f.calledWith("DELETE", "/backend/b1", "") || !f.calledWith("POST", "/pool ", `"pool_id":"api"`) {
		t.Fatalf("pool move must ensure pool + recreate under it: %v", f.calls)
	}
}

func TestLBTokenSanitization(t *testing.T) {
	cases := map[string]string{
		"default":    "default",
		"My Pool!":   "my-pool",
		"  spaced  ": "spaced",
		"!!!":        remoteLBPoolName,
		"a.b_c-1":    "a.b_c-1",
		"_weird":     "_weird",
		"api#1":      "api-1",
	}
	for in, want := range cases {
		if got := lbToken(in); got != want {
			t.Errorf("lbToken(%q)=%q want %q", in, got, want)
		}
	}
}

func mustAdapter(t *testing.T, controlURL string) *RemoteLoadBalancerAdapter {
	t.Helper()
	a, err := NewRemoteLoadBalancerAdapter(RemoteLBConfig{
		ControlURL: controlURL,
		DataURL:    "http://data",
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
