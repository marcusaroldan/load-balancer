package configstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const defaultConfigFile = "config.json"

// RuntimeConfig contains operational settings used by the load balancer.
type RuntimeConfig struct {
	HealthCheckIntervalSeconds int    `json:"health_check_interval_seconds"`
	HealthCheckPath            string `json:"health_check_path"`
	StickySessionTTLSeconds    int    `json:"sticky_session_ttl_seconds"`
	StickySessionEnabled       bool   `json:"sticky_session_enabled"`
	ListenerPort               int    `json:"listener_port"`
	ListenerHost               string `json:"listener_host"`
}

// BackendConfig is the serialized representation of a backend.
type BackendConfig struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Port    int    `json:"port"`
	Healthy bool   `json:"healthy"`
}

// PoolConfig is the serialized representation of a pool and its members.
type PoolConfig struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Backends []BackendConfig `json:"backends"`
}

// Config is the full persisted configuration for the load balancer.
type Config struct {
	Runtime RuntimeConfig         `json:"runtime"`
	Pools   map[string]PoolConfig `json:"pools"`
}

// Store is the persistence interface for config data.
type Store interface {
	Save(cfg Config) error
	Load() (Config, error)
}

// FileStore saves and loads config from a JSON file.
type FileStore struct {
	path string
}

// NewFileStore creates a file-backed config store using a default config.json path.
func NewFileStore(path string) *FileStore {
	if path == "" {
		path = defaultConfigFile
	}

	return &FileStore{path: filepath.Clean(path)}
}

// Save writes the config to disk as JSON.
func (f *FileStore) Save(cfg Config) error {
	if f.path == "" {
		return fmt.Errorf("config path is required")
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(f.path, data, 0o600); err != nil {
		return fmt.Errorf("write config file: %w", err)
	}

	return nil
}

// Load reads and decodes a config file from disk.
func (f *FileStore) Load() (Config, error) {
	if f.path == "" {
		return Config{}, fmt.Errorf("config path is required")
	}

	data, err := os.ReadFile(f.path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, fmt.Errorf("config file does not exist: %s", f.path)
		}
		return Config{}, fmt.Errorf("read config file: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode config file: %w", err)
	}

	return cfg, nil
}
