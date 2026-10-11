package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/marcusaroldan/load-balancer/internal/runtime"
)

func TestParseFlagsDefaults(t *testing.T) {
	cfg, opts, err := parseFlags(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if opts.showVersion {
		t.Fatal("version off by default")
	}
	if opts.healthcheck != "" {
		t.Fatal("healthcheck off by default")
	}
	if cfg.DataAddr != runtime.DefaultDataAddr || cfg.ControlAddr != runtime.DefaultControlAddr {
		t.Fatalf("defaults: %+v", cfg)
	}
	if cfg.HealthInterval != 0 {
		t.Fatalf("health interval should defer to runtime default, got %v", cfg.HealthInterval)
	}
}

func TestParseFlagsOverrides(t *testing.T) {
	cfg, _, err := parseFlags([]string{
		"-data", "0.0.0.0:9000",
		"-control", "127.0.0.1:9082",
		"-health-interval", "3s",
		"-drain-timeout", "5s",
		"-shutdown-deadline", "30s",
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataAddr != "0.0.0.0:9000" || cfg.ControlAddr != "127.0.0.1:9082" {
		t.Fatalf("addrs: %+v", cfg)
	}
	if cfg.HealthInterval != 3*time.Second || cfg.DrainTimeout != 5*time.Second || cfg.ShutdownTimeout != 30*time.Second {
		t.Fatalf("durations: %+v", cfg)
	}
}

func TestParseFlagsVersionAndErrors(t *testing.T) {
	_, opts, err := parseFlags([]string{"-version"}, io.Discard)
	if err != nil || !opts.showVersion {
		t.Fatalf("-version: %v %v", opts.showVersion, err)
	}
	_, opts, err = parseFlags([]string{"-healthcheck", "http://127.0.0.1:1/healthz"}, io.Discard)
	if err != nil || opts.healthcheck != "http://127.0.0.1:1/healthz" {
		t.Fatalf("-healthcheck: %+v %v", opts, err)
	}

	var errOut bytes.Buffer
	if _, _, err := parseFlags([]string{"-data"}, &errOut); err == nil {
		t.Fatal("missing value should fail")
	}
	if _, _, err := parseFlags([]string{"-health-interval", "banana"}, &errOut); err == nil {
		t.Fatal("bad duration should fail")
	}
	if !strings.Contains(errOut.String(), "loadbalancer") {
		t.Fatalf("flag errors should name the binary: %q", errOut.String())
	}
}
