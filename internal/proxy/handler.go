package proxy

import (
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"

	"github.com/marcusaroldan/load-balancer/internal/registry"
)

// UpstreamError marks transport failures after backend selection.
type UpstreamError struct {
	BackendID string
	Err       error
}

func (e *UpstreamError) Error() string { return "upstream " + e.BackendID + ": " + e.Err.Error() }
func (e *UpstreamError) Unwrap() error { return e.Err }

// RequestMetrics is a consistent snapshot of handler counters.
type RequestMetrics struct {
	Total           int64
	Success         int64
	GatewayFailures int64 // 502-class: upstream errors and 5xx responses
	RouteFailures   int64 // 503-class: no eligible backend or connection limits
	latencyNanos    int64
}

// AverageLatency reports mean request duration; zero when no requests yet.
func (m RequestMetrics) AverageLatency() time.Duration {
	if m.Total == 0 {
		return 0
	}
	return time.Duration(m.latencyNanos / m.Total)
}

// HandlerConfig wires the runtime-aware data plane to its collaborators.
type HandlerConfig struct {
	Registry *registry.Registry
	// PoolID selects the served pool statically (tests, single-purpose hosts).
	PoolID string
	// DefaultPool resolves the served pool per request when PoolID is empty.
	// An empty resolution means "no pool configured" -> 503 (scale-to-zero).
	DefaultPool func() string
}

// Handler is the PROD-003 data-plane http.Handler serving one pool through the
// registry: selection, bounded connections, forwarding, failure classification.
type Handler struct {
	cfg      HandlerConfig
	mu       sync.Mutex
	counters RequestMetrics
	proxies  map[string]*httputil.ReverseProxy
}

// NewHandler creates the runtime-aware proxy handler.
func NewHandler(cfg HandlerConfig) *Handler {
	return &Handler{cfg: cfg, proxies: make(map[string]*httputil.ReverseProxy)}
}

// ServeHTTP routes one request:
//   - select an eligible backend via the registry snapshot,
//   - reserve a connection slot (max_connections enforced),
//   - forward, classify outcomes, release the slot exactly once.
//
// Status contract: 503 when no backend is routable, 502 when the selected
// upstream fails at transport level.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()

	ref, release, err := h.acquireBackend()
	if err != nil {
		h.record(false, false, started)
		http.Error(w, "no backend available", http.StatusServiceUnavailable)
		return
	}
	defer release()

	target, err := ref.URL()
	if err != nil {
		h.record(false, false, started)
		http.Error(w, "invalid backend address", http.StatusBadGateway)
		return
	}

	recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	h.proxyFor(target).ServeHTTP(recorder, r)

	upstreamFailed := recorder.transportErr != nil
	status5xx := recorder.status >= http.StatusInternalServerError
	h.record(!upstreamFailed && !status5xx, upstreamFailed || status5xx, started)

	if upstreamFailed {
		http.Error(w, "upstream request failed", http.StatusBadGateway)
	}
}

// acquireBackend retries selection when a chosen backend is at its connection
// limit, so capacity pressure redistributes instead of rejecting outright.
func (h *Handler) acquireBackend() (*registry.BackendRef, func(), error) {
	poolID := h.resolvePoolID()
	for attempt := 0; attempt < maxSelectionAttempts; attempt++ {
		snap := h.cfg.Registry.Snapshot()
		ref, err := snap.Select(poolID)
		if err != nil {
			return nil, nil, err
		}
		release, err := h.cfg.Registry.AcquireConnection(poolID, ref.ID())
		if err == nil {
			return ref, release, nil
		}
		if err != registry.ErrConnectionLimitReached {
			return nil, nil, err
		}
	}
	return nil, nil, registry.ErrConnectionLimitReached
}

// resolvePoolID prefers the static PoolID, else asks the resolver (per
// request, so control-plane pool changes apply without rebuilding handlers).
func (h *Handler) resolvePoolID() string {
	if h.cfg.PoolID != "" {
		return h.cfg.PoolID
	}
	if h.cfg.DefaultPool != nil {
		return h.cfg.DefaultPool()
	}
	return ""
}

const maxSelectionAttempts = 3

func (h *Handler) proxyFor(target *url.URL) *httputil.ReverseProxy {
	key := target.String()
	h.mu.Lock()
	defer h.mu.Unlock()
	if proxy, ok := h.proxies[key]; ok {
		return proxy
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	baseDirector := proxy.Director
	proxy.Director = func(r *http.Request) {
		baseDirector(r)
		r.Host = target.Host
	}
	proxy.Transport = defaultTransport()
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		// Flag on the recorder via closure-captured request context is not
		// available here; instead record on the response writer if possible.
		if rec, ok := w.(*statusRecorder); ok {
			rec.transportErr = err
		} else {
			http.Error(w, "upstream request failed", http.StatusBadGateway)
		}
	}
	h.proxies[key] = proxy
	return proxy
}

func defaultTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: defaultDialTimeout, KeepAlive: defaultKeepAlive}
	return &http.Transport{
		DialContext:           dialer.DialContext,
		MaxIdleConnsPerHost:   defaultMaxIdleConnsPerHost,
		ResponseHeaderTimeout: defaultResponseHeaderTimeout,
	}
}

const (
	defaultDialTimeout = 5 * time.Second
	defaultKeepAlive   = 30 * time.Second
)

const (
	defaultMaxIdleConnsPerHost   = 16
	defaultResponseHeaderTimeout = 10 * time.Second
)

// Metrics returns the current counter snapshot.
func (h *Handler) Metrics() RequestMetrics {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.counters
}

func (h *Handler) record(success, gatewayFailure bool, started time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.counters.Total++
	h.counters.latencyNanos += time.Since(started).Nanoseconds()
	switch {
	case success:
		h.counters.Success++
	case gatewayFailure:
		h.counters.GatewayFailures++
	default:
		h.counters.RouteFailures++
	}
}

// statusRecorder captures the upstream status and any transport error without
// buffering the body.
type statusRecorder struct {
	http.ResponseWriter
	status       int
	wroteHeader  bool
	transportErr error
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}
