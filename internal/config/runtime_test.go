package config

import (
	"errors"
	"testing"
)

func baseValidConfig() Config {
	return Config{
		DataPlane:           ListenerConfig{Host: "127.0.0.1", Port: 8080},
		ControlPlane:        ListenerConfig{Host: "127.0.0.1", Port: 9090},
		DrainTimeoutSeconds: defaultDrainSec,
		Pools: []PoolConfig{{
			ID:        "web",
			Name:      "web",
			Algorithm: StrategyRoundRobin,
			HealthPolicy: HealthPolicy{
				Path:               "/health",
				IntervalSeconds:    5,
				TimeoutSeconds:     2,
				HealthyThreshold:   1,
				UnhealthyThreshold: 3,
			},
		}},
		Backends: []BackendConfig{{
			ID:             "b1",
			PoolID:         "web",
			Address:        "10.0.0.5",
			Port:           3000,
			Weight:         DefaultBackendWeight,
			MaxConnections: NoConnectionLimit,
			Enabled:        true,
		}},
	}
}

func TestValidateAcceptsBaseline(t *testing.T) {
	cfg := baseValidConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("baseline config rejected: %v", err)
	}
}

func TestValidateRejectsInvalidFields(t *testing.T) {
	mutate := func(fn func(*Config)) Config {
		cfg := baseValidConfig()
		fn(&cfg)
		return cfg
	}

	tests := []struct {
		name     string
		cfg      Config
		sentinel error
	}{
		{
			name: "data plane port zero",
			cfg: mutate(func(c *Config) {
				c.DataPlane.Port = 0
			}),
			sentinel: ErrInvalidPort,
		},
		{
			name: "backend port above max",
			cfg: mutate(func(c *Config) {
				c.Backends[0].Port = MaxPort + 1
			}),
			sentinel: ErrInvalidPort,
		},
		{
			name: "duplicate pool ids",
			cfg: mutate(func(c *Config) {
				c.Pools = append(c.Pools, PoolConfig{ID: "web", Algorithm: StrategyRoundRobin})
			}),
			sentinel: ErrDuplicateID,
		},
		{
			name: "duplicate backend ids",
			cfg: mutate(func(c *Config) {
				c.Backends = append(c.Backends, c.Backends[0])
			}),
			sentinel: ErrDuplicateID,
		},
		{
			name: "backend references unknown pool",
			cfg: mutate(func(c *Config) {
				c.Backends[0].PoolID = "ghost"
			}),
			sentinel: ErrMissingPool,
		},
		{
			name: "zero weight",
			cfg: mutate(func(c *Config) {
				c.Backends[0].Weight = 0
			}),
			sentinel: ErrInvalidWeight,
		},
		{
			name: "weight above max",
			cfg: mutate(func(c *Config) {
				c.Backends[0].Weight = MaxWeight + 1
			}),
			sentinel: ErrInvalidWeight,
		},
		{
			name: "max_connections below limit floor",
			cfg: mutate(func(c *Config) {
				c.Backends[0].MaxConnections = 0
			}),
			sentinel: ErrInvalidWeight,
		},
		{
			name: "healthy threshold above max",
			cfg: mutate(func(c *Config) {
				c.Pools[0].HealthPolicy.HealthyThreshold = MaxHealthyThreshold + 1
			}),
			sentinel: ErrInvalidThreshold,
		},
		{
			name: "unhealthy threshold above max",
			cfg: mutate(func(c *Config) {
				c.Pools[0].HealthPolicy.UnhealthyThreshold = MaxUnhealthyThreshold + 1
			}),
			sentinel: ErrInvalidThreshold,
		},
		{
			name: "health timeout exceeds interval",
			cfg: mutate(func(c *Config) {
				c.Pools[0].HealthPolicy.TimeoutSeconds = 6
			}),
			sentinel: ErrInvalidDuration,
		},
		{
			name: "health interval above max",
			cfg: mutate(func(c *Config) {
				c.Pools[0].HealthPolicy.IntervalSeconds = maxHealthIntervalSec + 1
			}),
			sentinel: ErrInvalidDuration,
		},
		{
			name: "unsupported algorithm",
			cfg: mutate(func(c *Config) {
				c.Pools[0].Algorithm = "ip_hash"
			}),
			sentinel: ErrUnknownStrategy,
		},
		{
			name: "sticky enabled without ttl",
			cfg: mutate(func(c *Config) {
				c.Pools[0].StickySessions = StickyConfig{Enabled: true}
			}),
			sentinel: ErrStickyTTLRequired,
		},
		{
			name: "sticky ttl above max",
			cfg: mutate(func(c *Config) {
				c.Pools[0].StickySessions = StickyConfig{Enabled: true, TTLSeconds: maxStickyTTLSec + 1}
			}),
			sentinel: ErrInvalidDuration,
		},
		{
			name: "empty pool id",
			cfg: mutate(func(c *Config) {
				c.Pools[0].ID = ""
			}),
			sentinel: ErrInvalidID,
		},
		{
			name: "pool id with path separators",
			cfg: mutate(func(c *Config) {
				c.Pools[0].ID = "web/../evil"
			}),
			sentinel: ErrInvalidID,
		},
		{
			name: "backend id with whitespace",
			cfg: mutate(func(c *Config) {
				c.Backends[0].ID = "bad id"
			}),
			sentinel: ErrInvalidID,
		},
		{
			name: "empty backend address",
			cfg: mutate(func(c *Config) {
				c.Backends[0].Address = ""
			}),
			sentinel: ErrInvalidAddress,
		},
		{
			name: "backend address with control character",
			cfg: mutate(func(c *Config) {
				c.Backends[0].Address = "host\ninject"
			}),
			sentinel: ErrInvalidAddress,
		},
		{
			name: "health path missing leading slash",
			cfg: mutate(func(c *Config) {
				c.Pools[0].HealthPolicy.Path = "health"
			}),
			sentinel: ErrInvalidPath,
		},
		{
			name: "drain timeout above max",
			cfg: mutate(func(c *Config) {
				c.DrainTimeoutSeconds = maxDrainSec + 1
			}),
			sentinel: ErrInvalidDuration,
		},
		{
			name: "empty listener host",
			cfg: mutate(func(c *Config) {
				c.ControlPlane.Host = ""
			}),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if tc.sentinel != nil && !errors.Is(err, tc.sentinel) {
				t.Fatalf("error %v does not wrap sentinel %v", err, tc.sentinel)
			}
		})
	}
}

func TestApplyDefaultsPopulatesDocumentedValues(t *testing.T) {
	cfg := Config{
		Pools: []PoolConfig{{ID: "api", Algorithm: StrategyLeastConnections}},
		Backends: []BackendConfig{{
			ID:      "a1",
			PoolID:  "api",
			Address: "10.0.0.9",
			Port:    8000,
			Enabled: true,
		}},
	}
	cfg.ApplyDefaults()

	if cfg.DataPlane.Port != defaultListenPort || cfg.DataPlane.Host != defaultListenHost {
		t.Errorf("data plane defaults: %+v", cfg.DataPlane)
	}
	if cfg.ControlPlane.Port != defaultControlPlanePort {
		t.Errorf("control plane port default: %+v", cfg.ControlPlane)
	}
	if cfg.DrainTimeoutSeconds != defaultDrainSec {
		t.Errorf("drain default: %d", cfg.DrainTimeoutSeconds)
	}

	pool := cfg.Pools[0]
	if pool.Name != "api" {
		t.Errorf("pool name should default to id, got %q", pool.Name)
	}
	if pool.HealthPolicy.Path != defaultHealthCheckPath ||
		pool.HealthPolicy.IntervalSeconds != defaultHealthIntervalSec ||
		pool.HealthPolicy.TimeoutSeconds != defaultHealthTimeoutSec ||
		pool.HealthPolicy.HealthyThreshold != defaultHealthyThresh ||
		pool.HealthPolicy.UnhealthyThreshold != defaultUnhealthyThresh {
		t.Errorf("pool health defaults: %+v", pool.HealthPolicy)
	}

	backend := cfg.Backends[0]
	if backend.Weight != DefaultBackendWeight {
		t.Errorf("backend weight default: %d", backend.Weight)
	}
	if backend.MaxConnections != NoConnectionLimit {
		t.Errorf("backend max connections default: %d", backend.MaxConnections)
	}
	if backend.HealthPolicy.IntervalSeconds != defaultHealthIntervalSec {
		t.Errorf("backend health policy should inherit pool policy, got %+v", backend.HealthPolicy)
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaulted config must validate, got %v", err)
	}
}

func TestApplyDefaultsPreservesExplicitValues(t *testing.T) {
	cfg := Config{
		DataPlane:           ListenerConfig{Host: "10.1.1.1", Port: 1234},
		ControlPlane:        ListenerConfig{Host: "127.0.0.1", Port: 4321},
		DrainTimeoutSeconds: 45,
		Pools: []PoolConfig{{
			ID:        "web",
			Name:      "Public Web",
			Algorithm: StrategyWeightedRR,
			HealthPolicy: HealthPolicy{
				Path:            "/ping",
				IntervalSeconds: 10,
				TimeoutSeconds:  3,
			},
		}},
		Backends: []BackendConfig{{
			ID:             "b1",
			PoolID:         "web",
			Address:        "10.0.0.5",
			Port:           3000,
			Weight:         7,
			MaxConnections: 100,
			Enabled:        true,
			HealthPolicy:   HealthPolicy{IntervalSeconds: 20, TimeoutSeconds: 5},
		}},
	}
	cfg.ApplyDefaults()

	if cfg.DataPlane.Port != 1234 || cfg.ControlPlane.Port != 4321 || cfg.DrainTimeoutSeconds != 45 {
		t.Errorf("explicit listener/drain values were overwritten")
	}
	if cfg.Pools[0].Name != "Public Web" || cfg.Pools[0].Algorithm != StrategyWeightedRR {
		t.Errorf("explicit pool fields overwritten")
	}
	if cfg.Pools[0].HealthPolicy.Path != "/ping" || cfg.Pools[0].HealthPolicy.IntervalSeconds != 10 {
		t.Errorf("explicit pool health fields overwritten: %+v", cfg.Pools[0].HealthPolicy)
	}
	b := cfg.Backends[0]
	if b.Weight != 7 || b.MaxConnections != 100 {
		t.Errorf("explicit backend fields overwritten")
	}
	// Backend override keeps its own interval/timeout but inherits remaining pool fields.
	if b.HealthPolicy.IntervalSeconds != 20 || b.HealthPolicy.TimeoutSeconds != 5 {
		t.Errorf("backend health overrides lost: %+v", b.HealthPolicy)
	}
	if b.HealthPolicy.Path != "/ping" || b.HealthPolicy.UnhealthyThreshold != defaultUnhealthyThresh {
		t.Errorf("backend did not inherit pool health policy for unset fields: %+v", b.HealthPolicy)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config with explicit values must validate, got %v", err)
	}
}

func TestSupportedStrategiesAreAccepted(t *testing.T) {
	for _, strategy := range SupportedStrategies() {
		cfg := baseValidConfig()
		cfg.Pools[0].Algorithm = strategy
		if err := cfg.Validate(); err != nil {
			t.Errorf("supported strategy %q rejected: %v", strategy, err)
		}
	}
}

func TestDefaultConfigIsValid(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Default() must be valid, got %v", err)
	}
}
