## Implementation Plan — Load Balancer Project

This document is an enterprise-style, ticketized implementation plan you can pick tickets from and complete end-to-end. Tickets include a short description, acceptance criteria, estimates, dependencies, priority, and suggested implementation steps.

**How to use these tickets**
- Pick a ticket (eg. `LB-001`) and assign it to yourself. Work from top-to-bottom within an epic for minimal merge conflicts.
- Each ticket includes an acceptance checklist — mark tests and docs as done before closing.
- Estimate format: `hours` (dev + basic testing) and `priority` (P0 highest).

## Epics & Sprints
- Epic 1: Core Load Balancer (basic listener, routing, backend pools)
- Epic 2: Health, Sessions & Persistence (health checks, sticky sessions, config store)
- Epic 3: Management API & Config (control plane API, config manager)
- Epic 4: Sandbox / Simulator & Demo UI (frontend to simulate conditions)
- Epic 5: Ops: CI/CD, containerization, monitoring

Sprint plan (suggested):
- Sprint 1 (2 weeks): LB core, simple round-robin, minimal API to add backends (LB-001..LB-005)
- Sprint 2 (2 weeks): Health checks, persistence, sticky sessions (LB-010..LB-015)
- Sprint 3 (2 weeks): Advanced algorithms, active-active, simulator UI (LB-020..LB-030)

---

## Ticket List (pickable)

LB-001: Project bootstrap & repo skeleton
- Description: Create repository layout, Go module, README, CI skeleton, and a small Makefile.
- Acceptance Criteria:
	- `go.mod` initialized, basic `main.go` that prints version
	- `README.md` with quick start and architecture links to planning docs
	- CI stub (GitHub Actions) that runs `go build` and `go test` on push
	- `Makefile` targets: `build`, `run`, `test`, `lint`
- Estimate: 3 hours
- Dependencies: none
- Priority: P0
- Suggested Steps: init module, add .github/workflows/ci.yml, add README, simple unit test

LB-002: Listener and request forwarding (HTTP)
- Description: Implement a TCP/HTTP listener that accepts requests and forwards them to a selected backend.
- Acceptance Criteria:
	- Listener accepts HTTP requests on port configurable via env/flag
	- A simple request is proxied to a backend and response returned to client
	- Logging of incoming request and chosen backend
	- Unit/integration test using a local HTTP test server
- Estimate: 8 hours
- Dependencies: LB-001
- Priority: P0

LB-003: Backend Pool data model & in-memory store
- Description: Implement data structures for Backend and Pool and an in-memory registry to add/remove backends.
- Acceptance Criteria:
	- Data model follows schema in [load-balancer-system-design.md](load-balancer-system-design.md)
	- API to add/remove/list backends programmatically (no HTTP yet)
	- Unit tests for add/remove/list operations
- Estimate: 4 hours
- Dependencies: LB-001
- Priority: P0

LB-004: Round-robin routing algorithm
- Description: Implement round-robin selection that consumes only healthy backends.
- Acceptance Criteria:
	- Given a pool with N backends, requests are distributed approximately uniformly
	- Unhealthy backends are excluded
	- Unit tests validate rotation and exclusion
- Estimate: 6 hours
- Dependencies: LB-002, LB-003
- Priority: P0

LB-005: Basic Control Plane — POST /backend, GET /pool
- Description: Add HTTP control-plane endpoints to manage backends and pools.
- Acceptance Criteria:
	- `POST /backend` accepts address/port/pool and returns 201 with IDs
	- `GET /pool/{pool_id}` returns pool configuration and members
	- Input validation and sensible error responses
	- Integration tests exercising endpoints
- Estimate: 6 hours
- Dependencies: LB-001, LB-003
- Priority: P0

---

LB-010: Health Checker service
- Description: Implement periodic health checks for backends and an internal status update mechanism.
- Acceptance Criteria:
	- Configurable interval and path (defaults from design doc)
	- Backend state flips between healthy/unhealthy based on thresholds
	- Health state is visible to routing logic
	- Unit tests and a small integration test using a mock server
- Estimate: 8 hours
- Dependencies: LB-003
- Priority: P0

LB-011: Sticky sessions (in-memory, optional shared cache)
- Description: Implement sticky session support with TTL; initial implementation in LB node memory.
- Acceptance Criteria:
	- If sticky enabled, clients are routed to the mapped backend until TTL expires
	- Sticky TTL respects configuration
	- Tests for TTL expiration and mapping behavior
- Estimate: 6 hours
- Dependencies: LB-002, LB-003
- Priority: P1

LB-012: Persistent configuration store (file-backed + interface)
- Description: Add an abstraction for persistent config and a simple file-backed implementation.
- Acceptance Criteria:
	- Config is saved/loaded from disk (JSON/YAML)
	- Configurable path, graceful reload on start
	- Unit tests for save/load
- Estimate: 4 hours
- Dependencies: LB-001
- Priority: P1

LB-013: Weighted and least-connections algorithms
- Description: Implement weighted-round-robin and least-connections routing.
- Acceptance Criteria:
	- Algorithms selectable per-pool via config
	- Unit tests showing expected distribution/behavior
- Estimate: 8 hours
- Dependencies: LB-003, LB-004
- Priority: P1

LB-020: Sandbox / simulator backend harness
- Description: Build a small harness that can start multiple fake backend servers with adjustable latency, error rates, and capacity to simulate conditions.
- Acceptance Criteria:
	- CLI to spawn N backends with configurable latency/error
	- Endpoint to toggle server failure or partition
	- Demo script to reproduce common scenarios (high load, failure)
- Estimate: 10 hours
- Dependencies: LB-001, LB-002, LB-010
- Priority: P1

LB-021: Demo frontend for portfolio (simple UI)
- Description: Small web UI to visualize pools, health, and to trigger simulator events.
- Acceptance Criteria:
	- SPA or simple static page that talks to control-plane endpoints
	- Visual indicators for backend health and request distribution
	- Deployable as static assets on a personal site
- Estimate: 12 hours
- Dependencies: LB-020, LB-005
- Priority: P2

LB-030: Observability & metrics (Prometheus compatible)
- Description: Expose metrics: requests, backend latency, backend health, active connections.
- Acceptance Criteria:
	- `/metrics` endpoint with Prometheus-format metrics
	- Basic Grafana dashboard JSON (optional)
	- Tests or smoke checks to validate metrics output
- Estimate: 6 hours
- Dependencies: LB-002
- Priority: P1

LB-040: CI/CD and containerization
- Description: Add Dockerfile(s), GitHub Actions to build images, and a simple deployment doc for the sandbox.
- Acceptance Criteria:
	- Dockerfile builds a minimal image
	- `ci` pipeline builds and pushes image (or builds only for PRs)
	- `deploy/` notes for running locally and on portfolio host
- Estimate: 6 hours
- Dependencies: LB-001
- Priority: P1

---

## Ticket Template (copy into PR or issue)
- Title: `LB-XXX: Short description`
- Description: One-paragraph goal, why it matters, links to design docs
- Acceptance Criteria:
	- [ ] Criterion 1 (testable)
	- [ ] Criterion 2 (docs/README updated)
	- [ ] Criterion 3 (unit/integration tests)
- Implementation Notes: bullet list of steps
- Estimate: `hours`
- Dependencies: list of ticket IDs or components

## How to take a ticket (developer-friendly checklist)
- Assign the ticket to yourself and comment with plan and ETA.
- Create a feature branch: `feature/LB-XXX-short-desc`.
- Implement small incremental commits; keep PRs focused (<= 300 LOC ideal).
- Add unit tests for logic and a minimal integration test where appropriate.
- Update `README.md` and the planning docs link when behavior changes.
- Run `go vet`/`golangci-lint` and ensure CI passes before merging.

## Acceptance & QA guidance
- Unit tests must be added for all algorithmic code (routing, health logic).
- Add integration tests that spin up the LB and a mock backend (use `httptest` package).
- Manual QA for sandbox and demo UI: run scenario scripts and verify metrics/behavior.

## Next steps (automatable)
- Create initial repo with LB-001 and LB-002 done as an MVP.
- Add skeleton for control-plane and config persistence.

---

File references: see planning docs for goals and design:
- [project-goals.md](project-goals.md#L1)
- [load-balancer-system-design.md](load-balancer-system-design.md#L1)
