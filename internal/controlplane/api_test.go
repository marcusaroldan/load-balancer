package controlplane

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marcusaroldan/load-balancer/internal/config"
	"github.com/marcusaroldan/load-balancer/internal/proxy"
	"github.com/marcusaroldan/load-balancer/internal/registry"
)

type stubMetrics struct{ m proxy.RequestMetrics }

func (s stubMetrics) Metrics() proxy.RequestMetrics { return s.m }

func newTestAPI(t *testing.T, withDataPlane bool) (*APIHandler, *registry.Registry) {
	t.Helper()
	reg := registry.New()
	base := config.Config{
		Pools: []config.PoolConfig{{ID: "web", Algorithm: config.StrategyRoundRobin}},
	}
	if err := reg.Load(base); err != nil {
		t.Fatalf("Load: %v", err)
	}
	deps := APIDeps{Registry: reg, StartedAt: time.Now().Add(-time.Minute)}
	if withDataPlane {
		deps.DataPlane = stubMetrics{m: proxy.RequestMetrics{Total: 10, Success: 9, GatewayFailures: 1}}
	}
	return NewAPIHandler(deps), reg
}

func doJSON(t *testing.T, h http.Handler, method, path string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
}

func requireStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status=%d want %d body=%s", rec.Code, want, rec.Body.String())
	}
}

func backendBody(id string) map[string]any {
	return map[string]any{"backend_id": id, "address": "10.0.0.5", "port": 3000}
}

func TestCreateBackendHappyPath(t *testing.T) {
	h, reg := newTestAPI(t, false)
	rec := doJSON(t, h, http.MethodPost, "/backend", backendBody("b1"))
	requireStatus(t, rec, http.StatusCreated)

	var resp createBackendResponse
	decode(t, rec, &resp)
	if resp.APIVersion != APIVersion || resp.BackendID != "b1" || resp.PoolID != "web" {
		t.Fatalf("resp=%+v", resp)
	}
	if _, _, _, err := reg.FindBackend("b1"); err != nil {
		t.Fatalf("backend not in live registry: %v", err)
	}
}

func TestCreateBackendPoolResolution(t *testing.T) {
	h, _ := newTestAPI(t, false)

	// Explicit unknown pool -> 404.
	body := backendBody("bx")
	body["pool"] = "ghost"
	requireStatus(t, doJSON(t, h, http.MethodPost, "/backend", body), http.StatusNotFound)

	// Second pool makes the field required.
	requireStatus(t, doJSON(t, h, http.MethodPost, "/pool",
		map[string]any{"pool_id": "api", "algorithm": config.StrategyWeightedRR}), http.StatusCreated)
	requireStatus(t, doJSON(t, h, http.MethodPost, "/backend", backendBody("by")), http.StatusBadRequest)
	body2 := backendBody("bz")
	body2["pool"] = "api"
	requireStatus(t, doJSON(t, h, http.MethodPost, "/backend", body2), http.StatusCreated)
}

func TestCreateBackendValidationAndConflict(t *testing.T) {
	h, _ := newTestAPI(t, false)

	requireStatus(t, doJSON(t, h, http.MethodPost, "/backend",
		map[string]any{"backend_id": "ok", "address": "", "port": 1}), http.StatusBadRequest)
	requireStatus(t, doJSON(t, h, http.MethodPost, "/backend",
		map[string]any{"backend_id": "ok", "address": "h", "port": 70000}), http.StatusBadRequest)
	requireStatus(t, doJSON(t, h, http.MethodPost, "/backend",
		map[string]any{"backend_id": "bad id", "address": "h", "port": 80}), http.StatusBadRequest)

	requireStatus(t, doJSON(t, h, http.MethodPost, "/backend", backendBody("dup")), http.StatusCreated)
	requireStatus(t, doJSON(t, h, http.MethodPost, "/backend", backendBody("dup")), http.StatusConflict)

	// Malformed JSON -> 400.
	req := httptest.NewRequest(http.MethodPost, "/backend", strings.NewReader("{not json"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	requireStatus(t, rec, http.StatusBadRequest)
}

func TestGetBackendAndHealth(t *testing.T) {
	h, reg := newTestAPI(t, false)
	requireStatus(t, doJSON(t, h, http.MethodPost, "/backend", backendBody("b1")), http.StatusCreated)

	rec := doJSON(t, h, http.MethodGet, "/backend/b1", nil)
	requireStatus(t, rec, http.StatusOK)
	var got backendResponse
	decode(t, rec, &got)
	if got.Port != 3000 || !got.Enabled || !got.Healthy || got.Weight != config.DefaultBackendWeight {
		t.Fatalf("desired+observed wrong: %+v", got)
	}

	// Unknown backend: 404 on both routes.
	requireStatus(t, doJSON(t, h, http.MethodGet, "/backend/ghost", nil), http.StatusNotFound)
	requireStatus(t, doJSON(t, h, http.MethodGet, "/backend/ghost/health", nil), http.StatusNotFound)

	// Health reflects probe counters; default unhealthy threshold is 3.
	for i := 0; i < 3; i++ {
		if err := reg.MarkUnhealthy("web", "b1"); err != nil {
			t.Fatal(err)
		}
	}
	rec = doJSON(t, h, http.MethodGet, "/backend/b1/health", nil)
	requireStatus(t, rec, http.StatusOK)
	var health healthResponse
	decode(t, rec, &health)
	if health.Healthy || health.ConsecutiveFailures != 3 || health.LastCheck == "" {
		t.Fatalf("health=%+v", health)
	}
}

func TestDeleteBackendReportsDrain(t *testing.T) {
	h, reg := newTestAPI(t, false)
	requireStatus(t, doJSON(t, h, http.MethodPost, "/backend", backendBody("b1")), http.StatusCreated)

	release, err := reg.AcquireConnection("web", "b1")
	if err != nil {
		t.Fatalf("AcquireConnection: %v", err)
	}

	// In-flight connection forces a bounded wait; drain timeout keeps it snappy.
	drainH := NewAPIHandler(APIDeps{Registry: reg, DrainTimeout: 20 * time.Millisecond, StartedAt: time.Now()})
	rec := doJSON(t, drainH, http.MethodDelete, "/backend/b1", nil)
	requireStatus(t, rec, http.StatusOK)
	var drained drainResponse
	decode(t, rec, &drained)
	if drained.DrainedConnections != 1 || drained.Complete {
		t.Fatalf("expected forced drain report, got %+v", drained)
	}
	if _, _, _, err := reg.FindBackend("b1"); err == nil {
		t.Fatal("backend still present after delete")
	}
	release() // idempotent tracker release after removal must not panic or leak
}

func TestDeleteBackendClean(t *testing.T) {
	h, reg := newTestAPI(t, false)
	requireStatus(t, doJSON(t, h, http.MethodPost, "/backend", backendBody("b1")), http.StatusCreated)
	rec := doJSON(t, h, http.MethodDelete, "/backend/b1", nil)
	requireStatus(t, rec, http.StatusOK)
	var drained drainResponse
	decode(t, rec, &drained)
	if !drained.Complete || drained.DrainedConnections != 0 {
		t.Fatalf("clean drain wrong: %+v", drained)
	}
	if _, _, _, err := reg.FindBackend("b1"); err == nil {
		t.Fatal("backend still present after clean delete")
	}
}

func TestPoolCRUD(t *testing.T) {
	h, _ := newTestAPI(t, false)

	// List includes seeded pool.
	rec := doJSON(t, h, http.MethodGet, "/pool", nil)
	requireStatus(t, rec, http.StatusOK)
	var listed listPoolsResponse
	decode(t, rec, &listed)
	if len(listed.Pools) != 1 || listed.Pools[0].PoolID != "web" || listed.Pools[0].Algorithm != config.StrategyRoundRobin {
		t.Fatalf("list=%+v", listed)
	}

	// Create with health policy + sticky.
	rec = doJSON(t, h, http.MethodPost, "/pool", map[string]any{
		"pool_id":       "grpc",
		"name":          "gRPC pool",
		"algorithm":     config.StrategyLeastConnections,
		"health_policy": map[string]any{"path": "/ping", "interval_seconds": 10, "timeout_seconds": 2},
		"sticky":        map[string]any{"enabled": true, "ttl_seconds": 60},
	})
	requireStatus(t, rec, http.StatusCreated)

	// Duplicate -> 409; bad algorithm -> 400; sticky without ttl -> 400.
	requireStatus(t, doJSON(t, h, http.MethodPost, "/pool", map[string]any{"pool_id": "grpc"}), http.StatusConflict)
	requireStatus(t, doJSON(t, h, http.MethodPost, "/pool",
		map[string]any{"pool_id": "x", "algorithm": "random"}), http.StatusBadRequest)
	requireStatus(t, doJSON(t, h, http.MethodPost, "/pool",
		map[string]any{"pool_id": "y", "sticky": map[string]any{"enabled": true}}), http.StatusBadRequest)

	// Get pool detail.
	rec = doJSON(t, h, http.MethodGet, "/pool/grpc", nil)
	requireStatus(t, rec, http.StatusOK)
	var detail poolResponse
	decode(t, rec, &detail)
	if detail.Pool.Name != "gRPC pool" || !detail.Pool.StickySessions.Enabled || detail.Pool.HealthPolicy.Path != "/ping" {
		t.Fatalf("detail=%+v", detail)
	}
	requireStatus(t, doJSON(t, h, http.MethodGet, "/pool/ghost", nil), http.StatusNotFound)
}

func TestDeletePoolDrainsMembers(t *testing.T) {
	h, reg := newTestAPI(t, false)
	requireStatus(t, doJSON(t, h, http.MethodPost, "/backend", backendBody("b1")), http.StatusCreated)
	requireStatus(t, doJSON(t, h, http.MethodPost, "/pool", map[string]any{"pool_id": "api"}), http.StatusCreated)

	rec := doJSON(t, h, http.MethodDelete, "/pool/web", nil)
	requireStatus(t, rec, http.StatusOK)
	var resp struct {
		APIVersion         string `json:"api_version"`
		PoolID             string `json:"pool_id"`
		DrainedConnections int    `json:"drained_connections"`
	}
	decode(t, rec, &resp)
	if resp.PoolID != "web" {
		t.Fatalf("resp=%+v", resp)
	}
	if _, ok := reg.Snapshot().Pool("web"); ok {
		t.Fatal("pool still present")
	}
	requireStatus(t, doJSON(t, h, http.MethodDelete, "/pool/api", nil), http.StatusOK)
	requireStatus(t, doJSON(t, h, http.MethodDelete, "/pool/ghost", nil), http.StatusNotFound)
}

func TestSetAlgorithmAppliesLive(t *testing.T) {
	h, reg := newTestAPI(t, false)
	requireStatus(t, doJSON(t, h, http.MethodPost, "/backend", backendBody("b1")), http.StatusCreated)

	rec := doJSON(t, h, http.MethodPut, "/config/algorithm", map[string]any{
		"algorithm": config.StrategyWeightedRR, "sticky_session": true, "sticky_ttl_seconds": 30,
	})
	requireStatus(t, rec, http.StatusOK)

	sticky, ok := reg.PoolSticky("web")
	if !ok || !sticky.Enabled || sticky.TTLSeconds != 30 {
		t.Fatalf("sticky not applied: %+v", sticky)
	}
	if strat, _ := reg.Snapshot().Strategy("web"); strat != config.StrategyWeightedRR {
		t.Fatalf("algorithm not live: %s", strat)
	}
}

func TestSetAlgorithmRejectsWithoutMutation(t *testing.T) {
	h, reg := newTestAPI(t, false)

	// Unknown strategy.
	requireStatus(t, doJSON(t, h, http.MethodPut, "/config/algorithm",
		map[string]any{"algorithm": "ip_hash"}), http.StatusBadRequest)
	// Sticky enabled without TTL must roll the whole PUT (algorithm included).
	requireStatus(t, doJSON(t, h, http.MethodPut, "/config/algorithm",
		map[string]any{"algorithm": config.StrategyLeastConnections, "sticky_session": true}), http.StatusBadRequest)

	if strat, _ := reg.Snapshot().Strategy("web"); strat != config.StrategyRoundRobin {
		t.Fatalf("rejected PUT changed live strategy: %s", strat)
	}
	sticky, _ := reg.PoolSticky("web")
	if sticky.Enabled {
		t.Fatal("rejected PUT enabled sticky sessions")
	}
	requireStatus(t, doJSON(t, h, http.MethodPut, "/config/algorithm",
		map[string]any{"pool": "ghost", "algorithm": config.StrategyRoundRobin}), http.StatusNotFound)
}

func TestGetConfigReturnsEffectiveDocument(t *testing.T) {
	h, _ := newTestAPI(t, false)
	requireStatus(t, doJSON(t, h, http.MethodPost, "/backend", backendBody("b1")), http.StatusCreated)

	rec := doJSON(t, h, http.MethodGet, "/config", nil)
	requireStatus(t, rec, http.StatusOK)
	var cfgResp configResponse
	decode(t, rec, &cfgResp)
	if len(cfgResp.Config.Pools) != 1 || len(cfgResp.Config.Backends) != 1 {
		t.Fatalf("config=%+v", cfgResp.Config)
	}
	b := cfgResp.Config.Backends[0]
	if b.Weight != config.DefaultBackendWeight || b.MaxConnections != config.NoConnectionLimit {
		t.Fatalf("defaults not materialized in GET /config: %+v", b)
	}
}

func TestHealthzReadyzMetrics(t *testing.T) {
	h, _ := newTestAPI(t, false)

	rec := doJSON(t, h, http.MethodGet, "/healthz", nil)
	requireStatus(t, rec, http.StatusOK)

	// Not serving (no data plane) -> readiness 503.
	requireStatus(t, doJSON(t, h, http.MethodGet, "/readyz", nil), http.StatusServiceUnavailable)

	serving, _ := newTestAPI(t, true)
	rec = doJSON(t, serving, http.MethodGet, "/readyz", nil)
	requireStatus(t, rec, http.StatusOK)

	rec = doJSON(t, serving, http.MethodGet, "/metrics", nil)
	requireStatus(t, rec, http.StatusOK)
	var metrics metricsResponse
	decode(t, rec, &metrics)
	if metrics.Requests.Total != 10 || metrics.Requests.Success != 9 || metrics.PoolCount != 1 {
		t.Fatalf("metrics=%+v", metrics)
	}
}

func TestMethodMismatchReturns405(t *testing.T) {
	h, _ := newTestAPI(t, false)
	req := httptest.NewRequest(http.MethodGet, "/backend", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405 got %d", rec.Code)
	}
}

func TestGeneratedBackendID(t *testing.T) {
	h, _ := newTestAPI(t, false)
	rec := doJSON(t, h, http.MethodPost, "/backend", map[string]any{"address": "10.0.0.5", "port": 3000})
	requireStatus(t, rec, http.StatusCreated)
	var resp createBackendResponse
	decode(t, rec, &resp)
	if resp.BackendID != "web-backend-1" {
		t.Fatalf("generated id=%q", resp.BackendID)
	}
}
