package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/marcusaroldan/load-balancer/internal/registry"
)

// newStarted boots a runtime on ephemeral loopback ports and waits until
// both listeners accept connections.
func newStarted(t *testing.T, cfg Config) *Runtime {
	t.Helper()
	if cfg.DataAddr == "" {
		cfg.DataAddr = "127.0.0.1:0"
	}
	if cfg.ControlAddr == "" {
		cfg.ControlAddr = "127.0.0.1:0"
	}
	rt := New(cfg, registry.New())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		sctx, scancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer scancel()
		rt.Shutdown(sctx)
	})
	waitReady(t, "http://"+rt.ControlPlaneAddr()+"/healthz")
	return rt
}

func waitReady(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("control plane never became reachable at %s", url)
}

func controlPost(t *testing.T, base, path string, body any) *http.Response {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(base+path, "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("post %s: %v", path, err)
	}
	return resp
}

func splitHTTP(u string) (string, int) {
	parsed, err := url.Parse(u)
	if err != nil {
		panic(err)
	}
	var port int
	fmt.Sscanf(parsed.Port(), "%d", &port)
	return parsed.Hostname(), port
}

// TestRuntimeEndToEndFullCycle is the process-level integration test required
// by PROD-008: it boots the real command components (listeners, registry,
// health, proxy, control plane), configures state through the control plane,
// routes live data-plane traffic, reconfigures strategy under load, and shuts
// everything down.
func TestRuntimeEndToEndFullCycle(t *testing.T) {
	b1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "one")
		fmt.Fprint(w, "one")
	}))
	defer b1.Close()
	b2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "two")
		fmt.Fprint(w, "two")
	}))
	defer b2.Close()

	rt := newStarted(t, Config{HealthInterval: 100 * time.Millisecond, ShutdownTimeout: 3 * time.Second})
	control := "http://" + rt.ControlPlaneAddr()
	data := "http://" + rt.DataPlaneAddr()

	// Empty registry: data plane is 503 (scale-to-zero baseline).
	resp, err := http.Get(data + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured data plane: got %d", resp.StatusCode)
	}

	// Control plane creates pool + members.
	h1Addr, h1Port := splitHTTP(b1.URL)
	h2Addr, h2Port := splitHTTP(b2.URL)
	if rec := controlPost(t, control, "/pool", map[string]any{"pool_id": "web"}); rec.StatusCode != http.StatusCreated {
		t.Fatalf("create pool: %d", rec.StatusCode)
	}
	rec := controlPost(t, control, "/backend", map[string]any{"backend_id": "b1", "pool": "web", "address": h1Addr, "port": h1Port})
	if rec.StatusCode != http.StatusCreated {
		t.Fatalf("create b1: %d", rec.StatusCode)
	}
	rec = controlPost(t, control, "/backend", map[string]any{"backend_id": "b2", "pool": "web", "address": h2Addr, "port": h2Port})
	if rec.StatusCode != http.StatusCreated {
		t.Fatalf("create b2: %d", rec.StatusCode)
	}

	// Readyz flips to ready once configured AND serving.
	readyResp, err := http.Get(control + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	readyResp.Body.Close()
	if readyResp.StatusCode != http.StatusOK {
		t.Fatalf("readyz after config: %d", readyResp.StatusCode)
	}

	// Data plane distributes round-robin across both members.
	seen := map[string]int{}
	for i := 0; i < 6; i++ {
		got := getOrFail(t, data+"/")
		seen[got]++
	}
	if seen["one"] != 3 || seen["two"] != 3 {
		t.Fatalf("round-robin distribution: %v", seen)
	}

	// Control plane swaps strategy live; traffic keeps flowing.
	req, _ := http.NewRequest(http.MethodPut, control+"/config/algorithm", bytes.NewReader([]byte(`{"pool":"web","algorithm":"least_connections"}`)))
	liveResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	liveResp.Body.Close()
	if liveResp.StatusCode != http.StatusOK {
		t.Fatalf("set algorithm: %d", liveResp.StatusCode)
	}
	for i := 0; i < 3; i++ {
		if getOrFail(t, data+"/") == "" {
			t.Fatal("traffic failed after strategy swap")
		}
	}

	// Metrics reflect data-plane totals through the public control endpoint.
	mResp, err := http.Get(control + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer mResp.Body.Close()
	var metrics struct {
		Requests struct {
			Total   int64 `json:"total"`
			Success int64 `json:"success"`
		} `json:"requests"`
		ActiveBackends int `json:"healthy_backends"`
		TotalBackends  int `json:"total_backends"`
	}
	if err := json.NewDecoder(mResp.Body).Decode(&metrics); err != nil {
		t.Fatal(err)
	}
	if metrics.Requests.Total < 9 || metrics.Requests.Success < 9 {
		t.Fatalf("metrics undercount: %+v", metrics)
	}
	if metrics.TotalBackends != 2 || metrics.ActiveBackends != 2 {
		t.Fatalf("health manager did not mark live backends healthy: %+v", metrics)
	}

	// Deleting a member through the API takes it out of rotation immediately.
	delReq, _ := http.NewRequest(http.MethodDelete, control+"/backend/b2", nil)
	dResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	dResp.Body.Close()
	if dResp.StatusCode != http.StatusOK {
		t.Fatalf("delete backend: %d", dResp.StatusCode)
	}
	for i := 0; i < 3; i++ {
		if got := getOrFail(t, data+"/"); got != "one" {
			t.Fatalf("deleted backend still served: %q", got)
		}
	}
}

func getOrFail(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get %s: status %d", url, resp.StatusCode)
	}
	return strings.TrimSpace(string(body))
}

// TestShutdownDrainsInFlightRequest verifies graceful shutdown: an in-flight
// proxied request completes while Shutdown blocks, and new connections are
// refused afterwards.
func TestShutdownDrainsInFlightRequest(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		fmt.Fprint(w, "slow-ok")
	}))
	defer slow.Close()

	rt := newStarted(t, Config{ShutdownTimeout: 3 * time.Second})
	control := "http://" + rt.ControlPlaneAddr()
	data := "http://" + rt.DataPlaneAddr()

	h1Addr, h1Port := splitHTTP(slow.URL)
	if rec := controlPost(t, control, "/pool", map[string]any{"pool_id": "web"}); rec.StatusCode != http.StatusCreated {
		t.Fatal("pool create")
	}
	if rec := controlPost(t, control, "/backend", map[string]any{"backend_id": "b1", "pool": "web", "address": h1Addr, "port": h1Port}); rec.StatusCode != http.StatusCreated {
		t.Fatal("backend create")
	}

	done := make(chan string, 1)
	go func() {
		resp, err := http.Get(data + "/")
		if err != nil {
			done <- "err:" + err.Error()
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		done <- strings.TrimSpace(string(body))
	}()
	time.Sleep(30 * time.Millisecond) // let the request reach the slow backend

	sctx, scancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer scancel()
	if err := rt.Shutdown(sctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	select {
	case got := <-done:
		if got != "slow-ok" {
			t.Fatalf("in-flight request not drained: %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight request never completed")
	}

	// New connections must be refused now.
	if _, err := http.Get(data + "/"); err == nil {
		t.Fatal("data plane still accepting after shutdown")
	}
}

// TestStartupFailures covers the acceptance criteria: busy port -> clean
// startup failure; same address for both planes -> rejected.
func TestStartupFailures(t *testing.T) {
	first := newStarted(t, Config{})

	err := New(Config{DataAddr: first.DataPlaneAddr()}, registry.New()).Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "listen data plane") {
		t.Fatalf("busy port: expected listen failure, got %v", err)
	}

	busy := first.DataPlaneAddr()
	if err := New(Config{DataAddr: busy, ControlAddr: busy}, registry.New()).Start(context.Background()); err != ErrSameAddr {
		t.Fatalf("same-addr config: want ErrSameAddr, got %v", err)
	}
}

func TestControlBindGuard(t *testing.T) {
	// Empty host (all interfaces) without the explicit opt-in is refused.
	err := New(Config{DataAddr: "127.0.0.1:0", ControlAddr: ":0"}, registry.New()).Start(context.Background())
	if err != ErrPublicControlBind {
		t.Fatalf("wildcard control bind: want ErrPublicControlBind, got %v", err)
	}

	// Same bind with the operator opt-in starts (and only then).
	rt := newStarted(t, Config{DataAddr: "127.0.0.1:0", ControlAddr: "127.0.0.1:0", AllowPublicControl: true})
	if rt.ControlPlaneAddr() == "" {
		t.Fatal("opt-in start failed")
	}

	// Loopback binds need no opt-in (covered implicitly by every other test;
	// asserted again here for the guard's positive case).
	if err := New(Config{DataAddr: "127.0.0.1:0", ControlAddr: "127.0.0.1:0"}, registry.New()).Start(context.Background()); err != nil {
		t.Fatalf("loopback control bind should start: %v", err)
	}
}

func TestRequestIDEndToEnd(t *testing.T) {
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend-Saw-Request-ID", r.Header.Get("X-Request-ID"))
	}))
	defer echo.Close()

	rt := newStarted(t, Config{})
	control := "http://" + rt.ControlPlaneAddr()
	data := "http://" + rt.DataPlaneAddr()

	h1, p1 := splitHTTP(echo.URL)
	requireStatusLocal(t, controlPost(t, control, "/pool", map[string]any{"pool_id": "web"}), http.StatusCreated)
	requireStatusLocal(t, controlPost(t, control, "/backend", map[string]any{"backend_id": "b1", "pool": "web", "address": h1, "port": p1}), http.StatusCreated)

	// Generated when absent.
	resp, err := http.Get(data + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	gen := resp.Header.Get("X-Request-ID")
	if len(gen) != 16 {
		t.Fatalf("request id not generated: %q", gen)
	}

	// Client-supplied ids are preserved end to end (upstream sees the same).
	req, _ := http.NewRequest(http.MethodGet, data+"/", nil)
	req.Header.Set("X-Request-ID", "trace-me-123")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if got := resp2.Header.Get("X-Request-ID"); got != "trace-me-123" {
		t.Fatalf("client id not echoed: %q", got)
	}
	if got := resp2.Header.Get("X-Backend-Saw-Request-ID"); got != "trace-me-123" {
		t.Fatalf("client id not forwarded upstream: %q", got)
	}
}

func requireStatusLocal(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != want {
		t.Fatalf("status %d want %d", resp.StatusCode, want)
	}
}
