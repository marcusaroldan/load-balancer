// Package config defines the canonical desired-state configuration model for
// the standalone load balancer runtime (PROD-001). It covers listeners, pools,
// backends, health policy, routing policy, sticky sessions, and graceful drain.
//
// Observed/runtime state (health counters, active connections) is intentionally
// NOT modeled here; see planning/standalone-production-load-balancer-plan.md.
package config

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Routing strategy names accepted by the runtime. These mirror the constants in
// internal/routing; config validation rejects anything outside this set.
const (
	StrategyRoundRobin       = "round_robin"
	StrategyWeightedRR       = "weighted_round_robin"
	StrategyLeastConnections = "least_connections"
)

// Validation bounds. Named to avoid magic numbers in rules and tests.
const (
	MinPort               = 1
	MaxPort               = 65535
	DefaultBackendWeight  = 1
	NoConnectionLimit     = -1
	MinWeight             = 1
	MaxWeight             = 100
	MinHealthyThreshold   = 1
	MaxHealthyThreshold   = 10
	MinUnhealthyThreshold = 1
	MaxUnhealthyThreshold = 10
	MaxActiveBackends     = MaxPort // upper bound for per-backend connection limits

	minHealthIntervalSec     = 1
	maxHealthIntervalSec     = 300
	minHealthTimeoutSec      = 1 // sub-second timeouts unsupported until needed
	maxHealthTimeoutSec      = 60
	minStickyTTLSec          = 1
	maxStickyTTLSec          = 86400
	minDrainSec              = 1
	maxDrainSec              = 600
	minGraceSec              = 0
	maxGraceSec              = 300
	defaultDrainSec          = 30
	defaultHealthIntervalSec = 5
	defaultHealthTimeoutSec  = 2

	maxNameLength           = 128
	defaultHealthCheckPath  = "/health"
	defaultHealthyThresh    = 1
	defaultUnhealthyThresh  = 3
	defaultListenHost       = "0.0.0.0"
	defaultListenPort       = 8080
	defaultControlPlanePort = 8082
)

// idPattern constrains pool/backend identifiers to URL-safe tokens so they can
// appear in control-plane paths without escaping.
var idPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// Sentinel validation errors. Callers may match with errors.Is; details are
// wrapped with field context via fmt.Errorf("%w: ...").
var (
	ErrInvalidPort       = errors.New("invalid port")
	ErrDuplicateID       = errors.New("duplicate id")
	ErrMissingPool       = errors.New("references unknown pool")
	ErrInvalidWeight     = errors.New("invalid weight")
	ErrInvalidThreshold  = errors.New("invalid health threshold")
	ErrInvalidDuration   = errors.New("invalid duration")
	ErrUnknownStrategy   = errors.New("unsupported algorithm")
	ErrInvalidAddress    = errors.New("invalid address")
	ErrInvalidID         = errors.New("invalid id")
	ErrInvalidName       = errors.New("invalid name")
	ErrInvalidPath       = errors.New("invalid health check path")
	ErrStickyTTLRequired = errors.New("sticky session ttl required when sticky sessions enabled")
)

// SupportedStrategies lists the algorithm values accepted at every entry point
// (CLI defaults, API payloads, persisted files). ip_hash and random are
// deliberately absent until specified and tested (PROD plan).
func SupportedStrategies() []string {
	return []string{StrategyRoundRobin, StrategyWeightedRR, StrategyLeastConnections}
}

func isSupportedStrategy(name string) bool {
	for _, s := range SupportedStrategies() {
		if s == name {
			return true
		}
	}
	return false
}

// BackendConfig is the desired state of one upstream server.
type BackendConfig struct {
	ID             string       `json:"id"`
	PoolID         string       `json:"pool_id"`
	Address        string       `json:"address"`
	Port           int          `json:"port"`
	Weight         int          `json:"weight,omitempty"`
	MaxConnections int          `json:"max_connections,omitempty"` // NoConnectionLimit (-1) means unlimited
	Enabled        bool         `json:"enabled"`
	HealthPolicy   HealthPolicy `json:"health_policy,omitempty"`
}

// HealthPolicy configures active probing for a pool or backend override.
// Zero-valued fields fall back to the parent/default policy during normalization.
type HealthPolicy struct {
	Path               string `json:"path,omitempty"`
	IntervalSeconds    int    `json:"interval_seconds,omitempty"`
	TimeoutSeconds     int    `json:"timeout_seconds,omitempty"`
	HealthyThreshold   int    `json:"healthy_threshold,omitempty"`
	UnhealthyThreshold int    `json:"unhealthy_threshold,omitempty"`
	GraceSeconds       int    `json:"grace_seconds,omitempty"`
}

// Interval reports the probe interval as a duration.
func (h HealthPolicy) Interval() time.Duration { return time.Duration(h.IntervalSeconds) * time.Second }

// Timeout reports the probe timeout as a duration.
func (h HealthPolicy) Timeout() time.Duration { return time.Duration(h.TimeoutSeconds) * time.Second }

// Grace reports the startup grace period as a duration.
func (h HealthPolicy) Grace() time.Duration { return time.Duration(h.GraceSeconds) * time.Second }

// StickyConfig enables and bounds cookie-based session affinity.
type StickyConfig struct {
	Enabled    bool `json:"enabled"`
	TTLSeconds int  `json:"ttl_seconds,omitempty"`
}

// TTL reports the sticky session lifetime as a duration.
func (s StickyConfig) TTL() time.Duration { return time.Duration(s.TTLSeconds) * time.Second }

// PoolConfig is the desired state of one backend pool.
type PoolConfig struct {
	ID             string       `json:"id"`
	Name           string       `json:"name,omitempty"`
	Algorithm      string       `json:"algorithm"`
	HealthPolicy   HealthPolicy `json:"health_policy,omitempty"`
	StickySessions StickyConfig `json:"sticky_sessions,omitempty"`
}

// ListenerConfig binds one plane (data or control) to an address.
type ListenerConfig struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// Config is the complete canonical desired-state document for the runtime.
type Config struct {
	DataPlane           ListenerConfig  `json:"data_plane"`
	ControlPlane        ListenerConfig  `json:"control_plane"`
	DrainTimeoutSeconds int             `json:"drain_timeout_seconds,omitempty"`
	Pools               []PoolConfig    `json:"pools"`
	Backends            []BackendConfig `json:"backends"`
}

// DefaultHealthPolicy returns the documented system defaults for active probes.
func DefaultHealthPolicy() HealthPolicy {
	return HealthPolicy{
		Path:               defaultHealthCheckPath,
		IntervalSeconds:    defaultHealthIntervalSec,
		TimeoutSeconds:     defaultHealthTimeoutSec,
		HealthyThreshold:   defaultHealthyThresh,
		UnhealthyThreshold: defaultUnhealthyThresh,
		GraceSeconds:       minGraceSec,
	}
}

// Default returns a minimal valid configuration: standard listeners, no pools.
func Default() Config {
	return Config{
		DataPlane:           ListenerConfig{Host: defaultListenHost, Port: defaultListenPort},
		ControlPlane:        ListenerConfig{Host: defaultListenHost, Port: defaultControlPlanePort},
		DrainTimeoutSeconds: defaultDrainSec,
	}
}

// ApplyDefaults fills zero-valued fields with documented defaults. It is the
// single source of defaulting shared by CLI flags, API handlers, and file load,
// so all three entry points normalize identically.
func (c *Config) ApplyDefaults() {
	if c.DataPlane.Host == "" {
		c.DataPlane.Host = defaultListenHost
	}
	if c.DataPlane.Port == 0 {
		c.DataPlane.Port = defaultListenPort
	}
	if c.ControlPlane.Host == "" {
		c.ControlPlane.Host = defaultListenHost
	}
	if c.ControlPlane.Port == 0 {
		c.ControlPlane.Port = defaultControlPlanePort
	}
	if c.DrainTimeoutSeconds == 0 {
		c.DrainTimeoutSeconds = defaultDrainSec
	}

	poolDefaults := DefaultHealthPolicy()
	for i := range c.Pools {
		pool := &c.Pools[i]
		if pool.Algorithm == "" {
			pool.Algorithm = StrategyRoundRobin
		}
		if pool.Name == "" {
			pool.Name = pool.ID
		}
		pool.HealthPolicy = mergeHealthPolicy(poolDefaults, pool.HealthPolicy)
	}
	for i := range c.Backends {
		b := &c.Backends[i]
		if b.Weight == 0 {
			b.Weight = DefaultBackendWeight
		}
		if b.MaxConnections == 0 {
			b.MaxConnections = NoConnectionLimit
		}
		b.HealthPolicy = mergeHealthPolicy(c.poolPolicy(b.PoolID), b.HealthPolicy)
	}
}

// poolPolicy finds a pool's normalized health policy; falls back to defaults.
func (c *Config) poolPolicy(poolID string) HealthPolicy {
	for _, p := range c.Pools {
		if p.ID == poolID {
			return p.HealthPolicy
		}
	}
	return DefaultHealthPolicy()
}

// mergeHealthPolicy overlays sparse onto base, field by field.
func mergeHealthPolicy(base, sparse HealthPolicy) HealthPolicy {
	merged := base
	if sparse.Path != "" {
		merged.Path = sparse.Path
	}
	if sparse.IntervalSeconds != 0 {
		merged.IntervalSeconds = sparse.IntervalSeconds
	}
	if sparse.TimeoutSeconds != 0 {
		merged.TimeoutSeconds = sparse.TimeoutSeconds
	}
	if sparse.HealthyThreshold != 0 {
		merged.HealthyThreshold = sparse.HealthyThreshold
	}
	if sparse.UnhealthyThreshold != 0 {
		merged.UnhealthyThreshold = sparse.UnhealthyThreshold
	}
	if sparse.GraceSeconds != 0 {
		merged.GraceSeconds = sparse.GraceSeconds
	}
	return merged
}

// Validate checks the full desired-state document and returns the first error
// found, wrapped with the offending resource. Call ApplyDefaults first when
// defaults are expected. Unknown algorithms are rejected, never defaulted.
func (c Config) Validate() error {
	if err := validateListener("data plane", c.DataPlane); err != nil {
		return err
	}
	if err := validateListener("control plane", c.ControlPlane); err != nil {
		return err
	}
	if err := validateRange("drain_timeout_seconds", c.DrainTimeoutSeconds, minDrainSec, maxDrainSec, ErrInvalidDuration); err != nil {
		return err
	}

	seenPools := make(map[string]struct{}, len(c.Pools))
	for _, pool := range c.Pools {
		if err := validateID("pool", pool.ID); err != nil {
			return err
		}
		if _, dup := seenPools[pool.ID]; dup {
			return fmt.Errorf("%w: pool %q", ErrDuplicateID, pool.ID)
		}
		seenPools[pool.ID] = struct{}{}
		if err := validateName("pool", pool.ID, pool.Name); err != nil {
			return err
		}
		if !isSupportedStrategy(pool.Algorithm) {
			return fmt.Errorf("%w: pool %q algorithm %q", ErrUnknownStrategy, pool.ID, pool.Algorithm)
		}
		if err := validateHealthPolicy("pool "+pool.ID, pool.HealthPolicy); err != nil {
			return err
		}
		if err := validateSticky("pool "+pool.ID, pool.StickySessions); err != nil {
			return err
		}
	}

	seenBackends := make(map[string]struct{}, len(c.Backends))
	for _, b := range c.Backends {
		if err := validateID("backend", b.ID); err != nil {
			return err
		}
		if _, dup := seenBackends[b.ID]; dup {
			return fmt.Errorf("%w: backend %q", ErrDuplicateID, b.ID)
		}
		seenBackends[b.ID] = struct{}{}
		if _, ok := seenPools[b.PoolID]; !ok {
			return fmt.Errorf("%w: backend %q references pool %q", ErrMissingPool, b.ID, b.PoolID)
		}
		if err := validateAddress(b.Address); err != nil {
			return fmt.Errorf("%w: backend %q: %v", ErrInvalidAddress, b.ID, err)
		}
		if err := validateRange("port", b.Port, MinPort, MaxPort, ErrInvalidPort); err != nil {
			return fmt.Errorf("%w: backend %q", err, b.ID)
		}
		if err := validateRange("weight", b.Weight, MinWeight, MaxWeight, ErrInvalidWeight); err != nil {
			return fmt.Errorf("%w: backend %q", err, b.ID)
		}
		if b.MaxConnections != NoConnectionLimit {
			if err := validateRange("max_connections", b.MaxConnections, MinWeight, MaxActiveBackends, ErrInvalidWeight); err != nil {
				return fmt.Errorf("%w: backend %q", err, b.ID)
			}
		}
		if err := validateHealthPolicy("backend "+b.ID, b.HealthPolicy); err != nil {
			return err
		}
	}
	return nil
}

func validateListener(name string, l ListenerConfig) error {
	if l.Host == "" {
		return fmt.Errorf("%s host is required", name)
	}
	return validateRange(name+" port", l.Port, MinPort, MaxPort, ErrInvalidPort)
}

func validateID(kind, id string) error {
	if id == "" {
		return fmt.Errorf("%w: %s id is empty", ErrInvalidID, kind)
	}
	if len(id) > maxNameLength || !idPattern.MatchString(id) {
		return fmt.Errorf("%w: %s id %q must match [A-Za-z0-9._-]{1,%d}", ErrInvalidID, kind, id, maxNameLength)
	}
	return nil
}

func validateName(kind, id, name string) error {
	if len(name) > maxNameLength {
		return fmt.Errorf("%w: %s %q name too long (%d > %d)", ErrInvalidName, kind, id, len(name), maxNameLength)
	}
	return nil
}

func validateAddress(addr string) error {
	if addr == "" {
		return errors.New("address is required")
	}
	if len(addr) > maxNameLength {
		return fmt.Errorf("address too long (%d > %d)", len(addr), maxNameLength)
	}
	for _, r := range addr {
		if r <= ' ' || r == 0x7f {
			return fmt.Errorf("address %q contains control characters", addr)
		}
	}
	return nil
}

func validateRange(field string, value, min, max int, sentinel error) error {
	if value < min || value > max {
		return fmt.Errorf("%w: %s=%d outside [%d,%d]", sentinel, field, value, min, max)
	}
	return nil
}

func validateHealthPolicy(owner string, h HealthPolicy) error {
	if err := validatePath(h.Path); err != nil {
		return fmt.Errorf("%w: %s health_check_path: %v", ErrInvalidPath, owner, err)
	}
	if err := validateOptionalSeconds("health_check_interval", owner, h.IntervalSeconds, minHealthIntervalSec, maxHealthIntervalSec); err != nil {
		return err
	}
	if err := validateOptionalSeconds("health_check_timeout", owner, h.TimeoutSeconds, minHealthTimeoutSec, maxHealthTimeoutSec); err != nil {
		return err
	}
	if err := validateOptionalRange("healthy_threshold", owner, h.HealthyThreshold, MinHealthyThreshold, MaxHealthyThreshold, ErrInvalidThreshold); err != nil {
		return err
	}
	if err := validateOptionalRange("unhealthy_threshold", owner, h.UnhealthyThreshold, MinUnhealthyThreshold, MaxUnhealthyThreshold, ErrInvalidThreshold); err != nil {
		return err
	}
	if h.TimeoutSeconds > 0 && h.IntervalSeconds > 0 && h.TimeoutSeconds >= h.IntervalSeconds {
		return fmt.Errorf("%w: %s health_check_timeout %ds must be less than interval %ds",
			ErrInvalidDuration, owner, h.TimeoutSeconds, h.IntervalSeconds)
	}
	if err := validateOptionalSeconds("grace_period", owner, h.GraceSeconds, minGraceSec, maxGraceSec); err != nil {
		return err
	}
	return nil
}

func validateSticky(owner string, s StickyConfig) error {
	if !s.Enabled {
		return nil
	}
	if s.TTLSeconds == 0 {
		return fmt.Errorf("%w: %s", ErrStickyTTLRequired, owner)
	}
	return validateOptionalSeconds("sticky_ttl", owner, s.TTLSeconds, minStickyTTLSec, maxStickyTTLSec)
}

func validatePath(path string) error {
	if path == "" {
		return nil
	}
	if path[0] != '/' {
		return fmt.Errorf("%w: %q must start with '/'", ErrInvalidPath, path)
	}
	for _, r := range path {
		if r <= ' ' || r == 0x7f {
			return fmt.Errorf("%w: %q contains control characters", ErrInvalidPath, path)
		}
	}
	return nil
}

// validateOptionalSeconds accepts unset (0) or a value inside [min,max].
func validateOptionalSeconds(field, owner string, value, min, max int) error {
	if value == 0 {
		return nil
	}
	if value < min || value > max {
		return fmt.Errorf("%w: %s %s=%ds outside [%ds,%ds]", ErrInvalidDuration, owner, field, value, min, max)
	}
	return nil
}

// validateOptionalRange accepts unset (0) or a value inside [min,max].
func validateOptionalRange(field, owner string, value, min, max int, sentinel error) error {
	if value == 0 {
		return nil
	}
	if value < min || value > max {
		return fmt.Errorf("%w: %s %s=%d outside [%d,%d]", sentinel, owner, field, value, min, max)
	}
	return nil
}
