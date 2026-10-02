// Package registry provides the unified pool registry and routing service that
// is the only production selection path (PROD-002). It composes the canonical
// config model (internal/config), the pool data types (internal/pool), and the
// routing strategies (internal/routing) behind one snapshot-consistent facade.
//
// Design rules from the standalone plan:
//   - Mutations are serialized by the Registry; reads/selections operate on an
//     immutable Snapshot, so a torn read is impossible.
//   - Strategy state lives inside the routing service with safe reset behavior
//     after membership or health changes.
//   - Unknown strategies fail loudly at admission time; nothing falls back.
package registry

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/marcusaroldan/load-balancer/internal/config"
	"github.com/marcusaroldan/load-balancer/internal/pool"
	"github.com/marcusaroldan/load-balancer/internal/routing"
)

var (
	ErrEmptyConfig      = errors.New("registry config must define at least one pool")
	ErrPoolHasBackends  = errors.New("pool still has backends; remove them first")
	ErrBackendUnhealthy = errors.New("backend is not eligible for selection")
)

// nowFunc makes expiry checks testable without sleeping.
type nowFunc func() time.Time

// backendView is the desired+observed pair for one backend, owned by the registry.
type backendView struct {
	cfg             config.BackendConfig
	healthy         bool
	registeredAt    time.Time
	lastCheck       time.Time
	consecutiveOK   int
	consecutiveFail int
	draining        bool
}

func (v *backendView) eligible(now time.Time, grace time.Duration) bool {
	if !v.cfg.Enabled || v.draining {
		return false
	}
	if v.healthy {
		return true
	}
	// Startup grace: a backend with no completed check yet is optimistically
	// eligible until its first probe lands (avoids cold-start 503 storms).
	return v.lastCheck.IsZero() && grace > 0 && now.Before(v.registeredAt.Add(grace))
}

// registeredAt records when the backend joined, for grace-period accounting.
func (v *backendView) touchRegistered(now time.Time) {
	if v.registeredAt.IsZero() {
		v.registeredAt = now
	}
}

// poolView holds one pool's desired config plus derived member list.
type poolView struct {
	cfg      config.PoolConfig
	backends map[string]*backendView
}

func (p *poolView) sortedIDs() []string {
	ids := make([]string, 0, len(p.backends))
	for id := range p.backends {
		ids = append(ids, id)
	}
	sortStrings(ids)
	return ids
}

// Snapshot is an immutable point-in-time view of the registry. The data plane
// takes one snapshot per request; selections never observe partial mutations.
// backendKey identifies one backend across pools in snapshot maps.
type backendKey struct {
	poolID    string
	backendID string
}

type Snapshot struct {
	pools          map[string]*pool.Pool
	backendConfigs map[backendKey]config.BackendConfig
	strategies     map[string]string
	policies       map[string]config.HealthPolicy
	memberOrder    map[string][]string // deterministic iteration order per pool
	now            nowFunc
}

// Pools lists pool IDs deterministically.
func (s *Snapshot) Pools() []string {
	ids := make([]string, 0, len(s.pools))
	for id := range s.pools {
		ids = append(ids, id)
	}
	sortStrings(ids)
	return ids
}

// Pool returns the pool by ID.
func (s *Snapshot) Pool(id string) (*pool.Pool, bool) {
	p, ok := s.pools[id]
	return p, ok
}

// Strategy reports the effective algorithm for a pool.
func (s *Snapshot) Strategy(poolID string) (string, bool) {
	strategy, ok := s.strategies[poolID]
	return strategy, ok
}

// HealthPolicyOf returns the effective health policy for a pool.
func (s *Snapshot) HealthPolicyOf(poolID string) (config.HealthPolicy, bool) {
	hp, ok := s.policies[poolID]
	return hp, ok
}

// BackendConfig exposes desired state for one backend.
func (s *Snapshot) BackendConfig(poolID, backendID string) (config.BackendConfig, bool) {
	bc, ok := s.backendConfigs[backendKey{poolID, backendID}]
	return bc, ok
}

// Registry is the unified pool registry and routing service.
type Registry struct {
	mu           sync.RWMutex
	pools        map[string]*poolView
	version      uint64
	route        *routing.Service
	now          nowFunc
	graceDefault time.Duration
}

// New creates an empty registry.
func New() *Registry {
	return &Registry{
		pools: make(map[string]*poolView),
		route: routing.NewService(),
		now:   time.Now,
	}
}

// Load replaces the entire registry content from a validated configuration.
// It is atomic: either every pool/backend loads, or the previous state remains.
func (r *Registry) Load(cfg config.Config) error {
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("load registry config: %w", err)
	}
	if len(cfg.Pools) == 0 {
		return ErrEmptyConfig
	}

	next := make(map[string]*poolView, len(cfg.Pools))
	for _, pc := range cfg.Pools {
		pv := &poolView{cfg: pc, backends: make(map[string]*backendView)}
		next[pc.ID] = pv
	}
	for _, bc := range cfg.Backends {
		pv, ok := next[bc.PoolID]
		if !ok {
			return fmt.Errorf("%w: backend %q references pool %q", ErrUnknownPoolForBackend, bc.ID, bc.PoolID)
		}
		view := &backendView{cfg: bc, healthy: true}
		view.touchRegistered(r.now())
		pv.backends[bc.ID] = view
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.pools = next
	r.version++
	r.route.ResetAll()
	return nil
}

// ErrUnknownPoolForBackend signals a Load whose backends reference missing pools
// despite validation (defense in depth against hand-built configs).
var ErrUnknownPoolForBackend = errors.New("backend references unknown pool")

// CreatePool adds a new pool. Duplicate IDs are rejected.
func (r *Registry) CreatePool(pc config.PoolConfig) error {
	pcCopy := pc
	defaults := config.DefaultHealthPolicy()
	if pcCopy.Algorithm == "" {
		pcCopy.Algorithm = config.StrategyRoundRobin
	}
	if pcCopy.Name == "" {
		pcCopy.Name = pcCopy.ID
	}
	pcCopy.HealthPolicy = overlayPolicy(defaults, pcCopy.HealthPolicy)

	if err := validatePoolAdmission(pcCopy); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.pools[pcCopy.ID]; exists {
		return fmt.Errorf("%w: pool %q already exists", config.ErrDuplicateID, pcCopy.ID)
	}
	r.pools[pcCopy.ID] = &poolView{cfg: pcCopy, backends: make(map[string]*backendView)}
	r.version++
	return nil
}

// DeletePool removes a pool. Fails while it still contains backends so callers
// drain members explicitly first (keeps ownership rules unambiguous).
func (r *Registry) DeletePool(poolID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	pv, ok := r.pools[poolID]
	if !ok {
		return fmt.Errorf("%w: %s", pool.ErrPoolNotFound, poolID)
	}
	if len(pv.backends) > 0 {
		return fmt.Errorf("%w: pool %q has %d backend(s)", ErrPoolHasBackends, poolID, len(pv.backends))
	}
	delete(r.pools, poolID)
	r.version++
	r.route.ResetPool(poolID)
	return nil
}

// AddBackend inserts a backend into an existing pool.
func (r *Registry) AddBackend(bc config.BackendConfig) error {
	bcCopy := bc
	if bcCopy.Weight == 0 {
		bcCopy.Weight = config.DefaultBackendWeight
	}
	if bcCopy.MaxConnections == 0 {
		bcCopy.MaxConnections = config.NoConnectionLimit
	}
	if err := validateBackendAdmission(bcCopy); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	pv, ok := r.pools[bcCopy.PoolID]
	if !ok {
		return fmt.Errorf("%w: %s", pool.ErrPoolNotFound, bcCopy.PoolID)
	}
	if _, exists := pv.backends[bcCopy.ID]; exists {
		return fmt.Errorf("%w: backend %q", config.ErrDuplicateID, bcCopy.ID)
	}
	view := &backendView{cfg: bcCopy, healthy: true}
	view.touchRegistered(r.now())
	pv.backends[bcCopy.ID] = view
	r.version++
	r.route.ResetPool(bcCopy.PoolID)
	return nil
}

// RemoveBackend deletes a backend from its pool.
func (r *Registry) RemoveBackend(poolID, backendID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	pv, ok := r.pools[poolID]
	if !ok {
		return fmt.Errorf("%w: %s", pool.ErrPoolNotFound, poolID)
	}
	if _, exists := pv.backends[backendID]; !exists {
		return fmt.Errorf("%w: backend %q in pool %q", pool.ErrBackendNotFound, backendID, poolID)
	}
	delete(pv.backends, backendID)
	r.version++
	r.route.ResetPool(poolID)
	return nil
}

// SetEnabled toggles a backend's desired availability (operator control).
func (r *Registry) SetEnabled(poolID, backendID string, enabled bool) error {
	return r.mutateBackend(poolID, backendID, func(v *backendView) {
		v.cfg.Enabled = enabled
	})
}

// SetWeight updates a backend weight used by weighted selection.
func (r *Registry) SetWeight(poolID, backendID string, weight int) error {
	if weight < config.MinWeight || weight > config.MaxWeight {
		return fmt.Errorf("%w: weight=%d outside [%d,%d]", config.ErrInvalidWeight, weight, config.MinWeight, config.MaxWeight)
	}
	return r.mutateBackend(poolID, backendID, func(v *backendView) {
		v.cfg.Weight = weight
	})
}

// SetStrategy changes the routing algorithm for a pool without a restart.
// Invalid names are rejected; valid names reset selector state for the pool.
func (r *Registry) SetStrategy(poolID, strategy string) error {
	if !isKnownStrategy(strategy) {
		return fmt.Errorf("%w: %q", config.ErrUnknownStrategy, strategy)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	pv, ok := r.pools[poolID]
	if !ok {
		return fmt.Errorf("%w: %s", pool.ErrPoolNotFound, poolID)
	}
	pv.cfg.Algorithm = strategy
	r.version++
	r.route.ResetPool(poolID)
	return nil
}

// MarkHealthy records a successful probe for a backend, applying the pool's
// recovery threshold before flipping eligibility.
func (r *Registry) MarkHealthy(poolID, backendID string) error {
	return r.recordProbe(poolID, backendID, true)
}

// MarkUnhealthy records a failed probe, applying the unhealthy threshold.
func (r *Registry) MarkUnhealthy(poolID, backendID string) error {
	return r.recordProbe(poolID, backendID, false)
}

// ResetHealth forces a backend's observed state (used by tests and simulator
// environment flips that bypass probing).
func (r *Registry) ResetHealth(poolID, backendID string, healthy bool) error {
	return r.mutateBackend(poolID, backendID, func(v *backendView) {
		v.healthy = healthy
		v.consecutiveOK = 0
		v.consecutiveFail = 0
		v.lastCheck = r.now()
	})
}

func (r *Registry) recordProbe(poolID, backendID string, success bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	pv, ok := r.pools[poolID]
	if !ok {
		return fmt.Errorf("%w: %s", pool.ErrPoolNotFound, poolID)
	}
	v, ok := pv.backends[backendID]
	if !ok {
		return fmt.Errorf("%w: backend %q in pool %q", pool.ErrBackendNotFound, backendID, poolID)
	}
	now := r.now()
	v.lastCheck = now
	policy := effectivePolicy(pv, v)
	if success {
		v.consecutiveFail = 0
		v.consecutiveOK++
		if !v.healthy && v.consecutiveOK >= policy.HealthyThreshold {
			v.healthy = true
			r.route.ResetPool(poolID)
		}
		return nil
	}
	v.consecutiveOK = 0
	v.consecutiveFail++
	if v.healthy && v.consecutiveFail >= policy.UnhealthyThreshold {
		v.healthy = false
		r.route.ResetPool(poolID)
	}
	return nil
}

func (r *Registry) mutateBackend(poolID, backendID string, fn func(*backendView)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	pv, ok := r.pools[poolID]
	if !ok {
		return fmt.Errorf("%w: %s", pool.ErrPoolNotFound, poolID)
	}
	v, ok := pv.backends[backendID]
	if !ok {
		return fmt.Errorf("%w: backend %q in pool %q", pool.ErrBackendNotFound, backendID, poolID)
	}
	fn(v)
	r.version++
	r.route.ResetPool(poolID)
	return nil
}

// Snapshot builds an immutable view of current state. pool.Backend values are
// fresh copies so later mutation of the registry cannot alter an in-flight
// request's view; eligibility reflects desired (enabled) + observed (health).
func (r *Registry) Snapshot() *Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()

	now := r.now()
	snap := &Snapshot{
		pools:          make(map[string]*pool.Pool, len(r.pools)),
		backendConfigs: make(map[backendKey]config.BackendConfig, len(r.pools)),
		strategies:     make(map[string]string, len(r.pools)),
		policies:       make(map[string]config.HealthPolicy, len(r.pools)),
		memberOrder:    make(map[string][]string, len(r.pools)),
		now:            r.now,
	}
	for poolID, pv := range r.pools {
		p := &pool.Pool{
			ID:       pv.cfg.ID,
			Name:     pv.cfg.Name,
			Metadata: map[string]string{"strategy": pv.cfg.Algorithm},
			Backends: make(map[string]*pool.Backend, len(pv.backends)),
		}
		for id, v := range pv.backends {
			p.Backends[id] = &pool.Backend{
				ID:      v.cfg.ID,
				Address: v.cfg.Address,
				Port:    v.cfg.Port,
				PoolID:  poolID,
				Healthy: v.eligible(now, r.graceDefault),
			}
			snap.backendConfigs[backendKey{poolID, id}] = v.cfg
		}
		snap.pools[poolID] = p
		snap.strategies[poolID] = pv.cfg.Algorithm
		snap.policies[poolID] = pv.cfg.HealthPolicy
		snap.memberOrder[poolID] = pv.sortedIDs()
	}
	return snap
}

// Select routes one request within a pool using the configured strategy.
// It returns the chosen backend or ErrNoHealthyBackends-style errors from the
// routing layer; disabled/unhealthy backends are excluded via snapshot flags.
func (r *Registry) Select(snap *Snapshot, poolID string) (*pool.Backend, error) {
	p, ok := snap.pools[poolID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", pool.ErrPoolNotFound, poolID)
	}
	policy := make(map[string]routing.BackendPolicy, len(p.Backends))
	for id := range p.Backends {
		bc, ok := snap.backendConfigs[backendKey{poolID, id}]
		if !ok {
			bc = config.BackendConfig{Weight: routing.DefaultBackendWeight}
		}
		policy[id] = routing.BackendPolicy{Weight: bc.Weight, Strategy: snap.strategies[poolID]}
	}
	return r.route.Select(snap.strategies[poolID], p, policy)
}

// validatePoolAdmission checks the fields Load() would have defaulted.
func validatePoolAdmission(pc config.PoolConfig) error {
	probe := config.Config{Pools: []config.PoolConfig{pc}}
	probe.ApplyDefaults()
	if err := probe.Validate(); err != nil {
		return err
	}
	return nil
}

// validateBackendAdmission checks a backend against its own constraints only;
// pool existence is checked separately at insert time.
func validateBackendAdmission(bc config.BackendConfig) error {
	probe := config.Config{
		Pools:    []config.PoolConfig{{ID: bc.PoolID, Algorithm: config.StrategyRoundRobin}},
		Backends: []config.BackendConfig{bc},
	}
	probe.ApplyDefaults()
	if err := probe.Validate(); err != nil {
		return err
	}
	return nil
}

func isKnownStrategy(name string) bool {
	for _, s := range config.SupportedStrategies() {
		if s == name {
			return true
		}
	}
	return false
}

// overlayPolicy copies sparse non-zero fields onto base. Mirrors the internal
// rule in config.ApplyDefaults but exported-shaped for single-pool admission.
func overlayPolicy(base, sparse config.HealthPolicy) config.HealthPolicy {
	out := base
	if sparse.Path != "" {
		out.Path = sparse.Path
	}
	if sparse.IntervalSeconds != 0 {
		out.IntervalSeconds = sparse.IntervalSeconds
	}
	if sparse.TimeoutSeconds != 0 {
		out.TimeoutSeconds = sparse.TimeoutSeconds
	}
	if sparse.HealthyThreshold != 0 {
		out.HealthyThreshold = sparse.HealthyThreshold
	}
	if sparse.UnhealthyThreshold != 0 {
		out.UnhealthyThreshold = sparse.UnhealthyThreshold
	}
	if sparse.GraceSeconds != 0 {
		out.GraceSeconds = sparse.GraceSeconds
	}
	return out
}

// effectivePolicy resolves backend override over pool policy.
func effectivePolicy(pv *poolView, v *backendView) config.HealthPolicy {
	if v.cfg.HealthPolicy != (config.HealthPolicy{}) {
		return overlayPolicy(pv.cfg.HealthPolicy, v.cfg.HealthPolicy)
	}
	return pv.cfg.HealthPolicy
}

func sortStrings(ids []string) {
	slices.Sort(ids)
}
