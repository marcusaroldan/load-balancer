# Deployment Guide

## 0. Current canonical topology: VM + Cloudflared subdomain

This section supersedes the Option A/B framing of sections 1-16 below, which
are kept as historical planning context. The service now runs as two
containers on the VM, joined by a private compose network; the Cloudflared
tunnel (deployed with the hermes/code-server stack, `network_mode: host`)
publishes exactly one URL:

```
browser -> cloudflare edge -> lb.marcusroldan.dev
            |
            v (tunnel ingress: http://127.0.0.1:8090)
+---------------------------+      +---------------------------+
| dashboard container       |      | lb container              |
| cmd/simulator             |----->| cmd/loadbalancer          |
| - web UI (same origin)    | HTTP |  control :8082 (net-only) |
| - simulated backends      |      |  data    :9000 (net-only) |
|   bind 0.0.0.0, advertise |      |                           |
|   "dashboard"             |<-----| health probes + proxy to  |
| publishes 127.0.0.1:8090  |      | http://dashboard:91xx     |
+---------------------------+      +---------------------------+
        lbdemo private bridge network (never published, never in the tunnel)
```

### Process wiring (B6: simulator drives the out-of-process LB)
dashboard container command:
```
simulator --port 8081 --idle-timeout 10m \
  --lb-control http://lb:8082 --lb-data http://lb:9000 \
  --backend-bind 0.0.0.0 --backend-advertise dashboard
```
lb container command:
```
loadbalancer -data :9000 -control :8082 -expose-control \
  -health-interval 2s
```
Scale-to-zero (S2): the dashboard watchdog enters the idle scenario after
`--idle-timeout` without any dashboard/API traffic -- an open browser tab
counts as traffic (it polls /api/state). Idle means: simulated listeners
closed, synthetic traffic stopped, and every backend deregistered from the
live LB (data plane then answers 503 with nothing to route). Starting any
scenario from the UI re-registers and resumes. `--idle-timeout 0` disables
the watchdog (local dev default).

### Ports
| Port | Where | Exposed to |
|---|---|---|
| 8090 -> container 8081 | dashboard/API | Cloudflared only (loopback publish) |
| lb data plane 9000 | compose network | visitors' simulated traffic (via dashboard), not published |
| lb control 8082 | compose network | dashboard container; `-expose-control` required to bind non-loopback |

### Configuration and persistence
- Desired state enters only through the control-plane API (validated, atomic,
  live). Flags/env configure the process: `-data`, `-control`,
  `-expose-control`, `-health-interval`, `-drain-timeout`,
  `-shutdown-deadline`.
- The demo runs EPHEMERAL (no config file): every lb restart begins with an
  empty registry, which is also the scale-to-zero guarantee; the dashboard
  re-registers backends when a scenario starts. File-backed config arrives
  with PROD-006; until then there is nothing to back up.

### Health behavior
- Per-pool HTTP probes (path, interval, timeout, thresholds, startup grace)
  from the canonical config; flapping uses consecutive-success/failure
  hysteresis; unhealthy and draining backends leave rotation immediately.

### Shutdown
- SIGTERM/SIGINT: health sweeps stop, the control plane stops accepting,
  then the data plane drains in-flight proxied requests up to
  `-shutdown-deadline` (default 30s). `docker stop` sends SIGTERM, so compose
  gets the same behavior; keep `stop_grace_period` >= the deadline.

### Security boundary (PROD-009)
- Control plane defaults to loopback and refuses non-loopback binds without
  `-expose-control`; the lb port is never published to the host and the
  Cloudflared ingress list contains only the dashboard.
- Simulated backends bind per operator flags; the browser can never steer a
  bind address or port (S1).
- No CORS headers are emitted by the load balancer planes; the dashboard UI
  is same-origin with its API through the subdomain.
- `/metrics` (JSON or Prometheus via `Accept: text/plain`) exposes aggregates
  only - no backend identities, routes, or session maps.
- Every control-plane mutation is audit-logged (method, path, status,
  request id, source, duration); `X-Request-ID` is generated or preserved
  per request and forwarded to backends for correlation.

### Tunnel ingress (cloudflared config, VM)
```yaml
ingress:
  - hostname: lb.marcusroldan.dev
    service: http://127.0.0.1:8090
  # existing hermes / code-server routes unchanged
  - service: http://127.0.0.1:8080
```

---

# Historical: Load Balancer Simulator Deployment Guide (Option A)

This document is the single source of truth for deploying the simulator backend and related infrastructure for the static GitHub Pages frontend. It is written to be consumed by an implementation agent in a separate portfolio repository that does not have access to this project directory.

## 1. Purpose and architecture summary

The project is split into two deployment surfaces:

1. Frontend: static simulator UI hosted via GitHub Pages
2. Backend: lightweight Go HTTP service that hosts the simulator state, scenario controls, and API endpoints for the UI

The recommended deployment is:

- GitHub Pages hosts the static HTML/CSS/JS frontend
- a small, always-on VPS hosts the Go backend
- an API gateway in the main portfolio infrastructure proxies traffic to that VPS
- the frontend uses an API base URL configured explicitly so it can call the gateway instead of assuming same-origin requests

This architecture keeps the static site cheap and simple while preserving a real backend with live mutable state, scenario controls, and interactive simulator behavior.

## 2. Deployment option selected: Option A

Option A is:

- GitHub Pages for the frontend
- a minimalist VPS for the simulator backend
- API Gateway route to the VPS for `/api/*` and simulator-specific endpoints
- no database
- no object storage
- no Lambda
- no container orchestrator
- in-memory state only

This is the correct choice because the simulator is intentionally stateful, interactive, and meant to operate as a lightweight control-plane and state-holding service. A serverless function model would not fit well because the simulator requires a long-running process with mutable state and continuous polling behavior.

## 3. What the backend actually is

The backend is not the load balancer itself. It is a simulator harness that models a live environment and exposes the state the frontend needs. This is by design.

The Go service in `cmd/simulator` does the following:

- starts a harness with a scenario preset (`stable`, `high_latency`, `partial_failure`, `partition`, `weighted`)
- creates multiple simulated backend HTTP servers in memory
- exposes a control API that allows the frontend to:
  - change scenario
  - change strategy
  - change request rate
  - add/remove clients
  - add/remove pools
  - add/remove/modify backends
- exposes `/api/state` for the dashboard to poll and re-render
- intentionally exposes only environment state and backend state, not routing internals

This makes it suitable for demonstration while preserving the principle that the actual load balancer routing logic remains hidden from the UI.

## 4. Runtime model and state behavior

### 4.1 In-memory only

The simulator is intentionally designed for in-memory operation. It keeps live state in memory within the process and does not persist to disk or to a database. This is intentional for the demo and is acceptable because the expected traffic is low and the simulator is not a production service.

### 4.2 State reset semantics

A restart resets the simulator state. The process is not durable. This must be explicit in operations and documentation.

### 4.3 Low-traffic assumption

This simulator is intended to support a small number of browser users and low request volume. It is not designed for scale, concurrency-heavy automation, or a production-grade multi-instance deployment.

### 4.4 Single-process assumptions

The backend is assumed to run as a single instance behind a gateway or proxy. There is no clustering, leader election, or replicated state. This is acceptable because the tool is a demo and a single-process service is enough for expected use.

## 5. Current backend API contract

The following contract is the API contract that the frontend expects. The implementation agent must use this as the source of truth when creating routing or infrastructure support.

### 5.1 Base URL and origin handling

The static site served from GitHub Pages does not automatically share the same origin as the simulator backend. Therefore, the frontend must be configured to call a specific API origin, rather than assuming the browser is on the same origin as the backend.

Required deployment behavior:

- GitHub Pages static frontend is served from a public static domain such as `app.example.com`
- simulator API is available on a separate domain such as `api.example.com` or through the portfolio API gateway under a path like `/loadbalancer/*`
- the frontend must be configured to use a runtime API base URL such as:
  - `https://api.example.com`
  - or `https://gateway.example.com/loadbalancer`
- if a gateway is used, the frontend should use the gateway route prefix, not an internal VPS port

### 5.2 CORS policy

Because the frontend is static and served from GitHub Pages, the backend must allow CORS from the GitHub Pages origin and any permitted production frontend domains.

Required CORS configuration:

- `Access-Control-Allow-Origin: https://your-github-pages-domain`
- `Access-Control-Allow-Methods: GET, POST, OPTIONS`
- `Access-Control-Allow-Headers: Content-Type`

This should be implemented by the API gateway and/or backend if necessary.

### 5.3 Endpoints

All endpoints below are required for the current frontend implementation. An implementation agent must treat them as mandatory unless the frontend is intentionally updated separately.

#### GET /api/state

Returns the current simulation state.

Response body:

```json
{
  "name": "stable",
  "strategy": "round_robin",
  "clients": 3,
  "request_rate": 30,
  "pools": ["default"],
  "backends": [
    {
      "id": "backend-1",
      "name": "",
      "url": "http://127.0.0.1:18081",
      "weight": 1,
      "latency": 20000000,
      "healthy": true,
      "failing": false,
      "failure_rate": 0,
      "pool": "default",
      "active_connections": 0,
      "load_percent": 25
    }
  ]
}
```

Notes:
- `latency` is serialized as Go `time.Duration` nanoseconds in the current implementation
- the frontend converts it to human readable values like `20ms`
- this is part of the visible UI contract; do not change without updating the frontend

#### POST /api/reset

Body:

```json
{ "value": "stable" }
```

Possible values:
- `stable`
- `high_latency`
- `partial_failure`
- `partition`
- `weighted`

Behavior:
- resets the simulator state for the named preset scenario
- returns `200 OK` when successful
- returns `400` if scenario is invalid

#### POST /api/strategy

Body:

```json
{ "value": "round_robin" }
```

Supported values:
- `round_robin`
- `weighted_round_robin`
- `least_connections`

#### POST /api/request-rate

Body:

```json
{ "value": "30" }
```

The value must be an integer represented as a string and parsed by the backend.

#### POST /api/client/add

No request body required.

Adds a simulated client.

#### POST /api/client/remove

No request body required.

Removes a simulated client.

#### POST /api/pool/add

Body:

```json
{ "value": "prod-us-east" }
```

Creates a named pool.

#### POST /api/backend/add

Body:

```json
{
  "id": "backend-123",
  "address": "127.0.0.1",
  "port": 0,
  "weight": 1,
  "latency": "50ms",
  "healthy": true,
  "pool": "default"
}
```

Behavior:
- creates a new backend entry with a generated backend URL/reference
- `id` is required
- `latency` is a Go duration string such as `50ms`, `250ms`, `1s`
- `pool` is optional; defaults to `default`

#### POST /api/backend/remove

Body:

```json
{ "id": "backend-123" }
```

Removes the backend by ID.

#### POST /api/backend/name

Body:

```json
{ "id": "backend-123", "name": "payments-api" }
```

Updates the backend name.

#### POST /api/backend/health

Body:

```json
{ "id": "backend-123", "healthy": true }
```

Marks the backend healthy/unhealthy.

#### POST /api/backend/latency

Body:

```json
{ "id": "backend-123", "latency": "120ms" }
```

Updates backend latency.

#### POST /api/backend/weight

Body:

```json
{ "id": "backend-123", "weight": 3 }
```

Updates backend weight.

#### POST /api/backend/pool

Body:

```json
{ "id": "backend-123", "pool": "default" }
```

Moves the backend to a pool.

#### POST /api/backend/failure-rate

Body:

```json
{ "id": "backend-123", "rate": 0.4 }
```

Sets the backend failure rate.

## 6. Current backend packaging and runtime requirements

### 6.1 Build artifact

The backend is a single Go binary built from:

- `./cmd/simulator`

The binary is expected to run on Linux.

### 6.2 Build command

Use a static or nearly-static Linux binary for the VPS deployment:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags "-s -w" \
  -o dist/loadbalancer-simulator ./cmd/simulator
```

### 6.3 Launch command

The service is started like this:

```bash
./loadbalancer-simulator --host 0.0.0.0 --port 8081 --static /opt/loadbalancer-simulator/web
```

This binary serves both:
- the API endpoints
- the static frontend files if static assets are present

### 6.4 Static asset directory

The simulator binary serves `web/` assets directly from the static directory. In the local repo this is `./web`.

For production deployment, static files should be copied to a directory such as:

```text
/opt/loadbalancer-simulator/web
```

This is optional if the static site is hosted separately, but it is the easiest way to support a full self-contained binary deployment if needed.

## 7. Recommended production structure on the VPS

The VPS should run a single service under a dedicated directory and a managed process manager.

Recommended layout:

```text
/opt/loadbalancer-simulator/
  bin/
    loadbalancer-simulator
  web/
    index.html
    app.js
    styles.css
    theme-engine.js
    inputs.css
  logs/
    simulator.log
  config/
    .env.example
```

This keeps the application simple and deployable with a single binary and a static asset folder.

## 8. Process manager and service configuration

A `systemd` unit is the correct lightweight deployment mechanism for this service.

### 8.1 Service file

Example unit:

```ini
[Unit]
Description=Load Balancer Simulator Backend
After=network.target

[Service]
Type=simple
User=loadbalancer
WorkingDirectory=/opt/loadbalancer-simulator
ExecStart=/opt/loadbalancer-simulator/bin/loadbalancer-simulator --host 0.0.0.0 --port 8081 --static /opt/loadbalancer-simulator/web
Restart=on-failure
RestartSec=5
Environment=PORT=8081
StandardOutput=append:/opt/loadbalancer-simulator/logs/simulator.log
StandardError=append:/opt/loadbalancer-simulator/logs/simulator.log

[Install]
WantedBy=multi-user.target
```

### 8.2 Service assumptions

- a dedicated Linux user named `loadbalancer` should run the service
- the service should bind to `0.0.0.0:8081`
- no root privileges should be required for the app itself
- the service should be restarted automatically on failure

### 8.3 Health checking

The backend exposes a status endpoint via its JSON APIs, but there is no health check endpoint explicitly defined in the current implementation.

For infrastructure health checks, use a lightweight API check such as:

- `GET /api/state`

It should return `200 OK` with valid JSON when the service is healthy.

This is the endpoint that should be used for any uptime probe or gateway health-check target.

## 9. Reverse proxy and gateway behavior

The frontend is static and hosted by GitHub Pages, but the API must be reachable from the browser. The backend is not intended to be publicly open directly on a wide public port. Instead, use a reverse proxy or API gateway layer.

### 9.1 Recommended routing strategy

Use an API gateway or proxy route that forwards requests to the VPS application instance.

Recommended routing model:

- `https://app.example.com` → GitHub Pages site
- `https://api.example.com/loadbalancer` → API Gateway -> VPS service
- or `https://gateway.example.com/loadbalancer/*` -> upstream VPS

### 9.2 Upstream target

The upstream target should be the VPS service:

```text
http://<VPS_PRIVATE_OR_PUBLIC_IP>:8081
```

If the gateway is HTTP-based and uses host-level routing, then the gateway should forward requests to the VPS on 8081 with the `Host` header preserved or normalized to match the API route.

### 9.3 TLS termination

Preferred:
- terminate TLS at the API gateway or CDN layer
- forward HTTP to the VPS internally

This keeps the app simple and eliminates the need for TLS tooling on the VPS itself.

## 10. DNS and GitHub Pages plan

### 10.1 Static frontend

The GitHub Pages site is configured with a CNAME record such as:

```text
app.example.com
```

This points to GitHub Pages and serves the static UI.

### 10.2 Simulator backend API

The API should use a separate hostname, for example:

```text
api.example.com
```

or a path under the main portfolio domain, for example:

```text
https://www.example.com/loadbalancer
```

The API must be routed through the API gateway to the VPS backend.

## 11. Terraform requirements for the portfolio project

This section is specifically for the portfolio website project and must be treated as implementation requirements for a Terraform-driven deployment.

### 11.1 Goal

The portfolio repo should define infrastructure to support:

- the static site hosted by GitHub Pages
- an API gateway route branch for the simulator backend
- an upstream target pointing to the VPS hosting the Go simulator service
- a public DNS route or path-based route to the simulator backend

### 11.2 Terraform resources needed

The portfolio Terraform project must include the following logical resources:

1. API Gateway route for simulator traffic
   - route path such as `/loadbalancer` or `/api/loadbalancer`
   - route method: `ANY` or `GET|POST` depending on gateway configuration
2. Integration to upstream VPS
   - HTTP integration or proxy integration
   - target URL: `http://<VPS_PUBLIC_IP>:8081`
3. DNS record or host alias
   - `api.example.com` or route under portfolio domain
4. Optional CORS config
   - allow GitHub Pages domain origin
5. Optional health check configuration
   - monitor `GET /api/state`
6. Optional security policy
   - restrict origin or authentication if needed

### 11.3 Terraform variable names and inputs

The implementation agent should define Terraform variables following this convention:

```hcl
variable "loadbalancer_domain" {
  type = string
}

variable "loadbalancer_api_prefix" {
  type = string
  default = "/loadbalancer"
}

variable "loadbalancer_vps_ip" {
  type = string
}

variable "loadbalancer_vps_port" {
  type = number
  default = 8081
}

variable "github_pages_origin" {
  type = string
}
```

The implementation should create:
- a route mapping from the gateway to the VPS
- a DNS record or API gateway custom domain entry
- CORS allowlist for the GitHub Pages origin

### 11.4 API gateway integration requirements

The gateway must support a proxied HTTP integration to the VPS. The exact integration format depends on the chosen gateway provider, but the semantics must be:

- incoming request on simulator path is forwarded to the upstream VPS
- response headers are proxied back to the client
- the simulator can receive JSON POST bodies as provided by the frontend
- gateway should allow a reasonable timeout for the simulator requests (since these are small and quick)

### 11.5 Branch strategy and repo ownership

The portfolio site repo will include a branch that manages the simulator backend routing and API gateway configuration. The load balancer repo itself remains the source of the app logic. The route config in the portfolio repo should be generated from this document and not depend on the load-balancer repo files being available.

## 12. Networking and security expectations

### 12.1 Public exposure

The simulator backend should not be exposed directly to arbitrary internet users without a proper gateway or proxy layer. The application can be safely exposed on its VPS port only if restrictively configured or if the gateway is the only entry path.

### 12.2 Basic hardening

Recommended baseline:
- firewall allows only 22/tcp and 8081/tcp from gateway or trusted IP ranges
- no public SSH access from the world
- OS patching handled by the VPS provider
- service runs under non-root user
- logs are stored under `/opt/loadbalancer-simulator/logs`

### 12.3 CORS and origin control

Since the frontend runs on GitHub Pages, allow only the known production origin(s). Do not use a wildcard `*` in production unless intentionally acceptable.

## 13. Operational behavior and assumptions

### 13.1 Simulated traffic

The backend simulates load and health based on an internal harness. It does not require external infrastructure or a database. It is a lightweight runtime model.

### 13.2 Polling cadence

The frontend polls `/api/state` on a rapid interval. This is okay for low traffic workloads when the backend is small and CPU is modest.

### 13.3 Memory usage

Memory use is low. The service stores a few pools and a few backends in memory and does not require large resources.

### 13.4 Expected cost profile

For low traffic and one active service, the expected cost is minimal:

- a small VPS: approximately $5–$15/month depending on region and provider
- optional managed gateway costs: low to moderate depending on usage
- zero database cost
- zero Lambda cost

This is a good fit for a demo or portfolio project that prioritizes minimal cost and low operational overhead.

## 14. Deployable artifact checklist

Before the service is considered deployable, the following must be true:

- the service builds as a Linux binary
- backend is started as a single process on port 8081
- static frontend is served from GitHub Pages or static hosting
- API gateway forwards simulator traffic to the VPS
- `/api/state` is reachable from the frontend and returns valid JSON
- scenario-changing endpoints return `200 OK`
- CORS is configured for the static frontend origin
- backend resets on process restart because the state is in memory only
- logs are written to a persistent log location

## 15. Acceptance criteria for the infrastructure agent

The implementation is accepted when all of the following are true:

1. A GitHub Pages frontend is live and reachable via static hosting
2. A simulator backend is live on a VPS
3. The API gateway routes simulator traffic to the VPS
4. The frontend uses a configured `API_BASE_URL` rather than assuming same-origin requests
5. CORS is configured for the frontend domain(s)
6. The `/api/state` endpoint returns valid JSON and is reachable from the browser
7. The stateful simulator remains interactive and supports scenario changes
8. The service is lightweight and compatible with a minimal-cost single-instance deployment

## 16. Final recommendation

This project should be deployed as:

- GitHub Pages static frontend
- a single tiny Linux VPS running the Go simulator backend
- a gateway or proxy route to the VPS
- no database, no serverless runtime, no orchestration layer

This is the most practical, cost-effective, and technically correct deployment model for the current architecture.

The portfolio repo must implement only the DNS and API Gateway route plumbing; the actual simulator binary and the runtime contract remain owned by this load balancer project and are documented here in full.
