package runtime

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestRuntimeMultiPoolHostRouting covers the multi-pool data-plane rule:
// a request's Host header selects the pool that claimed that hostname
// (port stripped, case-insensitive); an unclaimed hostname among multiple
// pools is a 503, and the single-pool default is unchanged.
func TestRuntimeMultiPoolHostRouting(t *testing.T) {
	b1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "one")
	}))
	defer b1.Close()
	b2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "two")
	}))
	defer b2.Close()

	rt := newStarted(t, Config{HealthInterval: 100 * time.Millisecond, ShutdownTimeout: 3 * time.Second})
	control := "http://" + rt.ControlPlaneAddr()
	data := "http://" + rt.DataPlaneAddr()

	if rec := controlPost(t, control, "/pool", map[string]any{"pool_id": "api", "host": "API.Example.com"}); rec.StatusCode != http.StatusCreated {
		t.Fatalf("create api pool: %d", rec.StatusCode)
	}
	if rec := controlPost(t, control, "/pool", map[string]any{"pool_id": "web", "host": "www.example.com"}); rec.StatusCode != http.StatusCreated {
		t.Fatalf("create web pool: %d", rec.StatusCode)
	}
	h1Addr, h1Port := splitHTTP(b1.URL)
	h2Addr, h2Port := splitHTTP(b2.URL)
	if rec := controlPost(t, control, "/backend", map[string]any{"backend_id": "b1", "pool": "api", "address": h1Addr, "port": h1Port}); rec.StatusCode != http.StatusCreated {
		t.Fatalf("create b1: %d", rec.StatusCode)
	}
	if rec := controlPost(t, control, "/backend", map[string]any{"backend_id": "b2", "pool": "web", "address": h2Addr, "port": h2Port}); rec.StatusCode != http.StatusCreated {
		t.Fatalf("create b2: %d", rec.StatusCode)
	}

	hostedGet := func(host string) (int, string) {
		req, err := http.NewRequest(http.MethodGet, data+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, string(body)
	}

	// Exact host match routes to the claiming pool; case and port are
	// normalized away on both sides.
	for _, host := range []string{"api.example.com", "API.EXAMPLE.COM", "api.example.com:8443"} {
		status, body := hostedGet(host)
		if status != http.StatusOK || body != "one" {
			t.Fatalf("host %q: got %d %q", host, status, body)
		}
	}
	status, body := hostedGet("www.example.com")
	if status != http.StatusOK || body != "two" {
		t.Fatalf("www host: got %d %q", status, body)
	}

	// Unclaimed hostname among multiple pools -> 503 (no default pick).
	status, _ = hostedGet("unknown.example.com")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("unknown host: got %d", status)
	}
}
