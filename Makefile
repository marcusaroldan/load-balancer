.PHONY: build run test lint race docker-lb docker-dashboard compose-up compose-down compose-ps

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

docker-lb:
	docker build --target loadbalancer -t lbdemo-lb .

docker-dashboard:
	docker build --target simulator -t lbdemo-dashboard .

compose-up:
	LB_REPO_DIR=. docker compose -f deploy/docker-compose.yml up -d --build

compose-down:
	docker compose -f deploy/docker-compose.yml down

compose-ps:
	docker compose -f deploy/docker-compose.yml ps
