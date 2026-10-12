package config

import (
	"errors"
	"testing"
)

func validPoolHostConfig(host string) Config {
	return Config{
		DataPlane:    ListenerConfig{Host: "0.0.0.0", Port: 8080},
		ControlPlane: ListenerConfig{Host: "0.0.0.0", Port: 8082},
		Pools: []PoolConfig{
			{ID: "api", Host: host, Algorithm: StrategyRoundRobin},
		},
		Backends: []BackendConfig{
			{ID: "b1", PoolID: "api", Address: "10.0.0.1", Port: 9001, Weight: 1},
		},
	}
}

func TestPoolHostAccepted(t *testing.T) {
	for _, host := range []string{"api.example.com", "Api.Example.Com", "lb-1.internal", "10.0.0.1", " deep-space.example.io "} {
		cfg := validPoolHostConfig(host)
		cfg.ApplyDefaults()
		if err := cfg.Validate(); err != nil {
			t.Fatalf("host %q: unexpected error: %v", host, err)
		}
		normalized := cfg.Pools[0].Host
		if normalized != "api.example.com" && normalized != "lb-1.internal" && normalized != "10.0.0.1" && normalized != "deep-space.example.io" {
			t.Fatalf("host %q not normalized, got %q", host, normalized)
		}
	}
}

func TestPoolHostRejected(t *testing.T) {
	cases := map[string]string{
		"":                "empty is allowed (unset), tested separately",
		"http://x.com":    "scheme",
		"x.com:8080":      "port",
		"x.com/path":      "path",
		"*.example.com":   "wildcard",
		"-bad.example.io": "leading hyphen",
		"bad..example.io": "empty label",
	}
	for host, why := range cases {
		if host == "" {
			continue
		}
		cfg := validPoolHostConfig(host)
		cfg.ApplyDefaults()
		if err := cfg.Validate(); !errors.Is(err, ErrInvalidHost) {
			t.Fatalf("host %q (%s): got %v, want ErrInvalidHost", host, why, err)
		}
	}
}

func TestPoolHostDuplicateRejected(t *testing.T) {
	cfg := Config{
		DataPlane:    ListenerConfig{Host: "0.0.0.0", Port: 8080},
		ControlPlane: ListenerConfig{Host: "0.0.0.0", Port: 8082},
		Pools: []PoolConfig{
			{ID: "a", Host: "same.example.com", Algorithm: StrategyRoundRobin},
			{ID: "b", Host: "SAME.example.COM", Algorithm: StrategyRoundRobin},
		},
		Backends: []BackendConfig{
			{ID: "b1", PoolID: "a", Address: "10.0.0.1", Port: 9001, Weight: 1},
			{ID: "b2", PoolID: "b", Address: "10.0.0.2", Port: 9001, Weight: 1},
		},
	}
	cfg.ApplyDefaults()
	err := cfg.Validate()
	if !errors.Is(err, ErrDuplicateHost) {
		t.Fatalf("duplicate host: got %v, want ErrDuplicateHost", err)
	}
}

func TestPoolHostUnsetIsValid(t *testing.T) {
	cfg := validPoolHostConfig("")
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unset host must validate: %v", err)
	}
}
