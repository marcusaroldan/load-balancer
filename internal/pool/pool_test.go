package pool

import "testing"

func TestAddBackendCreatesPoolAndStoresBackend(t *testing.T) {
	store := NewInMemoryStore()

	backend := Backend{
		ID:      "backend-1",
		Address: "http://127.0.0.1",
		Port:    8081,
	}

	if err := store.AddBackend("pool-a", backend); err != nil {
		t.Fatalf("AddBackend returned err: %v", err)
	}

	pool, ok := store.GetPool("pool-a")
	if !ok {
		t.Fatal("expected pool to exist")
	}

	if len(pool.Backends) != 1 {
		t.Fatalf("expected 1 backend, got %d", len(pool.Backends))
	}

	stored, exists := pool.Backends["backend-1"]
	if !exists {
		t.Fatal("expected backend to exist in pool")
	}
	if stored == nil {
		t.Fatal("expected backend pointer to be non-nil")
	}
	if stored.PoolID != "pool-a" {
		t.Fatalf("expected backend PoolID pool-a, got %q", stored.PoolID)
	}
}

func TestAddBackendRejectsDuplicateID(t *testing.T) {
	store := NewInMemoryStore()
	backend := Backend{ID: "backend-1", Address: "http://127.0.0.1", Port: 8081}

	if err := store.AddBackend("pool-a", backend); err != nil {
		t.Fatalf("first AddBackend returned err: %v", err)
	}

	if err := store.AddBackend("pool-a", backend); err == nil {
		t.Fatal("expected duplicate backend to be rejected")
	}
}

func TestRemoveBackendRemovesFromPool(t *testing.T) {
	store := NewInMemoryStore()
	backend := Backend{ID: "backend-1", Address: "http://127.0.0.1", Port: 8081}

	if err := store.AddBackend("pool-a", backend); err != nil {
		t.Fatalf("AddBackend returned err: %v", err)
	}

	if err := store.RemoveBackend("pool-a", "backend-1"); err != nil {
		t.Fatalf("RemoveBackend returned err: %v", err)
	}

	backends := store.ListBackends("pool-a")
	if len(backends) != 0 {
		t.Fatalf("expected no backends after removal, got %d", len(backends))
	}
}

func TestListBackendsReturnsAllBackendsInPool(t *testing.T) {
	store := NewInMemoryStore()

	for _, backend := range []Backend{
		{ID: "backend-1", Address: "http://127.0.0.1", Port: 8081},
		{ID: "backend-2", Address: "http://127.0.0.1", Port: 8082},
	} {
		if err := store.AddBackend("pool-a", backend); err != nil {
			t.Fatalf("AddBackend returned err: %v", err)
		}
	}

	backends := store.ListBackends("pool-a")
	if len(backends) != 2 {
		t.Fatalf("expected 2 backends, got %d", len(backends))
	}
}

func TestGetPoolReturnsFalseForMissingPool(t *testing.T) {
	store := NewInMemoryStore()

	if _, ok := store.GetPool("missing-pool"); ok {
		t.Fatal("expected missing pool to return false")
	}
}

func TestListPoolsReturnsAllPools(t *testing.T) {
	store := NewInMemoryStore()
	if err := store.AddBackend("pool-a", Backend{ID: "backend-1", Address: "http://127.0.0.1", Port: 8081}); err != nil {
		t.Fatalf("AddBackend returned err: %v", err)
	}
	if err := store.AddBackend("pool-b", Backend{ID: "backend-2", Address: "http://127.0.0.1", Port: 8082}); err != nil {
		t.Fatalf("AddBackend returned err: %v", err)
	}

	pools := store.ListPools()
	if len(pools) != 2 {
		t.Fatalf("expected 2 pools, got %d", len(pools))
	}
}
