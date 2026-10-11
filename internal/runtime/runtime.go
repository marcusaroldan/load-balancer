// Package runtime is the composition root for the standalone load balancer
// service (PROD-008). It wires the registry, health manager, data-plane
// proxy handler, and control-plane API onto two listeners and owns their
// lifecycle: Start opens both planes and the health sweep; Shutdown stops
// health checks, rejects new requests, and drains in-flight work within a
// bounded deadline.
//
// Startup modes (from the standalone plan):
//   - Ephemeral (the only mode today): the registry starts EMPTY. The data
//     plane answers 503 until a pool is configured through the control plane;
//     this is also the demo's scale-to-zero baseline.
//   - Config file load is PROD-006 territory and not wired here yet.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/marcusaroldan/load-balancer/internal/controlplane"
	"github.com/marcusaroldan/load-balancer/internal/health"
	"github.com/marcusaroldan/load-balancer/internal/proxy"
	"github.com/marcusaroldan/load-balancer/internal/registry"
)

// Default listener addresses for the standalone binary. The control plane
// defaults to loopback; exposing it beyond the machine (or the compose
// network) is an explicit operator choice (PROD-009 security boundary).
const (
	DefaultDataAddr    = "0.0.0.0:8080"
	DefaultControlAddr = "127.0.0.1:8082"
)

const (
	defaultDrainTimeout    = 10 * time.Second
	defaultShutdownTimeout = 30 * time.Second
)

var (
	ErrAlreadyStarted = errors.New("runtime already started")
	ErrNotStarted     = errors.New("runtime not started")
	ErrSameAddr       = errors.New("data and control listeners must differ")
)

// Config describes process-level wiring; desired traffic state lives in the
// registry, not here. Addresses accept ":0" for ephemeral ports (tests).
type Config struct {
	DataAddr        string        // data-plane listener
	ControlAddr     string        // control-plane listener
	HealthInterval  time.Duration // sweep cadence; 0 selects health.DefaultSweepInterval()
	DrainTimeout    time.Duration // per-backend drain budget for API deletes
	ShutdownTimeout time.Duration // max wait for in-flight requests at Shutdown
}

func (c *Config) applyDefaults() {
	if c.DataAddr == "" {
		c.DataAddr = DefaultDataAddr
	}
	if c.ControlAddr == "" {
		c.ControlAddr = DefaultControlAddr
	}
	if c.HealthInterval == 0 {
		c.HealthInterval = health.DefaultSweepInterval()
	}
	if c.DrainTimeout <= 0 {
		c.DrainTimeout = defaultDrainTimeout
	}
	if c.ShutdownTimeout <= 0 {
		c.ShutdownTimeout = defaultShutdownTimeout
	}
}

// Runtime owns both planes and the health manager over one shared registry.
type Runtime struct {
	cfg      Config
	registry *registry.Registry

	dataHandler  *proxy.Handler
	healthMgr    *health.Manager
	apiHandler   http.Handler
	lnData       net.Listener
	lnControl    net.Listener
	srvData      *http.Server
	srvControl   *http.Server
	started      bool
	shuttingDown bool
}

// New builds a runtime around an existing (typically empty) registry.
func New(cfg Config, reg *registry.Registry) *Runtime {
	cfg.applyDefaults()
	return &Runtime{cfg: cfg, registry: reg}
}

// Registry exposes the shared registry (used by the simulator harness and
// tests; production traffic only enters through the listeners).
func (rt *Runtime) Registry() *registry.Registry { return rt.registry }

// Config reports the effective (default-applied) runtime configuration, so
// startup logs never print unset zero values.
func (rt *Runtime) Config() Config { return rt.cfg }

// Start opens listeners, then constructs and serves the planes. Ordering is
// deliberate: the control plane is only built after both listeners are live,
// so GET /readyz reporting "serving" is truthful (it gates on the data plane
// being present, which implies its listener succeeded).
func (rt *Runtime) Start(ctx context.Context) error {
	if rt.started {
		return ErrAlreadyStarted
	}
	// Static address collision is a config error; ":0" style ephemeral
	// binds are resolved by the kernel and cannot collide here.
	if rt.cfg.DataAddr == rt.cfg.ControlAddr && !isEphemeral(rt.cfg.DataAddr) {
		return ErrSameAddr
	}

	lnData, err := net.Listen("tcp", rt.cfg.DataAddr)
	if err != nil {
		return fmt.Errorf("listen data plane on %s: %w", rt.cfg.DataAddr, err)
	}
	lnControl, err := net.Listen("tcp", rt.cfg.ControlAddr)
	if err != nil {
		lnData.Close()
		return fmt.Errorf("listen control plane on %s: %w", rt.cfg.ControlAddr, err)
	}
	if lnData.Addr().String() == lnControl.Addr().String() {
		lnData.Close()
		lnControl.Close()
		return ErrSameAddr
	}
	rt.lnData, rt.lnControl = lnData, lnControl

	rt.dataHandler = proxy.NewHandler(proxy.HandlerConfig{
		Registry: rt.registry,
		// Default-pool routing: with exactly one configured pool the data
		// plane serves it; zero pools -> 503 (scale-to-zero); >1 pools needs
		// host/path routing (future ticket, not yet specified).
		DefaultPool: rt.defaultPool,
	})
	rt.healthMgr = health.NewManager(rt.registry, rt.cfg.HealthInterval, nil)

	rt.srvData = &http.Server{Handler: rt.dataHandler, ReadHeaderTimeout: 10 * time.Second}
	rt.srvControl = &http.Server{
		Handler: controlplane.NewAPIHandler(controlplane.APIDeps{
			Registry:     rt.registry,
			DataPlane:    rt.dataHandler,
			DrainTimeout: rt.cfg.DrainTimeout,
			StartedAt:    time.Now(),
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	rt.started = true
	if err := rt.healthMgr.Start(ctx); err != nil {
		return fmt.Errorf("start health manager: %w", err)
	}

	go func() { _ = rt.srvData.Serve(lnData) }()
	go func() { _ = rt.srvControl.Serve(lnControl) }()
	return nil
}

// Shutdown stops health checks first (no new probe work), then gracefully
// drains both servers within the configured deadline.
func (rt *Runtime) Shutdown(ctx context.Context) error {
	if !rt.started || rt.shuttingDown {
		return ErrNotStarted
	}
	rt.shuttingDown = true

	if ctx == nil || ctx.Done() == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), rt.cfg.ShutdownTimeout)
		defer cancel()
	}

	var errs []error
	if rt.healthMgr != nil {
		if err := rt.healthMgr.Stop(); err != nil && !errors.Is(err, health.ErrNotRunning) {
			errs = append(errs, fmt.Errorf("stop health manager: %w", err))
		}
	}
	// Control plane first: config writes should stop before traffic drain
	// (a slice keeps this order deterministic; a map would not).
	servers := []struct {
		name string
		srv  *http.Server
	}{
		{"control", rt.srvControl},
		{"data", rt.srvData},
	}
	for _, s := range servers {
		if s.srv == nil {
			continue
		}
		if err := s.srv.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown %s plane: %w", s.name, err))
		}
	}
	return errors.Join(errs...)
}

// DataPlaneAddr / ControlPlaneAddr return resolved listener addresses
// (needed when ":0" was requested).
func (rt *Runtime) DataPlaneAddr() string    { return addrOf(rt.lnData) }
func (rt *Runtime) ControlPlaneAddr() string { return addrOf(rt.lnControl) }

// defaultPool implements the data-plane pool resolution rule: exactly one
// configured pool wins; anything else yields "" (-> 503).
func (rt *Runtime) defaultPool() string {
	snap := rt.registry.Snapshot()
	pools := snap.Pools()
	if len(pools) == 1 {
		return pools[0]
	}
	return ""
}

// isEphemeral reports a bind string whose port is 0 (kernel-assigned).
func isEphemeral(addr string) bool {
	_, port, err := net.SplitHostPort(addr)
	return err == nil && port == "0"
}

func addrOf(ln net.Listener) string {
	if ln == nil {
		return ""
	}
	return ln.Addr().String()
}
