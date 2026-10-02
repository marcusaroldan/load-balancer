package proxy

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// Backend is the legacy static backend descriptor used by cmd/loadbalancer's
// demo stub. Superseded by registry.BackendRef (PROD-003); removed when the
// standalone runtime wiring lands in PROD-008.
type Backend struct {
	ID      string
	Address string
}

// Selector picks a backend from a fixed list. Legacy API; the production path
// selects through the registry instead.
type Selector func([]Backend) Backend

// FirstBackend returns the first backend in the list. Legacy demo behavior.
func FirstBackend(backends []Backend) Backend {
	if len(backends) == 0 {
		return Backend{}
	}
	return backends[0]
}

// NewStaticHandler proxies to a fixed backend list using a legacy selector.
// Renamed from NewHandler to free that name for the runtime-aware Handler.
func NewStaticHandler(backends []Backend, selector Selector) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backend := selector(backends)

		if backend.Address == "" {
			http.Error(w, "no backend available", http.StatusServiceUnavailable)
			return
		}

		targetURL, err := url.Parse(backend.Address)
		if err != nil {
			http.Error(w, "invalid backend address", http.StatusInternalServerError)
			return
		}

		log.Printf("incoming request: %s %s -> backend=%s", r.Method, r.URL.Path, backend.ID)

		proxy := httputil.NewSingleHostReverseProxy(targetURL)
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("proxy error for backend=%s: %v", backend.ID, err)
			http.Error(w, "upstream request failed", http.StatusBadGateway)
		}

		proxy.ServeHTTP(w, r)
	})
}
