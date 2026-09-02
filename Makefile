.PHONY: build run test lint

build:
	go build ./...

run:
	go run ./cmd/loadbalancer

test:
	go test ./...

lint:
	gofmt -w .
	go vet ./...