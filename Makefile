.PHONY: init run build test

init:
	@./scripts/init-local.sh

run: init
	go run ./cmd/server

build:
	CGO_ENABLED=0 go build -o bin/job-center ./cmd/server

test:
	go test ./...
