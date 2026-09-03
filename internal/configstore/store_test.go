package configstore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileStoreSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	store := NewFileStore(path)
	cfg := Config{
		Runtime: RuntimeConfig{
			HealthCheckIntervalSeconds: 10,
			HealthCheckPath:            "/health",
			StickySessionTTLSeconds:    30,
			StickySessionEnabled:       true,
			ListenerPort:               8080,
			ListenerHost:               "0.0.0.0",
		},
		Pools: map[string]PoolConfig{
			"pool-a": {
				ID:   "pool-a",
				Name: "pool-a",
				Backends: []BackendConfig{
					{ID: "backend-1", Address: "127.0.0.1", Port: 8081, Healthy: true},
				},
			},
		},
	}

	if err := store.Save(cfg); err != nil {
		t.Fatalf("Save returned err: %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load returned err: %v", err)
	}

	if loaded.Runtime.ListenerPort != 8080 {
		t.Fatalf("expected listener port 8080, got %d", loaded.Runtime.ListenerPort)
	}
	if len(loaded.Pools["pool-a"].Backends) != 1 {
		t.Fatalf("expected 1 backend, got %d", len(loaded.Pools["pool-a"].Backends))
	}
}

func TestFileStoreReturnsErrorForMissingConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "missing.json")

	store := NewFileStore(path)
	if _, err := store.Load(); err == nil {
		t.Fatal("expected error for missing config file")
	}
}

func TestFileStoreRejectsInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad-config.json")

	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("WriteFile returned err: %v", err)
	}

	store := NewFileStore(path)
	if _, err := store.Load(); err == nil {
		t.Fatal("expected error for invalid JSON file")
	}
}
