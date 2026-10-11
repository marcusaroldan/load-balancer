// Registry-backed control plane API (PROD-007). Bound directly to the live
// runtime registry so every mutation immediately affects data-plane routing.
//
// Endpoints and versioning (APIVersion is included in every response):
//
//	POST   /backend                  create backend {backend_id?, pool?, address, port, weight?, max_connections?, health_check_path?}
//	GET    /backend/{backend_id}     desired + observed state
//	DELETE /backend/{backend_id}     disable, drain, remove; reports drained connections
//	GET    /backend/{backend_id}/health  health status and probe counters
//	PUT    /backend/{backend_id}/weight  desired weight update (validated)
//	PUT    /backend/{backend_id}/enabled routing eligibility toggle
//	POST   /pool                     create pool {pool_id, name?, algorithm?, health_policy?, sticky?}
//	GET    /pool                     list pools
//	GET    /pool/{pool_id}           pool config and members
//	DELETE /pool/{pool_id}           drain members and delete pool
//	PUT    /config/algorithm         set strategy + sticky settings for a pool
//	GET    /config                   effective non-secret configuration
//	GET    /healthz                  process liveness
//	GET    /readyz                   ready once configured and serving
//	GET    /metrics                  aggregate request/connection metrics
//
// Error contract: every failure is JSON {"api_version","error"} with a 400
// (validation), 404 (not found), 405 (bad method), 409 (conflict) or 500
// status. Mutations apply validate-then-commit; a rejected request never
// changes live state.
package controlplane

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/marcusaroldan/load-balancer/internal/config"
	"github.com/marcusaroldan/load-balancer/internal/pool"
	"github.com/marcusaroldan/load-balancer/internal/proxy"
	"github.com/marcusaroldan/load-balancer/internal/registry"
)

// APIVersion is the schema version echoed in all responses.
const APIVersion = "v1"

const (
	defaultDrainTimeout = 10 * time.Second
	generatedIDFormat   = "%s-backend-%d"
	maxBodyBytes        = 1 << 16 // 64 KiB request bodies bound API memory use
)

// DataPlaneMetrics supplies aggregate request counters (the proxy handler
// satisfies this); nil leaves /metrics reporting connections only.
type DataPlaneMetrics interface {
	Metrics() proxy.RequestMetrics
}

// APIDeps wires the control plane to the live runtime.
type APIDeps struct {
	Registry     *registry.Registry
	DataPlane    DataPlaneMetrics
	DrainTimeout time.Duration
	StartedAt    time.Time
}

// APIHandler serves the versioned control-plane API against the runtime.
type APIHandler struct {
	deps APIDeps
	mux  *http.ServeMux
}

// NewAPIHandler builds the routed control-plane handler.
func NewAPIHandler(deps APIDeps) *APIHandler {
	if deps.Registry == nil {
		panic("controlplane: nil registry")
	}
	if deps.DrainTimeout <= 0 {
		deps.DrainTimeout = defaultDrainTimeout
	}
	h := &APIHandler{deps: deps}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /backend", h.handleCreateBackend)
	mux.HandleFunc("GET /backend/{backend_id}", h.handleGetBackend)
	mux.HandleFunc("DELETE /backend/{backend_id}", h.handleDeleteBackend)
	mux.HandleFunc("GET /backend/{backend_id}/health", h.handleBackendHealth)
	mux.HandleFunc("POST /pool", h.handleCreatePool)
	mux.HandleFunc("GET /pool", h.handleListPools)
	mux.HandleFunc("GET /pool/{pool_id}", h.handleGetPool)
	mux.HandleFunc("DELETE /pool/{pool_id}", h.handleDeletePool)
	mux.HandleFunc("PUT /config/algorithm", h.handleSetAlgorithm)
	mux.HandleFunc("PUT /backend/{backend_id}/weight", h.handleSetBackendWeight)
	mux.HandleFunc("PUT /backend/{backend_id}/enabled", h.handleSetBackendEnabled)
	mux.HandleFunc("GET /config", h.handleGetConfig)
	mux.HandleFunc("GET /healthz", h.handleHealthz)
	mux.HandleFunc("GET /readyz", h.handleReadyz)
	mux.HandleFunc("GET /metrics", h.handleMetrics)
	h.mux = mux
	return h
}

// ServeHTTP lives in audit.go (mutations are audited there; reads pass
// straight to the mux).

// ---- request DTOs -------------------------------------------------------

type createBackendBody struct {
	BackendID       string `json:"backend_id"`
	Pool            string `json:"pool"`
	Address         string `json:"address"`
	Port            int    `json:"port"`
	Weight          int    `json:"weight"`
	MaxConnections  *int   `json:"max_connections"`
	HealthCheckPath string `json:"health_check_path"`
}

type createPoolBody struct {
	PoolID       string               `json:"pool_id"`
	Name         string               `json:"name"`
	Algorithm    string               `json:"algorithm"`
	HealthPolicy *config.HealthPolicy `json:"health_policy"`
	Sticky       *config.StickyConfig `json:"sticky"`
}

type algorithmBody struct {
	Pool             string `json:"pool"`
	Algorithm        string `json:"algorithm"`
	StickySession    bool   `json:"sticky_session"`
	StickyTTLSeconds int    `json:"sticky_ttl_seconds"`
}

// ---- response DTOs ------------------------------------------------------

type apiResponse struct {
	APIVersion string `json:"api_version"`
}

type errorResponse struct {
	APIVersion string `json:"api_version"`
	Error      string `json:"error"`
}

type createBackendResponse struct {
	APIVersion string `json:"api_version"`
	BackendID  string `json:"backend_id"`
	PoolID     string `json:"pool_id"`
}

type backendResponse struct {
	APIVersion        string `json:"api_version"`
	BackendID         string `json:"backend_id"`
	PoolID            string `json:"pool_id"`
	Address           string `json:"address"`
	Port              int    `json:"port"`
	Weight            int    `json:"weight"`
	MaxConnections    int    `json:"max_connections"`
	Enabled           bool   `json:"enabled"`
	Healthy           bool   `json:"healthy"`
	Draining          bool   `json:"draining"`
	ActiveConnections int    `json:"active_connections"`
}

type drainResponse struct {
	APIVersion         string `json:"api_version"`
	BackendID          string `json:"backend_id"`
	PoolID             string `json:"pool_id"`
	DrainedConnections int    `json:"drained_connections"`
	Complete           bool   `json:"complete"`
}

type healthResponse struct {
	APIVersion           string `json:"api_version"`
	BackendID            string `json:"backend_id"`
	Healthy              bool   `json:"healthy"`
	ConsecutiveSuccesses int    `json:"consecutive_successes"`
	ConsecutiveFailures  int    `json:"consecutive_failures"`
	LastCheck            string `json:"last_check,omitempty"`
}

type poolSummary struct {
	PoolID       string `json:"pool_id"`
	Name         string `json:"name"`
	Algorithm    string `json:"algorithm"`
	BackendCount int    `json:"backend_count"`
}

type listPoolsResponse struct {
	APIVersion string        `json:"api_version"`
	Pools      []poolSummary `json:"pools"`
}

type poolResponse struct {
	APIVersion string            `json:"api_version"`
	Pool       config.PoolConfig `json:"pool"`
	Backends   []backendResponse `json:"backends"`
}

type configResponse struct {
	APIVersion string        `json:"api_version"`
	Config     config.Config `json:"config"`
}

type metricsResponse struct {
	APIVersion        string          `json:"api_version"`
	Requests          requestCounters `json:"requests"`
	ActiveBackends    int             `json:"healthy_backends"`
	TotalBackends     int             `json:"total_backends"`
	ActiveConnections int             `json:"active_connections"`
	PoolCount         int             `json:"pool_count"`
}

type requestCounters struct {
	Total            int64   `json:"total"`
	Success          int64   `json:"success"`
	GatewayFailures  int64   `json:"gateway_failures"`
	RouteFailures    int64   `json:"route_failures"`
	AverageLatencyMS float64 `json:"average_latency_ms"`
}

// ---- handlers -----------------------------------------------------------

func (h *APIHandler) handleCreateBackend(w http.ResponseWriter, r *http.Request) {
	var body createBackendBody
	if err := decodeBody(w, r, &body); err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	poolID, err := h.resolvePool(body.Pool)
	if err != nil {
		h.writeError(w, statusFor(err), err)
		return
	}
	backendID := body.BackendID
	if backendID == "" {
		backendID = h.generateBackendID(poolID)
	}
	bc := config.BackendConfig{
		ID:      backendID,
		PoolID:  poolID,
		Address: body.Address,
		Port:    body.Port,
		Weight:  body.Weight,
		Enabled: true,
	}
	if body.MaxConnections != nil {
		bc.MaxConnections = *body.MaxConnections
	}
	if body.HealthCheckPath != "" {
		bc.HealthPolicy = config.HealthPolicy{Path: body.HealthCheckPath}
	}
	if err := h.deps.Registry.AddBackend(bc); err != nil {
		h.writeError(w, statusFor(err), err)
		return
	}
	h.writeJSON(w, http.StatusCreated, createBackendResponse{
		APIVersion: APIVersion, BackendID: backendID, PoolID: poolID,
	})
}

func (h *APIHandler) handleGetBackend(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("backend_id")
	poolID, cfg, st, err := h.deps.Registry.FindBackend(id)
	if err != nil {
		h.writeError(w, statusFor(err), err)
		return
	}
	h.writeJSON(w, http.StatusOK, backendResponse{
		APIVersion: APIVersion, BackendID: cfg.ID, PoolID: poolID,
		Address: cfg.Address, Port: cfg.Port, Weight: cfg.Weight,
		MaxConnections: cfg.MaxConnections, Enabled: cfg.Enabled,
		Healthy: st.Healthy, Draining: st.Draining, ActiveConnections: st.ActiveConnections,
	})
}

func (h *APIHandler) handleDeleteBackend(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("backend_id")
	poolID, _, _, err := h.deps.Registry.FindBackend(id)
	if err != nil {
		h.writeError(w, statusFor(err), err)
		return
	}
	respond, complete := h.drainAndRemove(poolID, id)
	h.writeJSON(w, http.StatusOK, respond)
	_ = complete
}

func (h *APIHandler) handleBackendHealth(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("backend_id")
	poolID, _, _, err := h.deps.Registry.FindBackend(id)
	if err != nil {
		h.writeError(w, statusFor(err), err)
		return
	}
	st, err := h.deps.Registry.Status(poolID, id)
	if err != nil {
		h.writeError(w, statusFor(err), err)
		return
	}
	resp := healthResponse{
		APIVersion: APIVersion, BackendID: id, Healthy: st.Healthy,
		ConsecutiveSuccesses: st.ConsecutiveOK, ConsecutiveFailures: st.ConsecutiveFail,
	}
	if !st.LastCheck.IsZero() {
		resp.LastCheck = st.LastCheck.UTC().Format(time.RFC3339Nano)
	}
	h.writeJSON(w, http.StatusOK, resp)
}

// handleSetBackendWeight updates desired weight without a delete/recreate,
// so in-flight connections keep draining correctly (registry.SetWeight is
// the single atomic mutation; validation mirrors admission rules).
func (h *APIHandler) handleSetBackendWeight(w http.ResponseWriter, r *http.Request) {
	backendID := r.PathValue("backend_id")
	var body struct {
		Weight int `json:"weight"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	poolID, _, _, err := h.deps.Registry.FindBackend(backendID)
	if err != nil {
		h.writeError(w, statusFor(err), err)
		return
	}
	if err := h.deps.Registry.SetWeight(poolID, backendID, body.Weight); err != nil {
		h.writeError(w, statusFor(err), err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"api_version": APIVersion, "backend_id": backendID, "pool_id": poolID,
		"weight": body.Weight,
	})
}

// handleSetBackendEnabled toggles routing eligibility immediately (a flag
// change, not a lifecycle event; draining/removal stay on DELETE).
func (h *APIHandler) handleSetBackendEnabled(w http.ResponseWriter, r *http.Request) {
	backendID := r.PathValue("backend_id")
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	poolID, _, _, err := h.deps.Registry.FindBackend(backendID)
	if err != nil {
		h.writeError(w, statusFor(err), err)
		return
	}
	if err := h.deps.Registry.SetEnabled(poolID, backendID, body.Enabled); err != nil {
		h.writeError(w, statusFor(err), err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"api_version": APIVersion, "backend_id": backendID, "pool_id": poolID,
		"enabled": body.Enabled,
	})
}

func (h *APIHandler) handleCreatePool(w http.ResponseWriter, r *http.Request) {
	var body createPoolBody
	if err := decodeBody(w, r, &body); err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	pc := config.PoolConfig{ID: body.PoolID, Name: body.Name, Algorithm: body.Algorithm}
	if body.HealthPolicy != nil {
		pc.HealthPolicy = *body.HealthPolicy
	}
	if body.Sticky != nil {
		pc.StickySessions = *body.Sticky
	}
	if err := h.deps.Registry.CreatePool(pc); err != nil {
		h.writeError(w, statusFor(err), err)
		return
	}
	h.writeJSON(w, http.StatusCreated, struct {
		APIVersion string `json:"api_version"`
		PoolID     string `json:"pool_id"`
	}{APIVersion, body.PoolID})
}

func (h *APIHandler) handleListPools(w http.ResponseWriter, r *http.Request) {
	snap := h.deps.Registry.Snapshot()
	out := listPoolsResponse{APIVersion: APIVersion, Pools: []poolSummary{}}
	for _, poolID := range snap.Pools() {
		p, _ := snap.Pool(poolID)
		algorithm, _ := snap.Strategy(poolID)
		out.Pools = append(out.Pools, poolSummary{
			PoolID: p.ID, Name: p.Name, Algorithm: algorithm, BackendCount: len(p.Backends),
		})
	}
	h.writeJSON(w, http.StatusOK, out)
}

func (h *APIHandler) handleGetPool(w http.ResponseWriter, r *http.Request) {
	poolID := r.PathValue("pool_id")
	snap := h.deps.Registry.Snapshot()
	cfg := h.deps.Registry.Config()
	pc, ok := findPoolConfig(cfg, poolID)
	if !ok {
		h.writeError(w, http.StatusNotFound, fmt.Errorf("%w: %s", pool.ErrPoolNotFound, poolID))
		return
	}
	p, _ := snap.Pool(poolID)
	resp := poolResponse{APIVersion: APIVersion, Pool: pc, Backends: []backendResponse{}}
	for _, b := range sortedBackends(p) {
		st, err := h.deps.Registry.Status(poolID, b.ID)
		if err != nil {
			h.writeError(w, statusFor(err), err)
			return
		}
		bc, _ := snap.BackendConfig(poolID, b.ID)
		resp.Backends = append(resp.Backends, backendResponse{
			APIVersion: APIVersion, BackendID: b.ID, PoolID: poolID,
			Address: b.Address, Port: b.Port, Weight: bc.Weight,
			MaxConnections: bc.MaxConnections, Enabled: bc.Enabled,
			Healthy: st.Healthy, Draining: st.Draining, ActiveConnections: st.ActiveConnections,
		})
	}
	h.writeJSON(w, http.StatusOK, resp)
}

func (h *APIHandler) handleDeletePool(w http.ResponseWriter, r *http.Request) {
	poolID := r.PathValue("pool_id")
	members, err := h.deps.Registry.MemberIDs(poolID)
	if err != nil {
		h.writeError(w, statusFor(err), err)
		return
	}
	drained := 0
	for _, id := range members {
		count, _ := h.deps.Registry.BeginDrain(poolID, id)
		h.deps.Registry.WaitDrain(poolID, id, h.deps.DrainTimeout)
		if err := h.deps.Registry.RemoveDrained(poolID, id, true); err == nil {
			drained += count
		}
	}
	if err := h.deps.Registry.DeletePool(poolID); err != nil {
		h.writeError(w, statusFor(err), err)
		return
	}
	h.writeJSON(w, http.StatusOK, struct {
		APIVersion         string `json:"api_version"`
		PoolID             string `json:"pool_id"`
		DrainedConnections int    `json:"drained_connections"`
	}{APIVersion, poolID, drained})
}

func (h *APIHandler) handleSetAlgorithm(w http.ResponseWriter, r *http.Request) {
	var body algorithmBody
	if err := decodeBody(w, r, &body); err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	poolID, err := h.resolvePool(body.Pool)
	if err != nil {
		h.writeError(w, statusFor(err), err)
		return
	}
	sticky := config.StickyConfig{Enabled: body.StickySession, TTLSeconds: body.StickyTTLSeconds}
	if err := h.deps.Registry.ConfigureAlgorithm(poolID, body.Algorithm, sticky); err != nil {
		h.writeError(w, statusFor(err), err)
		return
	}
	h.writeJSON(w, http.StatusOK, struct {
		APIVersion string `json:"api_version"`
		PoolID     string `json:"pool_id"`
		Algorithm  string `json:"algorithm"`
		Sticky     bool   `json:"sticky_session"`
	}{APIVersion, poolID, body.Algorithm, body.StickySession})
}

func (h *APIHandler) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	h.writeJSON(w, http.StatusOK, configResponse{
		APIVersion: APIVersion, Config: h.deps.Registry.Config(),
	})
}

func (h *APIHandler) handleHealthz(w http.ResponseWriter, r *http.Request) {
	h.writeJSON(w, http.StatusOK, struct {
		APIVersion    string  `json:"api_version"`
		Status        string  `json:"status"`
		UptimeSeconds float64 `json:"uptime_seconds"`
	}{APIVersion, "ok", time.Since(h.deps.StartedAt).Seconds()})
}

func (h *APIHandler) handleReadyz(w http.ResponseWriter, r *http.Request) {
	snap := h.deps.Registry.Snapshot()
	configured := len(snap.Pools()) > 0
	serving := h.deps.DataPlane != nil
	if configured && serving {
		h.writeJSON(w, http.StatusOK, struct {
			APIVersion string `json:"api_version"`
			Status     string `json:"status"`
		}{APIVersion, "ready"})
		return
	}
	h.writeError(w, http.StatusServiceUnavailable,
		fmt.Errorf("not ready: configured=%v serving=%v", configured, serving))
}

func (h *APIHandler) handleMetrics(w http.ResponseWriter, r *http.Request) {
	cfg := h.deps.Registry.Config()
	healthy, total, connections := 0, len(cfg.Backends), 0
	for _, b := range cfg.Backends {
		if st, err := h.deps.Registry.Status(b.PoolID, b.ID); err == nil {
			if st.Healthy && !st.Draining {
				healthy++
			}
			connections += st.ActiveConnections
		}
	}
	req := requestCounters{}
	if h.deps.DataPlane != nil {
		m := h.deps.DataPlane.Metrics()
		req = requestCounters{
			Total: m.Total, Success: m.Success, GatewayFailures: m.GatewayFailures,
			RouteFailures:    m.RouteFailures,
			AverageLatencyMS: float64(m.AverageLatency()) / float64(time.Millisecond),
		}
	}
	if wantsPrometheus(r) {
		h.writePrometheus(w, req, healthy, total, connections, len(cfg.Pools))
		return
	}
	h.writeJSON(w, http.StatusOK, metricsResponse{
		APIVersion: APIVersion, Requests: req,
		ActiveBackends: healthy, TotalBackends: total,
		ActiveConnections: connections,
		PoolCount:         len(cfg.Pools),
	})
}

// ---- helpers ------------------------------------------------------------

// drainAndRemove executes the full backend removal lifecycle: mark draining
// (no new selections), wait up to the configured deadline for in-flight
// requests, then remove. complete=false means it was force-removed with
// connections still in flight.
func (h *APIHandler) drainAndRemove(poolID, backendID string) (drainResponse, bool) {
	drained, err := h.deps.Registry.BeginDrain(poolID, backendID)
	if err != nil {
		return drainResponse{APIVersion: APIVersion, BackendID: backendID, PoolID: poolID}, false
	}
	_, complete := h.deps.Registry.WaitDrain(poolID, backendID, h.deps.DrainTimeout)
	if err := h.deps.Registry.RemoveDrained(poolID, backendID, !complete); err != nil {
		return drainResponse{
			APIVersion: APIVersion, BackendID: backendID, PoolID: poolID,
			DrainedConnections: drained, Complete: false,
		}, false
	}
	return drainResponse{
		APIVersion: APIVersion, BackendID: backendID, PoolID: poolID,
		DrainedConnections: drained, Complete: complete,
	}, complete
}

// resolvePool applies the system-design rule: pool is optional only when
// exactly one pool exists (the single pool is returned). Ambiguity errors map
// to 400 via errPoolAmbiguous.
func (h *APIHandler) resolvePool(requested string) (string, error) {
	if requested != "" {
		return requested, nil
	}
	snap := h.deps.Registry.Snapshot()
	pools := snap.Pools()
	switch len(pools) {
	case 1:
		return pools[0], nil
	case 0:
		return "", fmt.Errorf("%w: no pools exist; create a pool first", errPoolAmbiguous)
	default:
		return "", fmt.Errorf("%w: pool is required when multiple pools exist", errPoolAmbiguous)
	}
}

// errPoolAmbiguous marks client-side pool-selection failures -> 400.
var errPoolAmbiguous = errors.New("pool selection")

func (h *APIHandler) generateBackendID(poolID string) string {
	p, ok := h.deps.Registry.Snapshot().Pool(poolID)
	n := 1
	if ok {
		n = len(p.Backends) + 1
	}
	return fmt.Sprintf(generatedIDFormat, poolID, n)
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) error {
	if r.Body == nil {
		return errors.New("request body is required")
	}
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON payload: %w", err)
	}
	return nil
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, pool.ErrPoolNotFound), errors.Is(err, pool.ErrBackendNotFound),
		errors.Is(err, registry.ErrUnknownPoolForBackend):
		return http.StatusNotFound
	case errors.Is(err, config.ErrDuplicateID), errors.Is(err, pool.ErrBackendAlreadyExists):
		return http.StatusConflict
	case errors.Is(err, config.ErrUnknownStrategy), errors.Is(err, config.ErrInvalidPort),
		errors.Is(err, config.ErrInvalidWeight), errors.Is(err, config.ErrInvalidID),
		errors.Is(err, config.ErrInvalidAddress), errors.Is(err, config.ErrInvalidPath),
		errors.Is(err, config.ErrInvalidThreshold), errors.Is(err, config.ErrInvalidDuration),
		errors.Is(err, config.ErrInvalidName), errors.Is(err, config.ErrMissingPool),
		errors.Is(err, config.ErrStickyTTLRequired), errors.Is(err, registry.ErrPoolHasBackends),
		errors.Is(err, errPoolAmbiguous):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func (h *APIHandler) writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (h *APIHandler) writeError(w http.ResponseWriter, status int, err error) {
	h.writeJSON(w, status, errorResponse{APIVersion: APIVersion, Error: err.Error()})
}

// sortedBackends returns pool members in deterministic ID order so API
// payloads are stable across requests.
func sortedBackends(p *pool.Pool) []*pool.Backend {
	out := make([]*pool.Backend, 0, len(p.Backends))
	for _, b := range p.Backends {
		if b != nil {
			out = append(out, b)
		}
	}
	slices.SortFunc(out, func(a, b *pool.Backend) int { return strings.Compare(a.ID, b.ID) })
	return out
}

func findPoolConfig(cfg config.Config, poolID string) (config.PoolConfig, bool) {
	for _, pc := range cfg.Pools {
		if pc.ID == poolID {
			return pc, true
		}
	}
	return config.PoolConfig{}, false
}
