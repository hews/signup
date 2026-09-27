.DEFAULT_GOAL := help

help: ## Show available targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

run: ## Run locally on 127.0.0.1:8080 with ./signup.db
	go run ./cmd/signup

test: ## Format check, vet, tests
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo 'run gofmt'; exit 1; }
	go vet ./...
	go test -race -count=1 ./...

build: ## Static binary at ./signup
	CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o signup ./cmd/signup

.PHONY: help run test build
