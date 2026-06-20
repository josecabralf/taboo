.PHONY: help setup build test fmt lint vet tidy

.DEFAULT_GOAL := help

# The afk demo orchestrator (module `afk`), the adopter-layout reference consumer
# of the taboo library via `replace ../../pkg`. It is not released; this Makefile
# exists so the nested module builds, vets, tests and lints standalone on every PR.

help: ## List available targets
	@grep -E '^[a-zA-Z_-]+:.*##' Makefile | awk -F ':.*## ' '{printf "  \033[1;36m%-16s\033[0m %s\n", $$1, $$2}'

setup: ## Download module dependencies
	go mod download

build: ## Build all packages
	go build ./...

test: ## Run unit tests
	go test ./... -count=1 -cover

fmt: ## Format code with golangci-lint
	golangci-lint fmt

lint: ## Run golangci-lint
	golangci-lint run ./...

vet: ## Run go vet
	go vet ./...

tidy: ## Tidy Go module dependencies
	go mod tidy
