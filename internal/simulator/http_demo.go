package simulator

import (
	"fmt"
	"net/http"
)

// DemoServer starts a small dashboard-friendly HTTP server exposing the current simulation state.
func DemoServer(addr string, h *Harness) error {
	if addr == "" {
		addr = ":8081"
	}
	if h == nil {
		return fmt.Errorf("simulator harness is required")
	}

	httpServer := &http.Server{
		Addr:    addr,
		Handler: NewHTTPHandler(h),
	}
	return httpServer.ListenAndServe()
}
