# load-balancer

A production-shaped load balancer written in Go: registry-backed pools,
health checking with hysteresis, round-robin / weighted / least-connections
strategies, graceful draining, and a live control-plane API. It runs
standalone as a real service and doubles as the engine behind an interactive
simulator dashboard.

## Quick start: run the service with a local backend

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

The registry starts empty (503 until configured) and everything above is
in-memory: restart the process for a clean slate. The control plane binds
loopback by default; `-expose-control` is required to bind elsewhere and
should only be used on a trusted network (see DEPLOYMENT.md).

## Simulator dashboard

```sh
go run ./cmd/simulator -port 8090
# open http://localhost:8090
```

The dashboard displays and alters environment conditions (backends, weights,
latency, failure rates, strategy) and shows how the load balancer reacts.
Simulated backends bind to loopback by default; `--backend-bind` /
`--backend-advertise` configure reachability when a load balancer runs as a
separate container.

## Containers

### Build Image:
`docker build -t lb:dev .`

### Run Image:
`docker run -p 9000:8000 lb:dev` (exposes the data plane; control plane stays loopback inside the container)

### Image Size Comparison

| Image Type |Size (MB) |
| :---: | :---: |
| One-Stage Build (lb:naive) | 98MB |
| Multi-Stage Build (lb:dev) | 15MB |

### Notes about current Dockerfile
- Currently not pinned to an exact golang digest, need to pin exactly in the future for real builds

## Development

```sh
make test    # go test ./... -count=1
make race    # go test -race ./... -count=1
```
