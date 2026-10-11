package healthprobe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProbeOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := Probe(context.Background(), srv.URL); err != nil {
		t.Fatalf("healthy probe: %v", err)
	}
}

func TestProbeNonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	err := Probe(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("want 503 failure, got %v", err)
	}
}

func TestProbeTransportFailure(t *testing.T) {
	// Closed server -> connection refused must be an error, not a panic.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	if err := Probe(context.Background(), url); err == nil {
		t.Fatal("unreachable endpoint should fail")
	}
}

func TestProbeTimeoutBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(DefaultTimeout + 500*time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	start := time.Now()
	if err := Probe(context.Background(), srv.URL); err == nil {
		t.Fatal("stalled server should time out")
	}
	if elapsed := time.Since(start); elapsed > DefaultTimeout+1*time.Second {
		t.Fatalf("probe exceeded its bound: %s", elapsed)
	}
}
