package runtime

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

// RequestIDHeader is the correlation header generated (or preserved) at the
// edge and forwarded to upstreams, so a request can be traced through LB
// logs, the proxy, and backend logs by one id.
const RequestIDHeader = "X-Request-ID"

// requestIDBytes is the entropy of generated ids (8 bytes -> 16 hex chars).
const requestIDBytes = 8

// withRequestID assigns a request id when the client did not supply one,
// echoes it on the response, and stores it on the request so downstream
// handlers (ReverseProxy header copy, audit logs) see the same value.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if id == "" {
			id = newRequestID()
		}
		r.Header.Set(RequestIDHeader, id)
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r)
	})
}

// newRequestID returns a random hex id. crypto/rand cannot realistically
// fail here; on failure an empty id is returned rather than panicking in
// the serving path (the header is optional, not load-bearing).
func newRequestID() string {
	buf := make([]byte, requestIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	return hex.EncodeToString(buf)
}
