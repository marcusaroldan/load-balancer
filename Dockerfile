# syntax=docker/dockerfile:1
#
# Two shippable images from one build stage:
#
#   docker build --target loadbalancer -t lbdemo-lb .
#   docker build --target simulator    -t lbdemo-dashboard .
#
# Runtime images are FROM scratch with the CA bundle copied in: both
# services speak plain HTTP today, but the bundle keeps future HTTPS
# backends (and health probes) working without shipping a shell, and each
# image stays far under the 25 MB budget (distroless static is ~20 MB
# before the binary; scratch is ~11 MB total). There is intentionally no
# curl in the image -- each binary carries -healthcheck for HEALTHCHECK.

FROM golang:1.25-alpine AS build
ENV GOTOOLCHAIN=local
WORKDIR /src

# Cache module downloads and build artifacts across builds (BuildKit).
# NOTE: mount targets must be absolute; a relative "root/.cache" silently
# caches nothing.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
      -o /out/loadbalancer ./cmd/loadbalancer && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
      -o /out/simulator ./cmd/simulator

# --- load balancer service --------------------------------------------------
FROM scratch AS loadbalancer
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/loadbalancer /loadbalancer
USER 65534:65534
EXPOSE 9000 8082
ENTRYPOINT ["/loadbalancer"]
# Image defaults mirror the VM service. A bare `docker run` with no port
# mapping is still unreachable externally; the control-plane bind here is
# safe on the private bridge, and compose overrides these args explicitly.
CMD ["-data", ":9000", "-control", ":8082", "-expose-control"]

# --- simulator + dashboard (web UI) -------------------------------------------
FROM scratch AS simulator
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/simulator /simulator
COPY web/ /web/
USER 65534:65534
EXPOSE 8081
ENTRYPOINT ["/simulator"]
# Standalone UI demo (in-process harness). The compose project overrides
# with --lb-control/--lb-data to drive the real service (B6).
CMD ["-port", "8081", "-static", "/web", "-backend-bind", "0.0.0.0"]
