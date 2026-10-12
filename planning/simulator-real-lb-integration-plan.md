# Simulator-to-Load-Balancer Integration Plan

## Goal

Bridge the current gap between the simulator and the real load balancer so that the simulator becomes a true environment harness for the actual LB implementation.

The current simulator in [internal/simulator/simulator.go](../internal/simulator/simulator.go) creates fake backend listeners and exposes environment-only state. It does not start or route traffic through the real LB logic in [cmd/loadbalancer/main.go](../cmd/loadbalancer/main.go), [internal/proxy/proxy.go](../internal/proxy/proxy.go), or [internal/routing/strategy.go](../internal/routing/strategy.go).

The target state is:
- the simulator defines a scenario and a set of simulated backend conditions
- the real load balancer receives the traffic and performs actual routing
- the simulator observes backend health, latency, failures, and response outcomes from the real LB behavior
- the UI remains a dashboard for scenarios, not a direct source of LB routing truth

---

## Root cause

The simulator currently behaves as a synthetic model of the environment rather than a test harness around the real LB. It:
- starts backend listeners directly
- simulates health/failure/latency at the backend layer
- changes scenario metadata without ever instantiating the LB runtime
- never calls the selector or proxy logic used by the real implementation

This means the project currently demonstrates a fake environment, not the actual LB under test.

---

## Design principle

The simulator must follow this boundary:

1. The real LB owns routing decisions.
2. The simulator owns network conditions and scenario configuration.
3. The simulator can mutate the LB only through public, external configuration controls, not by bypassing the LB or reading private routing internals.
4. The UI must not expose internal routing state, only scenario state and externally observable outcomes.

This matches the contract already described in [planning/simulator-design-contract.md](simulator-design-contract.md).

---

## Target architecture

### Runtime layout

- `cmd/simulator` becomes the orchestration entrypoint for the scenario dashboard.
- `cmd/loadbalancer` remains the real LB runtime used by the simulator.
- The simulator process starts a real LB instance on a loopback address and a configured port.
- The simulator creates simulated backends on separate loopback ports to emulate network conditions.
- The LB is configured with pools and backend endpoints that point to the simulated backends.
- Traffic generation uses the real LB endpoint as the target instead of talking to the backends directly.

### Flow

1. The simulator loads a preset scenario.
2. It creates simulated backend listeners with health, latency, and failure configuration.
3. It builds a pool definition and registers the backends with the real LB.
4. It starts the real LB with the configured strategy.
5. The simulator sends synthetic client traffic to the LB.
6. The simulator observes:
   - backend health / availability
   - backend latency and failure behavior
   - request success / error rates
   - active connection / load counts
   - actual routing outcomes exposed by the LB through public metrics or public trace data
7. The dashboard renders only scenario state and externally observable outcomes.

---

## Integration contract

### Public simulator APIs

The simulator continues to expose dashboard-friendly APIs like:
- `GET /api/state`
- `POST /api/reset`
- `POST /api/strategy`
- `POST /api/backend/latency`
- `POST /api/backend/health`
- `POST /api/backend/weight`
- `POST /api/backend/failure-rate`

These remain environment-focused and are allowed to change scenario configuration.

### Required real-LB integration APIs

The real LB must provide a minimal public control plane or config interface for the simulator to use:
- create pool
- add backend to pool
- update backend health/latency metadata
- set strategy for a pool or default strategy
- list backends / health status
- query metrics or summary stats

If the real LB has no public API yet, the simulator must use a small adapter layer in Go that calls the LB's internal config and selector code directly through a defined interface. This is the crucial bridge: the simulator must not bypass the LB code path.

### Forbidden behavior

The simulator must not:
- read private selector state from the LB internals without a public API
- infer the chosen backend from implementation details instead of public endpoints
- call fake functions that duplicate LB logic
- claim to be testing the real LB while bypassing the actual routing engine

---

## Detailed implementation plan

### Ticket SIM-001: Define the simulator / LB contract

**Goal**
Establish the API and behavior contract between the simulator and the real LB.

**Scope**
- document the public config/control APIs the simulator may use
- define what the simulator is allowed to mutate
- define what it can observe without exposing private LB internals
- define a minimal data schema for scenario config and backend state

**Acceptance criteria**
- a written contract exists for simulator-to-LB interaction
- the allowed controls are limited to strategy, pool membership, backend health, weight, and latency
- no private LB routing state is exposed to the UI

**Implementation steps**
1. Review routing, pool, and control-plane APIs in [internal/routing/strategy.go](../internal/routing/strategy.go), [internal/pool/pool.go](../internal/pool/pool.go), and [internal/controlplane/handler.go](../internal/controlplane/handler.go).
2. Define a minimal public adapter interface for the simulator.
3. Add comments and unit tests around the contract.

**Estimate**: 3-4 hours

---

### Ticket SIM-002: Create a real LB runtime adapter

**Goal**
Create a reusable Go adapter that starts and configures the real LB in-process for the simulator.

**Scope**
- instantiate real LB components instead of a fake abstraction
- expose methods for:
  - initialize pool
  - add backend
  - set strategy
  - list backends
  - get public health and summary information

**Acceptance criteria**
- a simulator can instantiate the real LB via a runner or constructor
- the same router/selector logic used in production is called
- the bridge is isolated in a dedicated package or adapter layer

**Implementation steps**
1. Add a `internal/simulator/lbbridge.go` or similar package.
2. Build a real-LB runner that creates a pool store and selector.
3. Use the existing pool and routing implementations instead of custom simulator-only selection logic.
4. Ensure the bridge is operationally separate from UI rendering.

**Estimate**: 6-8 hours

---

### Ticket SIM-003: Convert scenario settings into real LB configuration

**Status**: Complete

**Goal**
Map a simulation scenario into actual LB config instead of fake backend-only state.

**Scope**
- scenario name, strategy, backend list, weights, health, pool membership
- load balancer config translation from scenario to real LB state
- pool creation and backend registry population

**Acceptance criteria**
- each preset scenario creates actual LB pool configuration
- strategy values match the real routing algorithms in [internal/routing/strategy.go](../internal/routing/strategy.go)
- backends are added to the real LB through the same public control path the LB would use in production

**Implementation steps**
1. Add and validate the `mapScenarioToLoadBalancer` mapper.
2. Convert simulator backend definitions into real `pool.Backend` entries.
3. Apply normalized defaults, strategy, weights, health, and pool membership.
4. Add tests covering stable, weighted, least-connections, invalid, and atomic-rejection scenarios.

**Implemented in**
- `internal/simulator/real_lb_adapter.go`
- `internal/simulator/real_lb_adapter_test.go`

**Verification**
- `go test ./internal/simulator -run 'TestMapScenarioToLoadBalancer|TestRealLoadBalancerAdapter' -count=1`
- `go test ./... -count=1`

**Estimate**: 4-6 hours

---

### Ticket SIM-004: Send traffic through the real LB

**Status**: Complete

**Goal**
Ensure all synthetic traffic is sent to the LB endpoint, not directly to the backend listeners.

**Scope**
- message generation via client requests against the LB listener
- request rate behavior and burst logic
- preserving scenario timing and backend health semantics without bypassing the LB

**Acceptance criteria**
- all requests are sent through the real LB path
- the LB selects backends using its real routing logic
- backend simulation continues to affect only environment conditions, not routing code

**Implementation steps**
1. Attach a real `LoadBalancerAdapter` to the simulator harness lifecycle.
2. Configure the adapter from live backend listener URLs and synchronize strategy, health, latency, failure rate, weight, pool, add, and remove changes.
3. Replace direct backend requests in the traffic loop with requests to the real LB endpoint.
4. Add an end-to-end test proving generated traffic increments the real LB request and success counters.

**Implemented in**
- `internal/simulator/lb_contract.go`
- `internal/simulator/real_lb_adapter.go`
- `internal/simulator/simulator.go`
- `internal/simulator/lb_contract_test.go`
- `internal/simulator/simulator_test.go`

**Verification**
- `go test ./internal/simulator -run 'TestHarnessGeneratesTrafficThroughRealLoadBalancer|TestLoadBalancerAdapterContract|TestHarness' -count=1`
- `go test ./... -count=1`

**Estimate**: 6-8 hours

---

### Ticket SIM-005: Add public observability endpoints for LB behavior

**Status**: Complete

**Goal**
Expose only the data needed to understand runtime behavior without leaking internals.

**Scope**
- real LB summary endpoint or metrics endpoint
- backend health summary
- routing outcome counters or recent selections via a public API
- optional summary telemetry that does not expose raw internal state

**Acceptance criteria**
- the dashboard can read actual LB workload and outcome statistics
- the data is public and stable
- the UI does not reveal private routing internals or full selection traces

**Implementation steps**
1. Add a public summary endpoint on the real LB.
2. Include backend status, active connections, total requests, request success/failure rates, and strategy.
3. Expose metrics or summary counters in a minimal format suitable for the simulator dashboard.
4. Keep implementation intentionally limited to the public contract.

**Implemented in (satisfied by PROD-007/PROD-009 + SIM-006 remote-adapter work, no separate code)**
- `internal/controlplane/api.go` — GET /healthz, /readyz, /metrics on the live registry
- `internal/controlplane/prometheus.go` — zero-label aggregate metrics (Prometheus via Accept: text/plain, JSON by default)
- `internal/simulator/remote_lb_adapter.go` — fetches /metrics, deltas vs baseline → snapshot
- `internal/simulator/lb_contract.go` — LoadBalancerSnapshot (deliberately omits routing internals/traces)
- `web/app.js`, `web/index.html` — outcomes panel rendering the aggregates

**Verification (2026-10-12)**
- Live: `GET https://lb.marcusroldan.dev/api/state` returns public `load_balancer` aggregates (requests, success/failure, avg latency, healthy/total backends, strategy, observed_at) with no routing internals or per-request traces.
- Tests: `TestRemoteAdapterSnapshotDeltasAndCache`, `TestRemoteAdapterRequiresBothURLs` (`go test ./...` green incl. -race).

**Estimate**: 4-6 hours

---

### Ticket SIM-006: Update the UI to render real LB outcomes, not fake-only environment state

**Status**: Complete

**Goal**
Update the dashboard to reflect real LB behavior while preserving the current scenario controls.

**Scope**
- render scenario status, LB strategy, backend health, and response outcomes
- show whether the LB is routing traffic successfully under the current environment
- maintain the existing environment/conditions model as a scenario control surface

**Acceptance criteria**
- frontend reads from simulator state that includes real-LB outcomes
- UI labels and metrics clearly distinguish environment conditions from LB routing results
- no internal routing state is shown to the user

**Implementation steps**
1. Add a structured `load_balancer` response section to the simulator state payload.
2. Include aggregate request count, success count, failure count, average latency, pool count, backend count, healthy backend count, and observation timestamp.
3. Update the JS renderer in [web/app.js](../web/app.js) and add a dedicated outcomes panel in [web/index.html](../web/index.html).
4. Keep routing internals private: no selected backend, destination, route history, or per-request trace is exposed.

**Implemented in**
- `internal/simulator/simulator.go`
- `internal/simulator/simulator_test.go`
- `web/app.js`
- `web/index.html`
- `web/styles.css`

**Verification**
- `go test ./internal/simulator -run 'TestSimulationStateMatchesPureSimulatorContract|TestSimulationStateReportsAggregateLoadBalancerOutcomes' -count=1`
- `go test ./... -count=1`
- `npx @tailwindcss/cli -i ./web/inputs.css -o ./web/styles.css --minify`

**Estimate**: 5-7 hours

---

### Ticket SIM-007: End-to-end verification and regression tests

**Status**: Complete

**Goal**
Prove the simulator is driving the real LB, not faking the path.

**Scope**
- integration tests with local server startup
- scenario verification for stable, high latency, partial failure, partition, and weighted cases
- check the traffic path uses the LB

**Acceptance criteria**
- integration tests validate the real LB receives traffic under each scenario
- tests assert that request routing is performed via the LB code path
- dashboard smoke tests confirm serialized state is consistent

**Implementation steps**
1. Use the harness helper to start simulated backends and the real in-process LB together.
2. Add scenario-based integration tests for stable, high latency, partial failure, partition, and weighted presets.
3. Validate completed LB outcomes, strategy, pool count, backend count, and dashboard state serialization without routing internals.
4. Run focused simulator tests, `go test -race ./internal/simulator`, and `go test ./...`.

**Implemented in**
- `internal/simulator/simulator_test.go`
- `internal/simulator/real_lb_adapter.go`
- `internal/simulator/simulator.go`

**Verification**
- `go test ./internal/simulator -run 'TestPresetScenariosDriveTheRealLoadBalancer|TestDashboardStateHandlerSerializesRealLBOutcomes' -count=1`
- `go test -race ./internal/simulator -count=1`
- `go test ./... -count=1`

**Note**: `go test -race ./... -count=1` also detects an existing unrelated race in `internal/health.TestCheckerStartAndStopLifecycle`; the simulator package itself passes race-enabled verification.

**Estimate**: 6-8 hours

---

## Recommended execution order

1. SIM-001: define the public contract
2. SIM-002: build the real LB adapter
3. SIM-003: map scenario config into real LB config
4. SIM-004: route synthetic traffic through the LB
5. SIM-005: expose summary observability
6. SIM-006: update the UI
7. SIM-007: end-to-end validation

This order keeps the integration minimal and verifiable while preserving the simulation UI.

---

## Critical implementation notes

### 1. Do not duplicate the LB logic in the simulator
The simulator should not implement a second routing engine. It should use the same real selector and pool logic already found in [internal/routing/strategy.go](../internal/routing/strategy.go).

### 2. Keep the simulator env-only in the presentation layer
The simulator should still own environment conditions such as latency, health flips, and failure probability. That is valid. What it cannot do is replace or bypass the LB routing engine.

### 3. Use public config APIs, not private internals
If direct internal access is needed during early development, hide that behind a small adapter interface, and move toward public, stable APIs as quickly as possible.

### 4. Keep the UI honest
The UI should present two layers clearly:
- scenario/environment layer
- real LB behavior layer

It should never imply that simulated values are actual LB routing decisions if they are not.

---

## Definition of done

The simulator is complete only when:
- the real LB is started as part of the simulator runtime
- scenario changes are applied via the real LB configuration path
- traffic flows through the real LB
- the dashboard shows real LB outcomes and environment conditions separately
- the contract and validation tests prove the integration is real

At that point, the simulator becomes a genuine live demonstration of the real load balancer under varied network conditions.
