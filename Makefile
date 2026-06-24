.PHONY: help setup build test test-race test-integration fmt lint vet tidy docs-serve

.DEFAULT_GOAL := help

# taboo is a monorepo. The yaml.v3-only library module lives at the repo root
# (github.com/josecabralf/taboo); two more modules live in subdirectories:
#   cli/                 the cobra/huh CLI     (github.com/josecabralf/taboo/cli)
#   .taboo/orchestrator/ the afk demo consumer (module afk)
# Each root target runs the library's own command here (the //go:embed sdk tree
# ships from internal/workshop, so build/test at the root travel with the embed),
# then fans out across the subdir modules so `workshop run -- make <target>` and
# CI gate every module in one shot. The workshop runs `make "$@"` at the repo
# root (see workshop.yaml).
MODULES := cli .taboo/orchestrator

# fanout runs the current target ($@) in every subdir module, stopping at the
# first failure. The targets below reuse it via $(fanout) after running the
# library target at the root, so each target name lives in exactly one place and
# the echoed label can never drift from the target.
define fanout
@for m in $(MODULES); do echo "==> $@ $$m"; $(MAKE) -C $$m $@ || exit $$?; done
endef

help: ## List available targets
	@grep -E '^[a-zA-Z_-]+:.*##' Makefile | awk -F ':.*## ' '{printf "  \033[1;36m%-16s\033[0m %s\n", $$1, $$2}'

setup: ## Download dependencies in every module
	go mod download
	$(fanout)

build: ## Build every module
	go build ./...
	$(fanout)

# Unit tests only. The library's integration suite is gated behind the
# `integration` build tag (see test-integration) and is deliberately excluded
# here so it never runs inside the dev workshop — a workshop within a workshop is
# problematic.
test: ## Unit-test every module
	go test ./... -count=1 -cover
	$(fanout)

# Race detector needs cgo; only the library and CLI carry concurrency worth
# racing (the afk demo is a thin orchestrator), so it runs on the library and cli.
test-race: ## Race-test the library and cli
	CGO_ENABLED=1 go test -race ./... -count=1
	@$(MAKE) -C cli test-race

# Host-only: the integration suite lives in the library module (the repo root)
# and needs the real workshop CLI + LXD. Not wired into the dev workshop or CI.
test-integration: ## Run library integration tests (requires workshop + LXD)
	go test -tags integration ./... -count=1 -v

fmt: ## Format every module
	golangci-lint fmt
	$(fanout)

lint: ## Lint every module
	golangci-lint run ./...
	$(fanout)

vet: ## Vet every module
	go vet ./...
	$(fanout)

tidy: ## Tidy every module
	go mod tidy
	$(fanout)

# Docs site (Material for MkDocs). Root-only (the site lives at the repo root),
# so no fanout. In the workshop the toolchain is provisioned from requirements.txt
# by .workshop/taboo/hooks/setup-project; on a dev's own machine, first run
# `pip install -r requirements.txt`. `mkdocs serve` binds DOCS_ADDR. On the host
# the site is at http://localhost:8000. Inside the workshop, localhost:8000 is
# reachable only from within the container; to reach it from the host, bind the
# container interface and open the workshop's IP, e.g.
# `DOCS_ADDR=0.0.0.0:8000 make docs-serve`, then open port 8000 on the IP from
# `lxc list`.
DOCS_ADDR ?= localhost:8000
docs-serve: ## Live-preview the docs site at http://localhost:8000 (mkdocs serve)
	mkdocs serve --dev-addr $(DOCS_ADDR)
