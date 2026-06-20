.PHONY: help setup build test test-race test-integration fmt lint vet tidy

.DEFAULT_GOAL := help

# The taboo library module (github.com/josecabralf/taboo/pkg). yaml.v3-only; it
# carries no CLI/UI dependencies. The //go:embed sdk tree ships from
# internal/workshop, so build/test here travel with the embed.

help: ## List available targets
	@grep -E '^[a-zA-Z_-]+:.*##' Makefile | awk -F ':.*## ' '{printf "  \033[1;36m%-16s\033[0m %s\n", $$1, $$2}'

setup: ## Download module dependencies
	go mod download

build: ## Build all packages
	go build ./...

# Unit tests only. The integration suite is gated behind the `integration` build
# tag (see test-integration) and is deliberately excluded here so it never runs
# inside the dev workshop — a workshop within a workshop is problematic.
test: ## Run unit tests
	go test ./... -count=1 -cover

test-race: ## Run unit tests under the race detector
	CGO_ENABLED=1 go test -race ./... -count=1

# Host-only: exercises the real `workshop` CLI and LXD. Not wired into the dev
# workshop or CI; run it directly on a machine with workshop + LXD installed.
test-integration: ## Run integration tests (requires workshop + LXD)
	go test -tags integration ./... -count=1 -v

fmt: ## Format code with golangci-lint
	golangci-lint fmt

lint: ## Run golangci-lint
	golangci-lint run ./...

vet: ## Run go vet
	go vet ./...

tidy: ## Tidy Go module dependencies
	go mod tidy
