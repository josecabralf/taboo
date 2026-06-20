.PHONY: help setup build test test-race fmt lint vet tidy

.DEFAULT_GOAL := help

# The taboo CLI module (github.com/josecabralf/taboo/cli). It depends on the
# cobra/huh/charmbracelet stack and on the library via `replace ../pkg`; the thin
# main.go delegates to cli/internal/app.

help: ## List available targets
	@grep -E '^[a-zA-Z_-]+:.*##' Makefile | awk -F ':.*## ' '{printf "  \033[1;36m%-16s\033[0m %s\n", $$1, $$2}'

setup: ## Download module dependencies
	go mod download

build: ## Build all packages
	go build ./...

test: ## Run unit tests
	go test ./... -count=1 -cover

test-race: ## Run unit tests under the race detector
	CGO_ENABLED=1 go test -race ./... -count=1

fmt: ## Format code with golangci-lint
	golangci-lint fmt

lint: ## Run golangci-lint
	golangci-lint run ./...

vet: ## Run go vet
	go vet ./...

tidy: ## Tidy Go module dependencies
	go mod tidy
