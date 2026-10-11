package registry

import (
	"fmt"
	"slices"
	"time"

	"github.com/marcusaroldan/load-balancer/internal/config"
	"github.com/marcusaroldan/load-balancer/internal/pool"
)

// BackendStatus is the observed (non-persisted) state of one backend.
type BackendStatus struct {
	Healthy           bool
	ConsecutiveOK     int
	ConsecutiveFail   int
	LastCheck         time.Time
	ActiveConnections int
	Draining          bool
}

// Status returns the observed state of one backend. Atomic read.
func (r *Registry) Status(poolID, backendID string) (BackendStatus, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	pv, v, err := r.lookupLocked(poolID, backendID)
	if err != nil {
		return BackendStatus{}, err
	}
	return statusOf(pv, v), nil
}

// FindBackend locates the owning pool for a backend ID and returns its desired
// config plus observed status. Pool iteration is ordered for determinism.
func (r *Registry) FindBackend(backendID string) (poolID string, cfg config.BackendConfig, st BackendStatus, err error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, id := range r.sortedPoolIDsLocked() {
		pv := r.pools[id]
		if v, ok := pv.backends[backendID]; ok {
			return id, v.cfg, statusOf(pv, v), nil
		}
	}
	return "", config.BackendConfig{}, BackendStatus{}, fmt.Errorf("%w: backend %q", pool.ErrBackendNotFound, backendID)
}

// ConfigureAlgorithm updates a pool's strategy and/or sticky-session settings
// as one atomic mutation: validation covers the full pool plus current
// members; on any error the live configuration is unchanged.
func (r *Registry) ConfigureAlgorithm(poolID string, algorithm string, sticky config.StickyConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	pv, ok := r.pools[poolID]
	if !ok {
		return fmt.Errorf("%w: %s", pool.ErrPoolNotFound, poolID)
	}
	if algorithm != "" && !isKnownStrategy(algorithm) {
		return fmt.Errorf("%w: %q", config.ErrUnknownStrategy, algorithm)
	}

	next := pv.cfg
	if algorithm != "" {
		next.Algorithm = algorithm
	}
	next.StickySessions = sticky

	if err := validatePoolWithMembers(next, pv); err != nil {
		return err
	}
	pv.cfg = next
	r.version++
	r.route.ResetPool(poolID)
	return nil
}

// MemberIDs lists backend IDs of a pool deterministically.
func (r *Registry) MemberIDs(poolID string) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	pv, ok := r.pools[poolID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", pool.ErrPoolNotFound, poolID)
	}
	return pv.sortedIDs(), nil
}

func (r *Registry) lookupLocked(poolID, backendID string) (*poolView, *backendView, error) {
	pv, ok := r.pools[poolID]
	if !ok {
		return nil, nil, fmt.Errorf("%w: %s", pool.ErrPoolNotFound, poolID)
	}
	v, ok := pv.backends[backendID]
	if !ok {
		return nil, nil, fmt.Errorf("%w: backend %q in pool %q", pool.ErrBackendNotFound, backendID, poolID)
	}
	return pv, v, nil
}

func (r *Registry) sortedPoolIDsLocked() []string {
	ids := make([]string, 0, len(r.pools))
	for id := range r.pools {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func statusOf(pv *poolView, v *backendView) BackendStatus {
	st := BackendStatus{
		Healthy:         v.healthy,
		ConsecutiveOK:   v.consecutiveOK,
		ConsecutiveFail: v.consecutiveFail,
		LastCheck:       v.lastCheck,
		Draining:        v.draining,
	}
	if v.trackers != nil {
		v.trackers.observed.mu.Lock()
		st.ActiveConnections = v.trackers.observed.activeConnections
		v.trackers.observed.mu.Unlock()
	}
	return st
}

// validatePoolWithMembers checks a candidate pool config together with its
// current members against the canonical validator. Listeners come from
// config.Default() so the probe only exercises pool/backend rules.
func validatePoolWithMembers(pc config.PoolConfig, pv *poolView) error {
	probe := config.Default()
	probe.Pools = []config.PoolConfig{pc}
	for _, id := range pv.sortedIDs() {
		probe.Backends = append(probe.Backends, pv.backends[id].cfg)
	}
	if err := probe.Validate(); err != nil {
		return err
	}
	return nil
}
