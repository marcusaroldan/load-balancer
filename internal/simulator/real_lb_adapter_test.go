package simulator

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestMapScenarioToLoadBalancerNormalizesAndPreservesConfiguration(t *testing.T) {
	mapped, err := mapScenarioToLoadBalancer(ScenarioConfig{
		Name: "  weighted-demo ",
		Backends: []BackendConfig{
			{ID: " backend-one ", Address: " 127.0.0.1 ", Port: 18081, Weight: 3, Healthy: true, Pool: " pool-a "},
			{ID: "backend-two", Port: 18082, Healthy: false, Pool: ""},
		},
	})
	if err != nil {
		t.Fatalf("map scenario: %v", err)
	}

	if mapped.Name != "weighted-demo" {
		t.Fatalf("expected trimmed scenario name, got %q", mapped.Name)
	}
	if mapped.Strategy != "round_robin" {
		t.Fatalf("expected default strategy, got %q", mapped.Strategy)
	}
	if got := mapped.Backends["backend-one"]; got.Pool != "pool-a" || got.Weight != 3 || got.Address != "127.0.0.1" {
		t.Fatalf("backend-one was not preserved and normalized: %+v", got)
	}
	if got := mapped.Backends["backend-two"]; got.Pool != defaultPoolName || got.Healthy {
		t.Fatalf("backend-two defaults were not applied: %+v", got)
	}
}

func TestMapScenarioToLoadBalancerRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name     string
		scenario ScenarioConfig
		want     string
	}{
		{name: "unknown strategy", scenario: ScenarioConfig{Name: "demo", Strategy: "random"}, want: "unknown strategy"},
		{name: "duplicate backend", scenario: ScenarioConfig{Name: "demo", Backends: []BackendConfig{{ID: "same"}, {ID: " same "}}}, want: "duplicated"},
		{name: "invalid latency", scenario: ScenarioConfig{Name: "demo", Backends: []BackendConfig{{ID: "one", Latency: -1}}}, want: "latency cannot be negative"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := mapScenarioToLoadBalancer(test.scenario)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
		})
	}
}

func TestRealLoadBalancerAdapterConfiguresPresetScenarios(t *testing.T) {
	for name, scenario := range PresetScenarioConfigs {
		t.Run(name, func(t *testing.T) {
			adapter := NewRealLoadBalancerAdapter()
			if err := adapter.ConfigureScenario(scenario); err != nil {
				t.Fatalf("configure %s: %v", name, err)
			}
			snapshot := adapter.Snapshot()
			if snapshot.Name != scenario.Name {
				t.Fatalf("expected scenario name %q, got %q", scenario.Name, snapshot.Name)
			}
			if snapshot.Strategy != scenario.Strategy {
				t.Fatalf("expected strategy %q, got %q", scenario.Strategy, snapshot.Strategy)
			}
			if snapshot.BackendCount != len(scenario.Backends) {
				t.Fatalf("expected %d backends, got %d", len(scenario.Backends), snapshot.BackendCount)
			}
			if snapshot.PoolCount != 1 {
				t.Fatalf("expected one configured pool, got %d", snapshot.PoolCount)
			}
		})
	}
}

func TestRealLoadBalancerAdapterRejectsScenarioWithoutMutation(t *testing.T) {
	adapter := NewRealLoadBalancerAdapter()
	if err := adapter.ConfigureScenario(PresetScenarioConfigs["stable"]); err != nil {
		t.Fatalf("configure stable scenario: %v", err)
	}

	before := adapter.Snapshot()
	err := adapter.ConfigureScenario(ScenarioConfig{
		Name:     "invalid",
		Strategy: "not-a-strategy",
	})
	if err == nil {
		t.Fatal("expected invalid scenario to be rejected")
	}
	after := adapter.Snapshot()
	if after.Name != before.Name || after.Strategy != before.Strategy || after.BackendCount != before.BackendCount {
		t.Fatalf("invalid scenario mutated adapter: before=%+v after=%+v", before, after)
	}
}

func TestRealLoadBalancerAdapterRoutesThroughRealSelector(t *testing.T) {
	backendOne := newTestBackendServer(t, "backend-one")
	backendTwo := newTestBackendServer(t, "backend-two")

	adapter := NewRealLoadBalancerAdapter()
	if err := adapter.ConfigureScenario(ScenarioConfig{
		Name:     "adapter-test",
		Strategy: "round_robin",
		Backends: []BackendConfig{
			backendConfigFromServer("backend-one", backendOne),
			backendConfigFromServer("backend-two", backendTwo),
		},
	}); err != nil {
		t.Fatalf("configure scenario: %v", err)
	}
	if err := adapter.Start(); err != nil {
		t.Fatalf("start adapter: %v", err)
	}
	defer adapter.Stop()

	for range 4 {
		response, err := http.Get(adapter.Endpoint())
		if err != nil {
			t.Fatalf("request through adapter: %v", err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("expected successful proxied response, got %d", response.StatusCode)
		}
	}

	snapshot := adapter.Snapshot()
	if snapshot.RequestCount != 4 {
		t.Fatalf("expected 4 requests through the adapter, got %d", snapshot.RequestCount)
	}
	if snapshot.SuccessfulRequests != 4 {
		t.Fatalf("expected 4 successful requests, got %d", snapshot.SuccessfulRequests)
	}
}

func TestRealLoadBalancerAdapterAppliesScenarioStrategyAndWeights(t *testing.T) {
	backendOne := newTestBackendServer(t, "backend-one")
	backendTwo := newTestBackendServer(t, "backend-two")

	adapter := NewRealLoadBalancerAdapter()
	if err := adapter.ConfigureScenario(ScenarioConfig{
		Name:     "weighted-adapter-test",
		Strategy: "weighted_round_robin",
		Backends: []BackendConfig{
			{ID: "backend-one", Address: serverAddress(t, backendOne), Port: serverPort(t, backendOne), Weight: 3, Healthy: true, Pool: "default"},
			{ID: "backend-two", Address: serverAddress(t, backendTwo), Port: serverPort(t, backendTwo), Weight: 1, Healthy: true, Pool: "default"},
		},
	}); err != nil {
		t.Fatalf("configure weighted scenario: %v", err)
	}

	if got := adapter.Snapshot(); got.Strategy != "weighted_round_robin" {
		t.Fatalf("expected weighted strategy, got %q", got.Strategy)
	}
	if err := adapter.SetBackendWeight("backend-two", 5); err != nil {
		t.Fatalf("set backend weight: %v", err)
	}
	if got := adapter.Snapshot(); got.BackendCount != 2 || got.HealthyBackends != 2 {
		t.Fatalf("unexpected weighted scenario snapshot: %+v", got)
	}
}

func newTestBackendServer(t *testing.T, response string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(server.Close)
	return server
}

func backendConfigFromServer(id string, server *httptest.Server) BackendConfig {
	parsed, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(parsed.Port())
	return BackendConfig{
		ID:      id,
		Address: parsed.Hostname(),
		Port:    port,
		Weight:  1,
		Healthy: true,
		Pool:    "default",
	}
}

func serverAddress(t *testing.T, server *httptest.Server) string {
	t.Helper()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	return parsed.Hostname()
}

func serverPort(t *testing.T, server *httptest.Server) int {
	t.Helper()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("parse test server port: %v", err)
	}
	return port
}
