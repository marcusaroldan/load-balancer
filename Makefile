.PHONY: build run test lint

build:
	go build ./...

run:
	go run ./cmd/loadbalancer

test:
	go test ./... -count=1

lint:
	gofmt -w .
	go vet ./...
race:
	go test -race ./... -count=1
