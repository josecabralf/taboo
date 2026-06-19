.PHONY: help setup build test test-race test-integration fmt lint vet tidy orchestrator

.DEFAULT_GOAL := help

# The afk orchestrator is a nested Go module under the dot-directory .taboo/, so
# the root ./... never sees it (see the orchestrator target).
ORCHESTRATOR_DIR := .taboo/orchestrator

help: ## List available targets
	@grep -E '^[a-zA-Z_-]+:.*##' Makefile | awk -F ':.*## ' '{printf "  \033[1;36m%-16s\033[0m %s\n", $$1, $$2}'

setup: ## Download module dependencies
	go mod download

build: ## Build all packages
	go build ./...

# Unit tests only. The integration suite is gated behind the `integration`
# build tag (see test-integration) and is deliberately excluded here so it
# never runs inside the dev workshop — a workshop within a workshop is
# problematic. CI runs this target via `workshop run make test`.
test: ## Run unit tests
	go test ./... -count=1 -cover

# Unit tests under the race detector. Requires cgo + a C compiler (the workshop's
# setup-base hook installs gcc); CGO_ENABLED is forced on here so the rest of the
# build stays pure-Go. Slower than `test`, so it's a separate opt-in target.
test-race: ## Run unit tests under the race detector
	CGO_ENABLED=1 go test -race ./... -count=1

# Host-only: exercises the real `workshop` CLI and LXD. Not wired into the dev
# workshop or CI; run it directly on a machine with workshop + LXD installed.
test-integration: ## Run integration tests (requires workshop + LXD)
	go test -tags integration ./pkg/taboo/ -count=1 -v

fmt: ## Format code with golangci-lint
	golangci-lint fmt

lint: ## Run golangci-lint
	golangci-lint run ./...

vet: ## Run go vet
	go vet ./...

tidy: ## Tidy Go module dependencies
	go mod tidy

# The root ./... skips dot-directories, so the nested afk module is invisible to
# build/test above. Build, vet and test it explicitly so its agent-loop code is
# gated on every PR. Each line runs in its own shell, so the cd is repeated.
orchestrator: ## Build, vet and test the nested afk orchestrator module
	cd $(ORCHESTRATOR_DIR) && go build ./...
	cd $(ORCHESTRATOR_DIR) && go vet ./...
	cd $(ORCHESTRATOR_DIR) && go test ./... -count=1 -cover
