package controlplane

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/marcusaroldan/load-balancer/internal/pool"
)

func TestCreateBackend(t *testing.T) {
	store := pool.NewInMemoryStore()
	handler := NewHandler(store)

	body := bytes.NewBufferString(`{
		"backend_id": "backend-1",
		"pool_id": "pool-a",
		"address": "127.0.0.1",
		"port": 8081
	}`)

	req := httptest.NewRequest(http.MethodPost, "/backend", body)
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", res.Code)
	}

	var response CreateBackendResponse
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode JSON: %v", err)
	}
	if response.ID != "backend-1" {
		t.Fatalf("expected backend ID backend-1, got %q", response.ID)
	}
	if response.PoolID != "pool-a" {
		t.Fatalf("expected pool ID pool-a, got %q", response.PoolID)
	}
}

func TestCreateBackendRejectsInvalidRequest(t *testing.T) {
	store := pool.NewInMemoryStore()
	handler := NewHandler(store)

	body := bytes.NewBufferString(`{"backend_id":"","pool_id":"pool-a","address":"127.0.0.1","port":0}`)
	req := httptest.NewRequest(http.MethodPost, "/backend", body)
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", res.Code)
	}
}

func TestGetPoolReturnsPoolMembers(t *testing.T) {
	store := pool.NewInMemoryStore()
	if err := store.AddBackend("pool-a", pool.Backend{ID: "backend-1", Address: "127.0.0.1", Port: 8081, Healthy: true}); err != nil {
		t.Fatalf("AddBackend returned err: %v", err)
	}
	if err := store.AddBackend("pool-a", pool.Backend{ID: "backend-2", Address: "127.0.0.1", Port: 8082, Healthy: false}); err != nil {
		t.Fatalf("AddBackend returned err: %v", err)
	}

	handler := NewHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/pool/pool-a", nil)
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.Code)
	}

	var response PoolResponse
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode JSON: %v", err)
	}
	if response.ID != "pool-a" {
		t.Fatalf("expected pool id pool-a, got %q", response.ID)
	}
	if len(response.Backends) != 2 {
		t.Fatalf("expected 2 backends, got %d", len(response.Backends))
	}
}

func TestGetPoolMissingReturnsNotFound(t *testing.T) {
	store := pool.NewInMemoryStore()
	handler := NewHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/pool/missing", nil)
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	if res.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", res.Code)
	}
}

func TestCreateBackendRejectsDuplicateBackendID(t *testing.T) {
	store := pool.NewInMemoryStore()
	handler := NewHandler(store)

	if err := store.AddBackend("pool-a", pool.Backend{ID: "backend-1", Address: "127.0.0.1", Port: 8081, Healthy: true}); err != nil {
		t.Fatalf("seed AddBackend returned err: %v", err)
	}

	body := bytes.NewBufferString(`{"backend_id":"backend-1","pool_id":"pool-a","address":"127.0.0.1","port":8082}`)
	req := httptest.NewRequest(http.MethodPost, "/backend", body)
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	if res.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", res.Code)
	}
}
