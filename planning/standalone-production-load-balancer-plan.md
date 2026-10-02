# Standalone Production Load Balancer Implementation Plan

## Objective

Replace the hard-coded proxy in `cmd/loadbalancer` with a standalone, dynamically configurable load balancer runtime. The runtime must own request routing, backend health, pool configuration, optional persistence, and the complete control-plane API. The simulator should consume the same public runtime contract rather than maintain a separate routing implementation.

This plan is intentionally incremental. The existing simulator integration remains useful as a regression harness while the production runtime is assembled behind stable interfaces.

## Current baseline

The repository already contains:

- HTTP proxy forwarding in `internal/proxy`.
- In-memory pools and backends in `internal/pool`.
- Round-robin, weighted-round-robin, and least-connections strategies in `internal/routing`.
- A separate round-robin selector in `internal/selector`.
- Basic health probing in `internal/health`.
- A TTL session store in `internal/sticky`.
- JSON file persistence in `internal/configstore`.
- Partial control-plane endpoints in `internal/controlplane`.
- A real in-process routing adapter used by the simulator.

The current production entry point still uses two hard-coded backends and `proxy.FirstBackend`. The existing packages are not composed into a standalone runtime, and the control plane does not mutate the serving data plane.

## Target architecture

```text
cmd/loadbalancer
  |
  +-- runtime.Runtime
        |
        +-- config.Manager ---- optional configstore.Store
        +-- pool.Registry
        +-- routing.Selector
        +-- health.Manager
        +-- sticky.Store (optional)
        +-- proxy.Handler
        +-- controlplane.Handler
        +-- metrics.Snapshot
```

The runtime should expose two listeners or two clearly separated routes:

- Data plane: client requests are routed to a selected healthy backend.
- Control plane: authenticated or locally restricted configuration and status operations.

The runtime must use one shared configuration snapshot for both listeners. A control-plane mutation is validated completely, applied atomically, reflected in routing, and persisted only after the in-memory update succeeds.

## Design decisions

### Runtime ownership

Add an `internal/runtime` package (or equivalent) as the composition root. It should provide lifecycle methods such as `Start`, `Shutdown`, `ServeDataPlane`, `ServeControlPlane`, and a test-friendly constructor. `cmd/loadbalancer/main.go` should only parse flags, build dependencies, and handle shutdown signals.

### Configuration model

Use one canonical configuration model for runtime, pools, backends, health policy, routing policy, and sticky-session settings. Keep operational health observations separate from desired configuration where possible:

- Desired state: address, port, enabled, weight, limits, pool, health policy.
- Observed state: healthy/unhealthy, consecutive successes/failures, last check time, active connections.

Do not persist transient counters or active connections as desired configuration.

### Concurrency and atomicity

Mutations should be serialized by a configuration manager. Reads and request selection should use a consistent snapshot or read lock. A failed validation or persistence write must leave the currently serving configuration unchanged. Backend removal must support graceful draining with a bounded timeout.

### Strategy support

Preserve the existing strategy names:

- `round_robin`
- `weighted_round_robin`
- `least_connections`

Add `ip_hash` and `random` only when their behavior is specified and tested. Unknown strategies must return a client error; silently falling back to round-robin is not acceptable for production configuration.

### Optional persistence

Persistence is disabled by default for an ephemeral process and enabled with an explicit config path or flag. Startup behavior should be selectable:

- No config file: start with an empty configuration or documented bootstrap config.
- Valid config file: load, validate, and apply before opening listeners.
- Invalid config file: fail startup with an actionable error; do not partially start.

The first implementation remains file-backed JSON. The `configstore.Store` interface should make later stores possible without changing the runtime.

## Work breakdown

### PROD-001: Canonical runtime configuration and validation

**Goal:** Define the complete desired-state model and reject invalid configurations before they reach the serving path.

**Scope:** Extend the current config types to represent pools, algorithms, health policy, sticky sessions, backend limits, listener settings, and graceful-drain settings. Define defaults and validation errors.

**Acceptance criteria:**

- Configuration can represent every field in the system design document.
- Defaults are applied consistently for CLI, API, and persisted config.
- Invalid ports, duplicate IDs, missing pools, invalid weights, invalid thresholds, unsupported algorithms, and invalid sticky TTLs are rejected.
- Validation has table-driven unit tests.

**Dependencies:** none.

### PROD-002: Unified pool registry and routing service

**Goal:** Make one routing service the only production selection path.

**Scope:** Build a registry that supports create, update, delete, list, and snapshot operations for pools and backends. Unify or retire the duplicated selector behavior in `internal/selector` and `internal/routing`. Add strategy instances with per-pool state and safe reset behavior after membership or health changes.

**Acceptance criteria:**

- Data-plane requests select from the configured pool registry.
- All strategies exclude disabled or unhealthy backends.
- Strategy changes apply to the selected pool without restarting the process.
- Pool membership changes do not corrupt selector state.
- Concurrent selection and mutation tests pass under `go test -race`.

**Dependencies:** PROD-001.

### PROD-003: Production proxy handler and request lifecycle

**Goal:** Replace the fixed `FirstBackend` handler with a runtime-aware data plane.

**Scope:** Select a pool, select a backend through the routing service, proxy the request, track request and active-connection metrics, enforce `max_connections`, classify upstream failures, and implement graceful backend draining.

**Acceptance criteria:**

- Listener and upstream addresses are configurable.
- Requests are distributed according to the configured strategy.
- No healthy backend returns `503`; upstream failures return the documented gateway error.
- Backend connection limits are enforced without leaking counters.
- Removing a backend prevents new selections and drains existing requests until timeout.
- Integration tests use `httptest` backends and verify response forwarding.

**Dependencies:** PROD-002.

### PROD-004: Enhanced health management

**Goal:** Turn basic probing into a configurable health manager that safely drives routing eligibility.

**Scope:** Add per-backend or per-pool health path and interval, request timeout, healthy and unhealthy thresholds, startup grace period, and observable transition state. Health updates must go through the registry rather than mutate detached objects.

**Acceptance criteria:**

- A backend becomes unhealthy only after the configured consecutive failure threshold.
- An unhealthy backend becomes eligible only after the configured consecutive success threshold.
- Timeouts, non-2xx responses, and transport errors are classified as failures.
- Health transitions are visible through the control plane and immediately affect selection.
- Start, stop, cancellation, and repeated lifecycle operations are race-free.
- Tests cover threshold hysteresis, recovery, timeout, removal during a check, and all-backends-unhealthy behavior.

**Dependencies:** PROD-001, PROD-002.

### PROD-005: Sticky sessions as an optional routing policy

**Goal:** Integrate the existing TTL store into request routing without making it mandatory.

**Scope:** Add cookie extraction, backend validation, assignment, TTL refresh policy, and fallback when the mapped backend is unavailable. Keep the store behind an interface so a shared cache can be added later.

**Acceptance criteria:**

- Sticky sessions are disabled unless configured.
- A valid session remains on an eligible backend until expiry or backend removal.
- Expired, malformed, or unavailable mappings fall back to normal selection.
- Cookies are set with documented security and lifetime attributes.
- Tests cover assignment, expiry, unhealthy backend fallback, deletion, and concurrent access.

**Dependencies:** PROD-002, PROD-003.

### PROD-006: Optional configuration persistence and reload

**Goal:** Make the existing file store usable by the standalone runtime without making disk state mandatory.

**Scope:** Extend the JSON schema to the canonical configuration, validate on load, write atomically via a temporary file and rename, and save successful control-plane mutations. Add a startup flag such as `-config` and an explicit disabled/ephemeral mode.

**Acceptance criteria:**

- The process can start with persistence disabled.
- A valid file is loaded before serving traffic.
- Invalid persisted configuration fails startup without serving a partial state.
- Writes are atomic and preserve the previous valid file if a write fails.
- API mutations either persist successfully or return an error without changing active state.
- Save/load, migration/defaulting, malformed file, permission, and rollback tests exist.

**Dependencies:** PROD-001, PROD-002.

### PROD-007: Complete control-plane API

**Goal:** Provide the management API described by the system design and connect it to the live runtime.

**Required endpoints:**

| Method | Endpoint | Purpose |
|---|---|---|
| `POST` | `/backend` | Create a backend in a pool |
| `GET` | `/backend/{backend_id}` | Return desired and observed backend state |
| `DELETE` | `/backend/{backend_id}` | Disable, drain, and remove a backend |
| `GET` | `/backend/{backend_id}/health` | Return health status and probe counters |
| `POST` | `/pool` | Create a pool |
| `GET` | `/pool` | List pools |
| `GET` | `/pool/{pool_id}` | Return pool configuration and members |
| `DELETE` | `/pool/{pool_id}` | Drain members and delete a pool |
| `PUT` | `/config/algorithm` | Set strategy and sticky-session settings |
| `GET` | `/config` | Return effective non-secret configuration |
| `GET` | `/healthz` | Process health and dependency status |
| `GET` | `/readyz` | Ready only after configuration and listeners are active |
| `GET` | `/metrics` | Aggregate request, latency, health, and connection metrics |

**Acceptance criteria:**

- Request and response schemas are documented and versioned.
- IDs, pool membership, ports, weights, thresholds, and algorithm values are validated.
- Errors use consistent JSON fields and appropriate `4xx` or `5xx` statuses.
- API mutations update the same runtime used by data-plane requests.
- Delete operations report drain status or a documented asynchronous operation state.
- Handler tests cover every route, invalid payload, missing resource, conflict, and rollback case.

**Dependencies:** PROD-001 through PROD-004, PROD-006 for persistence-aware behavior.

### PROD-008: Standalone command wiring and operational controls

**Goal:** Make `cmd/loadbalancer` a usable service rather than a demo proxy.

**Scope:** Wire the runtime, load configuration, open data/control listeners, handle `SIGINT`/`SIGTERM`, apply shutdown deadlines, and expose flags or environment variables for listener addresses, control-plane address, config path, health defaults, and drain timeout.

**Acceptance criteria:**

- No hard-coded backend addresses or `FirstBackend` usage remains in the production command.
- Startup logs identify listeners, persistence mode, and configured pool count without secrets.
- Startup fails cleanly on invalid configuration or occupied listener ports.
- Shutdown stops health checks, rejects new requests, drains in-flight requests, and exits within the configured deadline.
- A process-level integration test starts the command components and exercises data and control planes.

**Dependencies:** PROD-003, PROD-004, PROD-006, PROD-007.

### PROD-009: Observability, security boundary, and documentation

**Goal:** Make the service diagnosable and safe enough for a small real deployment.

**Scope:** Add Prometheus-compatible aggregate metrics, structured request/control-plane logs, request IDs, control-plane bind restrictions or authentication hook, CORS policy only where explicitly needed, and operator documentation.

**Acceptance criteria:**

- Metrics include request totals, status classes, latency, active connections, backend health, and rejected selections.
- No selected backend identity, route history, session mapping, or private routing state is exposed to the simulator UI.
- Control-plane exposure defaults to loopback or requires an explicit unsafe/public configuration.
- `DEPLOYMENT.md` documents configuration, persistence, health behavior, ports, shutdown, and backup expectations.
- README quick-start instructions run the standalone service with a local backend.

**Dependencies:** PROD-008.

## Suggested execution order

1. PROD-001: canonical configuration and validation.
2. PROD-002: unified registry and routing service.
3. PROD-003: production proxy and request lifecycle.
4. PROD-004: enhanced health manager.
5. PROD-007: control plane against the live runtime.
6. PROD-006: optional persistence and startup reload.
7. PROD-005: sticky-session integration.
8. PROD-008: standalone command wiring and graceful shutdown.
9. PROD-009: metrics, security boundary, and documentation.

The control plane should be implemented before persistence is enabled so persistence stores validated runtime configuration rather than duplicating API-specific behavior.

## Test and verification strategy

Each phase should add focused tests before moving to the next phase:

- Unit tests for validation, state transitions, strategy behavior, and persistence rollback.
- Handler tests using `httptest.NewRecorder` for every control-plane route.
- Integration tests with multiple `httptest.Server` backends for forwarding, distribution, health changes, failure responses, draining, and reload.
- Race tests for the runtime, health manager, and control plane.
- Process smoke test for startup, readiness, API mutation, data-plane traffic, and graceful shutdown.
- Full verification: `go test ./... -count=1`, `go test -race ./... -count=1`, `go build ./...`, and `go vet ./...`.

## Milestones

### Milestone A: Real standalone data plane

Complete PROD-001 through PROD-003 and PROD-008. The production command routes configurable pools through the real selector and proxy, with no simulator dependency.

### Milestone B: Operational control

Complete PROD-004 and PROD-007. Health transitions and all management operations affect live traffic atomically.

### Milestone C: Durable optional service

Complete PROD-006 and PROD-005. Operators can choose ephemeral or file-backed configuration and optionally enable sticky sessions.

### Milestone D: Deployable service

Complete PROD-009, then add containerization and deployment automation as a follow-up operational ticket if the target host requires it.

## Definition of done

The standalone production load balancer is complete when:

- `cmd/loadbalancer` starts a configurable runtime with no hard-coded backends.
- All data-plane routing goes through the canonical pool and selector service.
- Health thresholds, timeouts, recovery, and draining are tested and visible through the API.
- The complete control-plane API mutates live state with validation and atomic failure behavior.
- Persistence is optional, validated on startup, and safely written when enabled.
- Sticky sessions are an integrated opt-in policy rather than an unused package.
- Readiness, shutdown, metrics, and operator documentation are present.
- Simulator traffic can use the same public runtime contract without exposing internal routing decisions.
- Focused, race-enabled, and full repository verification passes.