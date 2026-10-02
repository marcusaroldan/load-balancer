package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marcusaroldan/load-balancer/internal/config"
	"github.com/marcusaroldan/load-balancer/internal/registry"
)

func testRegistry(t *testing.T, backends []config.BackendConfig) *registry.Registry {
	t.Helper()
	poolCfg := config.PoolConfig{ID: "web", Algorithm: config.StrategyRoundRobin}
	r := registry.New()
	if err := r.Load(config.Config{Pools: []config.PoolConfig{poolCfg}, Backends: backends}); err != nil {
		t.Fatalf("registry Load: %v", err)
	}
	return r
}

func backendConfigs(servers []*httptest.Server) []config.BackendConfig {
	out := make([]config.BackendConfig, 0, len(servers))
	for i, s := range servers {
		out = append(out, config.BackendConfig{
			ID:      fmt.Sprintf("b%d", i+1),
			PoolID:  "web",
			Address: s.URL,
			Enabled: true,
		})
	}
	return out
}

func TestHandlerForwardsRequestToBackend(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello from %s %s", r.Method, r.URL.Path)
	}))
	defer backend.Close()

	reg := testRegistry(t, backendConfigs([]*httptest.Server{backend}))
	handler := NewHandler(HandlerConfig{Registry: reg, PoolID: "web"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/orders/42", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if want := "hello from GET /orders/42"; rec.Body.String() != want {
		t.Fatalf("body=%q want %q", rec.Body.String(), want)
	}
	metrics := handler.Metrics()
	if metrics.Total != 1 || metrics.Success != 1 {
		t.Fatalf("metrics=%+v", metrics)
	}
}

func TestHandlerDistributesAcrossStrategies(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	servers := make([]*httptest.Server, 3)
	for i := range servers {
		id := fmt.Sprintf("b%d", i+1)
		servers[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			hits[id]++
			mu.Unlock()
		}))
		defer servers[i].Close()
	}

	reg := testRegistry(t, backendConfigs(servers))
	handler := NewHandler(HandlerConfig{Registry: reg, PoolID: "web"})

	for i := 0; i < 9; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status=%d", i, rec.Code)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, id := range []string{"b1", "b2", "b3"} {
		if hits[id] != 3 {
			t.Fatalf("round robin distribution wrong: %v", hits)
		}
	}
}

func TestHandlerNoHealthyBackendsReturns503(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer backend.Close()

	reg := testRegistry(t, backendConfigs([]*httptest.Server{backend}))
	if err := reg.ResetHealth("web", "b1", false); err != nil {
		t.Fatalf("ResetHealth: %v", err)
	}
	handler := NewHandler(HandlerConfig{Registry: reg, PoolID: "web"})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", rec.Code)
	}
	if m := handler.Metrics(); m.RouteFailures != 1 || m.Total != 1 {
		t.Fatalf("metrics=%+v", m)
	}
}

func TestHandlerUpstreamFailureReturns502(t *testing.T) {
	// Server that accepts then immediately closes without responding.
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("no hijacker")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
	}))
	url := failing.URL
	failing.Close()

	bc := config.BackendConfig{ID: "b1", PoolID: "web", Address: url, Enabled: true}
	reg := testRegistry(t, []config.BackendConfig{bc})
	handler := NewHandler(HandlerConfig{Registry: reg, PoolID: "web"})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d body=%q", rec.Code, rec.Body.String())
	}
	if m := handler.Metrics(); m.GatewayFailures != 1 {
		t.Fatalf("metrics=%+v", m)
	}
}

func TestHandlerEnforcesMaxConnections(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer slow.Close()

	bc := config.BackendConfig{
		ID: "b1", PoolID: "web", Address: slow.URL,
		Enabled: true, MaxConnections: 1,
	}
	reg := testRegistry(t, []config.BackendConfig{bc})
	handler := NewHandler(HandlerConfig{Registry: reg, PoolID: "web"})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	}()
	<-started // first request now occupies the single slot

	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec2.Code != http.StatusServiceUnavailable {
		close(release)
		wg.Wait()
		t.Fatalf("second request should hit limit: %d", rec2.Code)
	}
	close(release)
	wg.Wait()

	// Slot released: third request succeeds and counters do not leak.
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec3.Code != http.StatusOK {
		t.Fatalf("after release want 200, got %d", rec3.Code)
	}
	if active, err := reg.ActiveConnections("web", "b1"); err != nil || active != 0 {
		t.Fatalf("connection counter leaked: active=%d err=%v", active, err)
	}
}

func TestHandlerDrainingExcludesBackend(t *testing.T) {
	drained := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Server", "drained")
	}))
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Server", "live")
	}))
	defer drained.Close()
	defer live.Close()

	backends := backendConfigs([]*httptest.Server{drained, live})
	reg := testRegistry(t, backends)
	handler := NewHandler(HandlerConfig{Registry: reg, PoolID: "web"})

	if _, err := reg.BeginDrain("web", "b1"); err != nil {
		t.Fatalf("BeginDrain: %v", err)
	}
	for i := 0; i < 4; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if got := rec.Header().Get("X-Server"); got != "live" {
			t.Fatalf("draining backend served request %d: %s", i, got)
		}
	}
}

func TestRemoveDrainedWaitsForInFlight(t *testing.T) {
	inflightRelease := make(chan struct{})
	serverStarted := make(chan struct{}, 1)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverStarted <- struct{}{}
		<-inflightRelease
		w.WriteHeader(http.StatusOK)
	}))
	defer slow.Close()

	bc := config.BackendConfig{ID: "b1", PoolID: "web", Address: slow.URL, Enabled: true}
	reg := testRegistry(t, []config.BackendConfig{bc})
	handler := NewHandler(HandlerConfig{Registry: reg, PoolID: "web"})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	}()
	<-serverStarted

	count, err := reg.BeginDrain("web", "b1")
	if err != nil {
		t.Fatalf("BeginDrain: %v", err)
	}
	if count != 1 {
		t.Fatalf("BeginDrain should report one in-flight connection, got %d", count)
	}
	if err := reg.RemoveDrained("web", "b1", false); err == nil {
		t.Fatal("removal while draining must block until connections finish")
	}

	close(inflightRelease)
	wg.Wait()

	remaining, complete := reg.WaitDrain("web", "b1", time.Second)
	if !complete || remaining != 0 {
		t.Fatalf("WaitDrain incomplete: remaining=%d", remaining)
	}
	if err := reg.RemoveDrained("web", "b1", false); err != nil {
		t.Fatalf("RemoveDrained after completion: %v", err)
	}
}

func TestHandlerPreservesRequestBodyAndHeaders(t *testing.T) {
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := new(strings.Builder)
		if _, err := fmt.Fprint(body, r.Header.Get("X-Marker")); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 64)
		n, _ := r.Body.Read(buf)
		fmt.Fprintf(w, "%s|%s", body.String(), string(buf[:n]))
	}))
	defer echo.Close()

	reg := testRegistry(t, backendConfigs([]*httptest.Server{echo}))
	handler := NewHandler(HandlerConfig{Registry: reg, PoolID: "web"})

	req := httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader("payload-123"))
	req.Header.Set("X-Marker", "marked")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Body.String(); got != "marked|payload-123" {
		t.Fatalf("forwarding lost method/body/header: %q", got)
	}
}

func TestMetricsAverageLatencyRecorded(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Millisecond)
	}))
	defer backend.Close()

	reg := testRegistry(t, backendConfigs([]*httptest.Server{backend}))
	handler := NewHandler(HandlerConfig{Registry: reg, PoolID: "web"})
	for i := 0; i < 3; i++ {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	}
	m := handler.Metrics()
	if m.Total != 3 || m.Success != 3 {
		t.Fatalf("metrics=%+v", m)
	}
	if m.AverageLatency() <= 0 {
		t.Fatalf("latency not recorded: %+v", m)
	}
}
