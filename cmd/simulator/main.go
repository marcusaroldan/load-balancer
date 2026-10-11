package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/marcusaroldan/load-balancer/internal/simulator"
)

const (
	defaultBindHost = "0.0.0.0"
	defaultPort     = 8081
	defaultStatic   = "web"

	// apiBodyLimit caps every /api request body; scenario payloads are
	// tiny, this only exists to blunt junk floods.
	apiBodyLimit = 64 << 10

	// idleWatchdog samples staleness at timeout/divisor, bounded so tiny
	// test timeouts do not turn the watchdog into a busy loop.
	idleWatchdogDivisor     = 4
	idleWatchdogMinInterval = 500 * time.Millisecond
)

type updateRequest struct {
	Value string `json:"value"`
}

func main() {
	host := flag.String("host", defaultBindHost, "bind host")
	port := flag.Int("port", defaultPort, "bind port")
	staticDir := flag.String("static", filepath.Join(".", defaultStatic), "static assets directory")
	backendBind := flag.String("backend-bind", "127.0.0.1", "host simulated backends bind listeners to (loopback unless an out-of-process load balancer shares the network)")
	backendAdvertise := flag.String("backend-advertise", "", "host advertised in backend URLs for load-balancer registration (defaults to --backend-bind; set the container service DNS name in compose)")
	lbControl := flag.String("lb-control", "", "control-plane URL of an out-of-process load balancer (e.g. http://lb:8082); empty runs the simulator with the in-process harness")
	lbData := flag.String("lb-data", "", "data-plane URL synthetic traffic is sent to (required with --lb-control)")
	idleTimeout := flag.Duration("idle-timeout", 0, "scale the demo to zero after this long with no dashboard traffic (0 disables)")
	flag.Parse()

	binding := simulator.BackendBinding{BindHost: *backendBind, AdvertiseHost: *backendAdvertise}
	opts := []simulator.HarnessOption{simulator.WithBackendBinding(binding)}
	if (*lbControl == "") != (*lbData == "") {
		log.Fatal("--lb-control and --lb-data must be set together")
	}
	if *lbControl != "" {
		opts = append(opts, simulator.WithAdapterFactory(
			simulator.NewRemoteAdapterFactory(simulator.RemoteLBConfig{ControlURL: *lbControl, DataURL: *lbData})))
		log.Printf("load balancer mode: remote (control %s, data %s)", *lbControl, *lbData)
	}
	harness, err := simulator.NewHarness(simulator.PresetScenarioConfigs["stable"], opts...)
	if err != nil {
		log.Fatalf("create simulator harness: %v", err)
	}
	defer harness.Stop()

	mux := http.NewServeMux()

	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(harness.State()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	mux.HandleFunc("/api/reset", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req updateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := harness.ResetScenario(req.Value); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/api/strategy", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req updateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := harness.SetStrategy(req.Value); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/api/request-rate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req updateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var rate int
		if _, err := fmt.Sscanf(req.Value, "%d", &rate); err != nil {
			http.Error(w, "value must be an integer", http.StatusBadRequest)
			return
		}
		if err := harness.SetRequestRate(rate); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/api/client/add", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := harness.AddClient(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/api/client/remove", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := harness.RemoveClient(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/api/pool/add", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req updateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := harness.AddPool(req.Value); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/api/backend/add", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		id, _ := req["id"].(string)
		weight, _ := req["weight"].(float64)
		latency, _ := req["latency"].(string)
		healthy, _ := req["healthy"].(bool)
		pool, _ := req["pool"].(string)
		if id == "" {
			http.Error(w, "id is required", http.StatusBadRequest)
			return
		}
		// address/port are intentionally NOT read from the request body:
		// bind policy belongs to the operator flags (S1 security boundary),
		// and listener ports are kernel-assigned.
		if _, err := harness.AddManagedBackend(id, pool, int(weight), parseDuration(latency), healthy); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/api/backend/remove", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		id, _ := req["id"].(string)
		if id == "" {
			http.Error(w, "id is required", http.StatusBadRequest)
			return
		}
		if err := harness.RemoveBackend(id); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/api/backend/name", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		id, _ := req["id"].(string)
		name, _ := req["name"].(string)
		if err := harness.SetName(id, name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/api/backend/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		id, _ := req["id"].(string)
		healthy, _ := req["healthy"].(bool)
		if err := harness.SetHealthy(id, healthy); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/api/backend/latency", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		id, _ := req["id"].(string)
		latency, _ := req["latency"].(string)
		if err := harness.SetLatency(id, parseDuration(latency)); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/api/backend/weight", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		id, _ := req["id"].(string)
		weight, _ := req["weight"].(float64)
		if err := harness.SetWeight(id, int(weight)); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/api/backend/pool", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		id, _ := req["id"].(string)
		pool, _ := req["pool"].(string)
		if err := harness.SetBackendPool(id, pool); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/api/backend/failure-rate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		id, _ := req["id"].(string)
		rate, _ := req["rate"].(float64)
		if err := harness.SetFailureRate(id, rate); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	staticFS := http.Dir(*staticDir)
	mux.Handle("/", http.FileServer(staticFS))

	addr := fmt.Sprintf("%s:%d", *host, *port)
	lastActivity := &atomic.Int64{}
	lastActivity.Store(time.Now().UnixNano())
	server := &http.Server{
		Addr:              addr,
		Handler:           bodyLimit(noteActivity(lastActivity, mux), apiBodyLimit),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
	}

	if *idleTimeout > 0 {
		stopWatchdog := make(chan struct{})
		defer close(stopWatchdog)
		go idleWatchdog(harness, lastActivity, *idleTimeout, stopWatchdog)
		log.Printf("idle scale-to-zero armed: %s", *idleTimeout)
	}

	log.Printf("simulator dashboard listening on %s (backend bind %q advertise %q)", addr, *backendBind, effectiveAdvertise(*backendBind, *backendAdvertise))
	log.Fatal(server.ListenAndServe())
}

// effectiveAdvertise mirrors simulator.BackendBinding's fallback purely for
// the startup log line.
func effectiveAdvertise(bind, advertise string) string {
	if advertise != "" {
		return advertise
	}
	if bind == "" || bind == "0.0.0.0" || bind == "::" || bind == "*" {
		return "127.0.0.1"
	}
	return bind
}

// noteActivity records every dashboard/API request as "somebody is using
// the simulator" (an open tab polls /api/state continuously; closing the
// tab lets the idle clock run out) for the scale-to-zero watchdog.
func noteActivity(marker *atomic.Int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		marker.Store(time.Now().UnixNano())
		next.ServeHTTP(w, r)
	})
}

// idleWatchdog enters the zero state after the marker goes stale. The tick
// interval is a fraction of the timeout so "no traffic for N" is detected
// promptly without busy polling; EnterIdleMode is idempotent by design.
func idleWatchdog(harness *simulator.Harness, marker *atomic.Int64, timeout time.Duration, stop <-chan struct{}) {
	interval := max(idleWatchdogMinInterval, timeout/idleWatchdogDivisor)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			last := time.Unix(0, marker.Load())
			if time.Since(last) < timeout {
				continue
			}
			if err := harness.EnterIdleMode(); err != nil {
				log.Printf("idle scale-to-zero failed: %v", err)
			}
		}
	}
}

// bodyLimit caps request bodies so dashboard floods cannot exhaust memory.
func bodyLimit(next http.Handler, maxBytes int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		}
		next.ServeHTTP(w, r)
	})
}

func parseDuration(value string) time.Duration {
	if value == "" {
		return 0
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0
	}
	return duration
}
