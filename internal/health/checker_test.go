package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/marcusaroldan/load-balancer/internal/pool"
)

func TestCheckerMarksHealthyBackendAsHealthy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Fatalf("expected /health path, got %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	store := pool.NewInMemoryStore()
	if err := store.AddBackend("pool-a", pool.Backend{ID: "backend-1", Address: server.URL, Port: 0, Healthy: false}); err != nil {
		t.Fatalf("AddBackend returned err: %v", err)
	}

	checker := NewChecker(store, 50*time.Millisecond, "/health")
	if err := checker.CheckPool("pool-a"); err != nil {
		t.Fatalf("CheckPool returned err: %v", err)
	}

	p, ok := store.GetPool("pool-a")
	if !ok {
		t.Fatal("expected pool to exist")
	}
	backend := p.Backends["backend-1"]
	if backend == nil || !backend.Healthy {
		t.Fatal("expected backend to be healthy after successful health check")
	}
}

func TestCheckerMarksFailedBackendAsUnhealthy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	store := pool.NewInMemoryStore()
	if err := store.AddBackend("pool-a", pool.Backend{ID: "backend-1", Address: server.URL, Port: 0, Healthy: true}); err != nil {
		t.Fatalf("AddBackend returned err: %v", err)
	}

	checker := NewChecker(store, 50*time.Millisecond, "/health")
	if err := checker.CheckPool("pool-a"); err != nil {
		t.Fatalf("CheckPool returned err: %v", err)
	}

	p, ok := store.GetPool("pool-a")
	if !ok {
		t.Fatal("expected pool to exist")
	}
	backend := p.Backends["backend-1"]
	if backend == nil || backend.Healthy {
		t.Fatal("expected backend to be unhealthy after failed health check")
	}
}

func TestCheckerStartAndStopLifecycle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	store := pool.NewInMemoryStore()
	if err := store.AddBackend("pool-a", pool.Backend{ID: "backend-1", Address: server.URL, Port: 0, Healthy: false}); err != nil {
		t.Fatalf("AddBackend returned err: %v", err)
	}

	checker := NewChecker(store, 20*time.Millisecond, "/health")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	checker.Start(ctx)
	time.Sleep(80 * time.Millisecond)
	checker.Stop()

	p, ok := store.GetPool("pool-a")
	if !ok {
		t.Fatal("expected pool to exist")
	}
	backend := p.Backends["backend-1"]
	if backend == nil || !backend.Healthy {
		t.Fatal("expected backend to become healthy during the started lifecycle")
	}
}
