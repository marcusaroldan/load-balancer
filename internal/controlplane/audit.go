package controlplane

import (
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// requestIDHeader mirrors runtime.RequestIDHeader; kept as a literal here so
// controlplane does not import runtime (the middleware lives in runtime and
// always runs first; this only reads the value).
const requestIDHeader = "X-Request-ID"

// auditResponseWriter captures the status code for the audit line.
type auditResponseWriter struct {
	http.ResponseWriter
	status int
}

func (a *auditResponseWriter) WriteHeader(code int) {
	a.status = code
	a.ResponseWriter.WriteHeader(code)
}

// ServeHTTP audits every mutation (POST/PUT/DELETE/PATCH) with one structured
// line: who asked for what, outcome, correlation id. Reads are not audited
// (they are high-volume and non-destructive). CORS is deliberately absent on
// this plane: browsers must never call it (same-origin dashboard only).
func (h *APIHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !isMutation(r.Method) {
		h.mux.ServeHTTP(w, r)
		return
	}
	started := time.Now()
	aw := &auditResponseWriter{ResponseWriter: w, status: http.StatusOK}
	h.mux.ServeHTTP(aw, r)
	slog.Info("control-plane mutation",
		"method", r.Method,
		"path", r.URL.Path,
		"status", aw.status,
		"request_id", r.Header.Get(requestIDHeader),
		"source", strings.Split(r.RemoteAddr, "%")[0],
		"duration_ms", time.Since(started).Milliseconds(),
	)
}

func isMutation(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}
