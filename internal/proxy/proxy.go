package proxy

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
)

type Backend struct {
	ID      string
	Address string
}

type Selector func([]Backend) Backend

func FirstBackend(backends []Backend) Backend {
	if len(backends) == 0 {
		return Backend{}
	}
	return backends[0]
}

func NewHandler(backends []Backend, selector Selector) http.Handler {
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
