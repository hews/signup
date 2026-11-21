.DEFAULT_GOAL := help

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

help: ## Show available targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

run: ## Run locally on 127.0.0.1:8080 with ./signup.db
	go run ./cmd/signup

test: ## Format check, vet, tests
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo 'run gofmt'; exit 1; }
	go vet ./...
	go test -race -count=1 ./...

vulncheck: ## govulncheck against today's advisories, as the daily scan (vulnerabilities.yml) runs it
	go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

build: ## Static binary at ./signup
	CGO_ENABLED=0 go build -trimpath -ldflags='-s -w -X main.version=$(VERSION)' -o signup ./cmd/signup

.PHONY: help run test vulncheck build
