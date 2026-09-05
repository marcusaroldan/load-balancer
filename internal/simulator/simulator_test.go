package simulator

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"testing"
	"time"
)

func backendStateByID(t *testing.T, h *Harness, id string) BackendState {
	t.Helper()
	for _, backend := range h.State().Backends {
		if backend.ID == id {
			return backend
		}
	}
	t.Fatalf("backend %q not found in state", id)
	return BackendState{}
}

func waitForCondition(t *testing.T, timeout time.Duration, check func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s: %s", timeout, description)
}

func TestSimulationStateMatchesPureSimulatorContract(t *testing.T) {
	cfg := PresetScenarioConfigs["stable"]
	h, err := NewHarness(cfg)
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}
	defer h.Stop()

	state := h.State()
	payload, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}

	forbidden := []string{"selected_backend", "destination", "route_history", "lb_state", "last_request", "active_route"}
	for _, key := range forbidden {
		if _, ok := raw[key]; ok {
			t.Fatalf("state unexpectedly exposes forbidden LB routing field %q: %s", key, payload)
		}
	}

	wantStateKeys := []string{"name", "strategy", "clients", "request_rate", "pools", "backends"}
	gotStateKeys := make([]string, 0, len(raw))
	for key := range raw {
		gotStateKeys = append(gotStateKeys, key)
	}
	sort.Strings(wantStateKeys)
	sort.Strings(gotStateKeys)
	if !reflect.DeepEqual(gotStateKeys, wantStateKeys) {
		t.Fatalf("unexpected state keys: got %v want %v", gotStateKeys, wantStateKeys)
	}

	if len(state.Backends) == 0 {
		t.Fatal("state should contain backend records")
	}
	backendMap := map[string]any{}
	backendJson, err := json.Marshal(state.Backends[0])
	if err != nil {
		t.Fatalf("marshal backend: %v", err)
	}
	if err := json.Unmarshal(backendJson, &backendMap); err != nil {
		t.Fatalf("unmarshal backend: %v", err)
	}

	wantBackendKeys := []string{"active_connections", "failing", "failure_rate", "healthy", "id", "latency", "load_percent", "name", "pool", "url", "weight"}
	gotBackendKeys := make([]string, 0, len(backendMap))
	for key := range backendMap {
		gotBackendKeys = append(gotBackendKeys, key)
	}
	sort.Strings(wantBackendKeys)
	sort.Strings(gotBackendKeys)
	if !reflect.DeepEqual(gotBackendKeys, wantBackendKeys) {
		t.Fatalf("unexpected backend keys: got %v want %v", gotBackendKeys, wantBackendKeys)
	}
}

func TestRandomProcessingDelayStaysWithinSimulationBounds(t *testing.T) {
	const samples = 250
	for i := 0; i < samples; i++ {
		delay := randomProcessingDelay()
		if delay < 50*time.Millisecond || delay > 200*time.Millisecond {
			t.Fatalf("processing delay out of bounds: got %s, want between 50ms and 200ms", delay)
		}
	}
}

func TestHarnessShowsActiveConnectionsDuringTraffic(t *testing.T) {
	cfg := PresetScenarioConfigs["stable"]
	h, err := NewHarness(cfg)
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}
	defer h.Stop()

	if err := h.SetRequestRate(150); err != nil {
		t.Fatalf("set request rate: %v", err)
	}
	if err := h.AddClient(); err != nil {
		t.Fatalf("add client: %v", err)
	}

	waitForCondition(t, 3*time.Second, func() bool {
		state := h.State()
		for _, backend := range state.Backends {
			if backend.ActiveConnections > 0 {
				return true
			}
		}
		return false
	}, "traffic should keep at least one backend busy long enough to observe active connections")
}

func TestScenarioPresetsExist(t *testing.T) {
	for _, name := range []string{"stable", "high_latency", "partial_failure", "partition", "weighted"} {
		cfg, ok := PresetScenarioConfigs[name]
		if !ok {
			t.Fatalf("missing preset scenario %q", name)
		}
		if cfg.Name == "" {
			t.Fatalf("scenario %q missing name", name)
		}
		if cfg.Strategy == "" {
			t.Fatalf("scenario %q missing strategy", name)
		}
		if len(cfg.Backends) == 0 {
			t.Fatalf("scenario %q has no backends", name)
		}
	}
}

func TestHarnessStartsAndResponds(t *testing.T) {
	cfg := PresetScenarioConfigs["stable"]
	h, err := NewHarness(cfg)
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}
	defer h.Stop()

	backends := h.Backends()
	if len(backends) != len(cfg.Backends) {
		t.Fatalf("expected %d backends, got %d", len(cfg.Backends), len(backends))
	}

	for _, backend := range backends {
		resp, err := http.Get(backend.URL + "/health")
		if err != nil {
			t.Fatalf("fetch health for %s: %v", backend.ID, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("backend %s health endpoint returned %d", backend.ID, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestHarnessCanToggleState(t *testing.T) {
	cfg := PresetScenarioConfigs["stable"]
	h, err := NewHarness(cfg)
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}
	defer h.Stop()

	if err := h.SetHealthy("backend-1", false); err != nil {
		t.Fatalf("mark backend unhealthy: %v", err)
	}
	if err := h.SetLatency("backend-1", 250*time.Millisecond); err != nil {
		t.Fatalf("set latency: %v", err)
	}

	backend, err := h.GetBackend("backend-1")
	if err != nil {
		t.Fatalf("get backend: %v", err)
	}
	if backend.Healthy {
		t.Fatal("backend should be unhealthy after toggle")
	}
	if backend.Latency != 250*time.Millisecond {
		t.Fatal("latency not updated")
	}
}

func TestHarnessSupportsLiveSimulationControls(t *testing.T) {
	cfg := PresetScenarioConfigs["stable"]
	h, err := NewHarness(cfg)
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}
	defer h.Stop()

	if err := h.SetStrategy("weighted_round_robin"); err != nil {
		t.Fatalf("set strategy: %v", err)
	}
	if err := h.SetRequestRate(80); err != nil {
		t.Fatalf("set request rate: %v", err)
	}
	if err := h.AddClient(); err != nil {
		t.Fatalf("add client: %v", err)
	}
	if _, err := h.AddBackend("backend-4", "127.0.0.1", 18130, 2, 100*time.Millisecond, true); err != nil {
		t.Fatalf("add backend: %v", err)
	}

	state := h.State()
	if state.Strategy != "weighted_round_robin" {
		t.Fatalf("expected weighted_round_robin strategy, got %q", state.Strategy)
	}
	if state.RequestRate != 80 {
		t.Fatalf("expected request rate 80, got %d", state.RequestRate)
	}
	if state.Clients != 4 {
		t.Fatalf("expected 4 clients, got %d", state.Clients)
	}
	if len(state.Backends) != 4 {
		t.Fatalf("expected 4 backends, got %d", len(state.Backends))
	}
}

func TestHarnessCanResetToScenario(t *testing.T) {
	cfg := PresetScenarioConfigs["stable"]
	h, err := NewHarness(cfg)
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}
	defer h.Stop()

	if err := h.SetStrategy("least_connections"); err != nil {
		t.Fatalf("set strategy: %v", err)
	}
	if err := h.SetRequestRate(400); err != nil {
		t.Fatalf("set request rate: %v", err)
	}
	if err := h.ResetScenario("high_latency"); err != nil {
		t.Fatalf("reset scenario: %v", err)
	}

	state := h.State()
	if state.Name != "high_latency" {
		t.Fatalf("expected high_latency scenario, got %q", state.Name)
	}
	if state.Strategy != "least_connections" {
		t.Fatalf("expected least_connections strategy, got %q", state.Strategy)
	}
	if state.RequestRate != 30 {
		t.Fatalf("expected request rate reset to 30, got %d", state.RequestRate)
	}
	if len(state.Backends) != 3 {
		t.Fatalf("expected 3 backends after reset, got %d", len(state.Backends))
	}
}

func TestHarnessTracksBackendLoadAndSupportsClientRemoval(t *testing.T) {
	cfg := PresetScenarioConfigs["stable"]
	h, err := NewHarness(cfg)
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}
	defer h.Stop()

	if err := h.SetRequestRate(48); err != nil {
		t.Fatalf("set request rate: %v", err)
	}
	if err := h.AddClient(); err != nil {
		t.Fatalf("add client: %v", err)
	}

	state := h.State()
	if len(state.Backends) == 0 {
		t.Fatal("state has no backends")
	}
	for _, backend := range state.Backends {
		if backend.ActiveConnections < 0 {
			t.Fatalf("backend %s active connections cannot be negative", backend.ID)
		}
		if backend.Latency < 0 {
			t.Fatalf("backend %s latency cannot be negative", backend.ID)
		}
	}

	if err := h.RemoveClient(); err != nil {
		t.Fatalf("remove client: %v", err)
	}
	if h.State().Clients != 3 {
		t.Fatalf("expected clients to be reduced to 3, got %d", h.State().Clients)
	}
}

func TestHarnessDoesNotModelLoadBalancerRouting(t *testing.T) {
	cfg := PresetScenarioConfigs["weighted"]
	h, err := NewHarness(cfg)
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}
	defer h.Stop()

	backend1, err := h.GetBackend("backend-1")
	if err != nil {
		t.Fatalf("get backend-1: %v", err)
	}
	backend2, err := h.GetBackend("backend-2")
	if err != nil {
		t.Fatalf("get backend-2: %v", err)
	}

	backend1.mu.Lock()
	backend1.ActiveConnections = 12
	backend1.mu.Unlock()
	backend2.mu.Lock()
	backend2.ActiveConnections = 4
	backend2.mu.Unlock()

	if err := h.SetStrategy("least_connections"); err != nil {
		t.Fatalf("set strategy: %v", err)
	}
	if err := h.SetRequestRate(300); err != nil {
		t.Fatalf("set request rate: %v", err)
	}
	if err := h.SetWeight("backend-2", 8); err != nil {
		t.Fatalf("set weight: %v", err)
	}

	state := h.State()
	backend1State := backendStateByID(t, h, "backend-1")
	backend2State := backendStateByID(t, h, "backend-2")
	if backend1State.LoadPercent != 100 {
		t.Fatalf("expected load percent to mirror active connections, got %d for backend-1", backend1State.LoadPercent)
	}
	if backend2State.LoadPercent != 40 {
		t.Fatalf("expected backend-2 load percent to stay tied to its active connections, got %d", backend2State.LoadPercent)
	}
	if got, want := state.Strategy, "least_connections"; got != want {
		t.Fatalf("expected simulator strategy state to remain %q, got %q", want, got)
	}
	if got, want := state.RequestRate, 300; got != want {
		t.Fatalf("expected simulator request rate to remain %d, got %d", want, got)
	}
}

func TestHarnessEnvironmentStateIsPassedThroughForLB(t *testing.T) {
	cfg := PresetScenarioConfigs["stable"]
	h, err := NewHarness(cfg)
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}
	defer h.Stop()

	if err := h.SetHealthy("backend-2", false); err != nil {
		t.Fatalf("set healthy: %v", err)
	}
	if err := h.SetLatency("backend-2", 225*time.Millisecond); err != nil {
		t.Fatalf("set latency: %v", err)
	}
	if err := h.SetFailureRate("backend-2", 0.7); err != nil {
		t.Fatalf("set failure rate: %v", err)
	}
	if err := h.SetBackendPool("backend-2", "edge"); err != nil {
		t.Fatalf("set backend pool: %v", err)
	}
	if err := h.SetWeight("backend-2", 9); err != nil {
		t.Fatalf("set weight: %v", err)
	}

	state := h.State()
	backend := backendStateByID(t, h, "backend-2")
	if backend.Healthy {
		t.Fatal("backend-2 should remain unhealthy in the simulation state")
	}
	if backend.Latency != 225*time.Millisecond {
		t.Fatalf("expected latency to remain 225ms, got %s", backend.Latency)
	}
	if backend.FailureRate != 0.7 {
		t.Fatalf("expected failure rate 0.7, got %f", backend.FailureRate)
	}
	if backend.Pool != "edge" {
		t.Fatalf("expected pool edge, got %q", backend.Pool)
	}
	if backend.Weight != 9 {
		t.Fatalf("expected weight 9, got %d", backend.Weight)
	}
	foundEdgePool := false
	for _, pool := range state.Pools {
		if pool == "edge" {
			foundEdgePool = true
			break
		}
	}
	if !foundEdgePool {
		t.Fatalf("expected simulator to expose pool group %q in state, got %v", "edge", state.Pools)
	}
}

func TestHarnessUnhealthyAndPartitionedBackendsDrainTraffic(t *testing.T) {
	cfg := PresetScenarioConfigs["stable"]
	h, err := NewHarness(cfg)
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}
	defer h.Stop()

	backend, err := h.GetBackend("backend-2")
	if err != nil {
		t.Fatalf("get backend: %v", err)
	}
	if err := h.SetLatency("backend-2", 150*time.Millisecond); err != nil {
		t.Fatalf("set latency: %v", err)
	}

	for i := 0; i < 8; i++ {
		go func() {
			resp, err := http.Get(backend.URL)
			if err == nil {
				resp.Body.Close()
			}
		}()
	}

	waitForCondition(t, 2*time.Second, func() bool {
		current, err := h.GetBackend("backend-2")
		if err != nil {
			return false
		}
		current.mu.RLock()
		active := current.ActiveConnections
		current.mu.RUnlock()
		return active > 0
	}, "request traffic should begin hitting backend-2")

	if err := h.SetHealthy("backend-2", false); err != nil {
		t.Fatalf("mark partitioned backend unhealthy: %v", err)
	}

	waitForCondition(t, 3*time.Second, func() bool {
		current, err := h.GetBackend("backend-2")
		if err != nil {
			return false
		}
		current.mu.RLock()
		active := current.ActiveConnections
		current.mu.RUnlock()
		return active == 0
	}, "unhealthy backend traffic should eventually drain")

	state := h.State()
	unhealthy := backendStateByID(t, h, "backend-2")
	if unhealthy.Healthy {
		t.Fatal("backend-2 should remain unhealthy after the drain check")
	}
	if len(state.Backends) == 0 {
		t.Fatal("state should contain backend records even when unhealthy")
	}
	if got := fmt.Sprintf("%d", unhealthy.LoadPercent); got == "" {
		t.Fatal("load percent should be serialized for unhealthy backends")
	}
}

func TestHarnessCanRemoveBackend(t *testing.T) {
	cfg := PresetScenarioConfigs["stable"]
	h, err := NewHarness(cfg)
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}
	defer h.Stop()

	if err := h.RemoveBackend("backend-2"); err != nil {
		t.Fatalf("remove backend: %v", err)
	}
	if _, err := h.GetBackend("backend-2"); err == nil {
		t.Fatal("backend-2 still existed after removal")
	}
}
