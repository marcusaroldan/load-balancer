// cmd/loadbalancer is the standalone load balancer service (PROD-008).
// It parses process flags, builds the runtime composition root, and manages
// the signal-driven lifecycle. No backends, pools, or routing decisions are
// hardcoded here: state enters exclusively through the control plane.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/marcusaroldan/load-balancer/internal/healthprobe"
	"github.com/marcusaroldan/load-balancer/internal/registry"
	"github.com/marcusaroldan/load-balancer/internal/runtime"
)

const version = "v0.3.0-dev"

func main() {
	cfg, opts, err := parseFlags(os.Args[1:], os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid flags: %v\n", err)
		os.Exit(2)
	}
	if opts.showVersion {
		fmt.Println("load-balancer", version)
		return
	}
	if opts.healthcheck != "" {
		// Container HEALTHCHECK helper: the scratch image has no curl,
		// so the service binary probes its own control plane.
		if err := healthprobe.Probe(context.Background(), opts.healthcheck); err != nil {
			log.Printf("UNHEALTHY: %v", err)
			os.Exit(1)
		}
		log.Printf("healthy")
		os.Exit(0)
	}

	if err := run(cfg); err != nil {
		log.Fatalf("load balancer exited: %v", err)
	}
	log.Print("shutdown complete")
}

// run wires the runtime and blocks until SIGINT/SIGTERM, then drains within
// the configured shutdown deadline.
func run(cfg runtime.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	rt := runtime.New(cfg, registry.New())
	if err := rt.Start(ctx); err != nil {
		return err
	}

	eff := rt.Config()
	log.Printf("load balancer %s: data plane on %s, control plane on %s, health interval %s, drain budget %s, shutdown deadline %s, persistence ephemeral, pools at start: %d",
		version, rt.DataPlaneAddr(), rt.ControlPlaneAddr(),
		eff.HealthInterval, eff.DrainTimeout, eff.ShutdownTimeout,
		len(rt.Registry().Config().Pools))

	<-ctx.Done()
	stop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	return rt.Shutdown(shutdownCtx)
}

// cliOptions captures the "act-and-exit" flags (version, healthcheck); the
// rest of the flags configure the long-running service.
type cliOptions struct {
	showVersion bool
	healthcheck string
}

func parseFlags(args []string, errOut io.Writer) (runtime.Config, cliOptions, error) {
	fs := flag.NewFlagSet("loadbalancer", flag.ContinueOnError)
	fs.SetOutput(errOut)

	var cfg runtime.Config
	var opts cliOptions
	fs.BoolVar(&opts.showVersion, "version", false, "print version and exit")
	fs.StringVar(&opts.healthcheck, "healthcheck", "", "probe this URL and exit (0 = healthy, 1 = unhealthy) instead of starting; for container HEALTHCHECK")
	fs.StringVar(&cfg.DataAddr, "data", runtime.DefaultDataAddr, "data-plane listener address (host:port)")
	fs.StringVar(&cfg.ControlAddr, "control", runtime.DefaultControlAddr, "control-plane listener address (host:port); keep loopback unless an operator network requires more")
	fs.DurationVar(&cfg.HealthInterval, "health-interval", 0, "health sweep cadence (0 = system default)")
	fs.DurationVar(&cfg.DrainTimeout, "drain-timeout", 0, "per-backend drain budget for control-plane deletes (0 = system default)")
	fs.DurationVar(&cfg.ShutdownTimeout, "shutdown-deadline", 0, "max wait for in-flight requests at shutdown (0 = system default)")
	fs.BoolVar(&cfg.AllowPublicControl, "expose-control", false, "allow a non-loopback control-plane bind (trusted operator networks only, e.g. a compose bridge; never publish this port to the internet)")

	if err := fs.Parse(args); err != nil {
		return cfg, opts, err
	}
	return cfg, opts, nil
}
