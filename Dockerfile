# syntax=docker/dockerfile:1
FROM golang:1.25-alpine AS build
ENV GOTOOLCHAIN=local
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=secret,id=netrc,target=root/.netrc \
    --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/loadbalancer ./cmd/loadbalancer

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/loadbalancer /loadbalancer
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/loadbalancer"]