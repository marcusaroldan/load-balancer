package controlplane

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/marcusaroldan/load-balancer/internal/pool"
)

// CreateBackendRequest contains backend creation data for the control plane API.
type CreateBackendRequest struct {
	BackendID string `json:"backend_id"`
	PoolID    string `json:"pool_id"`
	Address   string `json:"address"`
	Port      int    `json:"port"`
}

// CreateBackendResponse is returned after a backend is successfully created.
type CreateBackendResponse struct {
	ID      string `json:"id"`
	PoolID  string `json:"pool_id"`
	Address string `json:"address"`
	Port    int    `json:"port"`
}

// BackendResponse is the serialized backend object visible via the API.
type BackendResponse struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Port    int    `json:"port"`
	Healthy bool   `json:"healthy"`
}

// PoolResponse is the serialized pool and its members.
type PoolResponse struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Backends []BackendResponse `json:"backends"`
}

// Handler exposes the basic control plane endpoints for backend and pool management.
type Handler struct {
	store *pool.InMemoryStore
}

// NewHandler creates a control-plane handler bound to the in-memory pool store.
func NewHandler(store *pool.InMemoryStore) *Handler {
	return &Handler{store: store}
}

// ServeHTTP routes control-plane requests.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && r.URL.Path == "/backend" {
		h.handleCreateBackend(w, r)
		return
	}
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/pool/") {
		poolID := strings.TrimPrefix(r.URL.Path, "/pool/")
		if poolID == "" {
			h.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "pool id is required"})
			return
		}
		h.handleGetPool(w, r, poolID)
		return
	}

	h.writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
}

func (h *Handler) handleCreateBackend(w http.ResponseWriter, r *http.Request) {
	if r.Body == nil {
		h.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request body is required"})
		return
	}

	var req CreateBackendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON payload"})
		return
	}

	if err := validateCreateBackendRequest(req); err != nil {
		h.writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	backend := pool.Backend{
		ID:      req.BackendID,
		Address: req.Address,
		Port:    req.Port,
		Healthy: true,
	}

	if err := h.store.AddBackend(req.PoolID, backend); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errDuplicateBackend) || strings.Contains(err.Error(), "already exists") {
			status = http.StatusConflict
		}
		h.writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	response := CreateBackendResponse{
		ID:      backend.ID,
		PoolID:  req.PoolID,
		Address: backend.Address,
		Port:    backend.Port,
	}

	h.writeJSON(w, http.StatusCreated, response)
}

func (h *Handler) handleGetPool(w http.ResponseWriter, r *http.Request, poolID string) {
	p, ok := h.store.GetPool(poolID)
	if !ok {
		h.writeJSON(w, http.StatusNotFound, map[string]string{"error": "pool not found"})
		return
	}

	backends := make([]BackendResponse, 0, len(p.Backends))
	for _, backend := range p.Backends {
		if backend == nil {
			continue
		}
		backends = append(backends, BackendResponse{
			ID:      backend.ID,
			Address: backend.Address,
			Port:    backend.Port,
			Healthy: backend.Healthy,
		})
	}

	response := PoolResponse{
		ID:       p.ID,
		Name:     p.Name,
		Backends: backends,
	}

	h.writeJSON(w, http.StatusOK, response)
}

func validateCreateBackendRequest(req CreateBackendRequest) error {
	if req.BackendID == "" {
		return errors.New("backend_id is required")
	}
	if req.PoolID == "" {
		return errors.New("pool_id is required")
	}
	if req.Address == "" {
		return errors.New("address is required")
	}
	if req.Port <= 0 || req.Port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	return nil
}

func (h *Handler) writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
	}
}

var errDuplicateBackend = errors.New("duplicate backend")

func (h *Handler) createBackendID(poolID string, port int) string {
	return fmt.Sprintf("%s-%s", poolID, strconv.Itoa(port))
}
