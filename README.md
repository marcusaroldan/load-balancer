# load-balancer

This project is a production-shaped load balancer written in Go. 
Features:
- registry-backed pools
- health checking with hysteresis
- round-robin / weighted / least-connections routing strategies
- graceful draining
- multi-pool host routing
- live control-plane API 
It can run standalone as a real service and also doubles as the engine behind an interactive [simulator dashboard](https://lb.marcusroldan.dev).

This is a pure standard library with zero third-party dependencies. Multi-stage build produces a ~15 MB distroless image that health-checks itself (there is no curl in scratch images).

## Quick start guide: Run the service with a local backend

Terminal 1 - a toy backend:

```sh
python3 -m http.server 9101
```

Terminal 2 - the load balancer (control plane on loopback, data plane open):

```sh
go run ./cmd/loadbalancer -data :9000 -control 127.0.0.1:8082
```

Terminal 3 - configure it live and send traffic through it:

```sh
curl -X POST localhost:8082/pool    -d '{"pool_id":"web"}'
curl -X POST localhost:8082/backend -d '{"backend_id":"b1","pool":"web","address":"127.0.0.1","port":9101}'
curl localhost:8082/readyz && curl -I localhost:9000   # 200 through the LB
curl localhost:8082/metrics                            # counters + health gauges
```

The registry starts empty (503 until it is configured) and everything above is in-memory: restart the process for a clean slate. The control plane binds loopback by default; `-expose-control` is required to bind elsewhere and should only be used on a trusted network.

## Multi-pool host routing

Each pool may claim a public hostname. The data plane resolves the pool per request from the `Host` header (port stripped, case-insensitive):

```sh
curl -X POST localhost:8082/pool -d '{"pool_id":"api","host":"api.example.com"}'
curl -X POST localhost:8082/pool -d '{"pool_id":"web","host":"www.example.com"}'
```

- A request whose hostname matches a pool's claim routes to that pool.
- With a single configured pool, everything routes to it (default rule).
- Among multiple pools, an unclaimed hostname is a 503 — the balancer never guesses.
- Duplicate host claims are rejected (`409`), invalid hostnames (`400`).

## Simulator dashboard

```sh
go run ./cmd/simulator -port 8090
# open up http://localhost:8090 to view
```

The dashboard displays and alters environment conditions (backends, weights, latency, failure rates, strategy) and shows how the load balancer reacts: synthetic clients send real HTTP requests through a real load balancer instance (in-process by default, or out-of-process over the control plane), and the outcomes panel renders the LB's own aggregates. Simulated backends bind to loopback by default; `--backend-bind` / `--backend-advertise` configure reachability when a load balancer runs as a separate container. The dashboard polls every 200ms and updates DOM nodes in place so there are no full repaints.

## Control plane API (v1)

| Endpoint | Purpose |
|---|---|
| `POST /pool` | create pool (`pool_id`, `host?`, `algorithm?`, `health_policy?`, `sticky?`) |
| `GET /pool` / `GET /pool/{id}` / `DELETE /pool/{id}` | inspect / drain + remove |
| `POST /backend` / `GET /backend/{id}` / `DELETE /backend/{id}` | manage members |
| `PUT /backend/{id}/weight` / `PUT /backend/{id}/enabled` | live tuning |
| `PUT /config/algorithm` | swap strategy + sticky settings atomically |
| `GET /config` | current desired-state document |
| `GET /healthz` / `GET /readyz` | liveness / readiness (ready = configured + serving) |
| `GET /metrics` | aggregates as JSON, or Prometheus exposition with `Accept: text/plain` |

## Containers

### Build images:
```sh
make docker-lb         # docker build --target loadbalancer -t lbdemo-lb .
make docker-dashboard  # docker build --target simulator -t lbdemo-dashboard .
```

### Run the demo project (real LB plus the dashboard:
```sh
make compose-up        # dashboard on http://127.0.0.1:8090, LB unpublished
```

`deploy/docker-compose.yml` runs both containers on a private bridge: the dashboard publishes `127.0.0.1:${DASHBOARD_PORT:-8090}` only, the LB publishes nothing (control plane reachable only inside the compose network), both run read-only rootfs with all capabilities dropped, and both health-check themselves with the bundled `/healthz` probe.

### Run the images standalone:
```sh
docker run --rm -p 127.0.0.1:9000:9000 -p 127.0.0.1:8082:8082 lbdemo-lb
docker run --rm -p 127.0.0.1:8090:8081 lbdemo-dashboard
```
The load balancer image defaults to `-data :9000 -control :8082`; the control plane is only reachable because you mapped it to loopback yourself (never publish :8082 through a tunnel or proxy).

### Image size

| Build | Size |
| :--- | :---: |
| One-stage build (lb:naive) | 98 MB |
| Multi-stage build (lb:dev) | 15 MB |

## Development

```sh
make test    # go test ./... -count=1
make race    # go test -race ./... -count=1
make lint    # gofmt + go vet
```

Dashboard styles are Tailwind v4 compiled to committed CSS:

```sh
npm run build:css   # web/inputs.css -> web/styles.css (minified, committed)
```

CI runs vet, gofmt, the full test suite with `-race`, builds both container targets, asserts the image-size budget, smoke-tests the scratch images, and validates the compose file.

## Security notes

- The control plane owns the LB. It binds loopback by default and refuses a non-loopback bind unless `-expose-control` is passed explicitly.
- The Prometheus `/metrics` endpoint is public by design (aggregates only, zero labels — no per-backend routing state can leak through it). If you expose the LB through a tunnel or proxy, either keep the data plane as the only published surface or scrape `/metrics` from inside the private network.
- Request bodies are size-capped; mutations are audit-logged on the control plane; every response carries an `X-Request-ID` propagated upstream.

## Future extensions (designed, not implemented)

These are deliberate next steps to extend the project:

- **Prometheus scrape + Grafana dashboard**: `/metrics` already is configured for future integration with Prometheus; a scrape config and an importable dashboard JSON are planned so operators get time-series history (rates, percentiles, alerting) on top of the current aggregates.
- **Sticky sessions**: per-pool cookie affinity with TTL exists in the config model (`sticky_sessions` with `ttl_seconds` validation) but is not wired into the routing engine yet.
- **Persistent configuration**: save/reload of the desired-state document across restarts is planned; today the runtime starts empty and is configured live through the control plane (or `Load()` in tests/harnesses).

## Project layout

```
cmd/loadbalancer/    standalone data+control plane binary
cmd/simulator/       dashboard server driving a real LB (in-process or remote)
internal/config/     canonical desired-state model + validation
internal/registry/   immutable snapshots over pools/backends/health
internal/routing/    selection strategies (RR / weighted / least-connections)
internal/proxy/      data-plane handler: selection, limits, forwarding, metrics
internal/health/     active probes with hysteresis + grace periods
internal/controlplane/  HTTP API v1 + audit + Prometheus negotiation
internal/runtime/    composition root: listeners, lifecycle, pool resolution
internal/healthprobe/  self-contained healthcheck for scratch images
deploy/              docker-compose.yml for the demo project
web/                 dashboard frontend (vanilla JS)
```

## License

MIT [LICENSE](LICENSE).
