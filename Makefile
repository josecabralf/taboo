.PHONY: help setup build test test-race test-integration fmt lint vet tidy

.DEFAULT_GOAL := help

# taboo is a monorepo of three Go modules with no root go.mod and no go.work:
#   pkg/                 the yaml.v3-only library (github.com/josecabralf/taboo/pkg)
#   cli/                 the cobra/huh CLI     (github.com/josecabralf/taboo/cli)
#   .taboo/orchestrator/ the afk demo consumer (module afk)
# Each module owns its own Makefile. These root targets fan out across all three
# so `workshop run -- make <target>` and CI gate every module in one shot. The
# workshop runs `make "$@"` at the repo root (see workshop.yaml).
MODULES := pkg cli .taboo/orchestrator

# fanout runs the current target ($@) in every module, stopping at the first
# failure. The targets below reuse it via $(fanout), so each target name lives in
# exactly one place and the echoed label can never drift from the target.
define fanout
@for m in $(MODULES); do echo "==> $@ $$m"; $(MAKE) -C $$m $@ || exit $$?; done
endef

help: ## List available targets
	@grep -E '^[a-zA-Z_-]+:.*##' Makefile | awk -F ':.*## ' '{printf "  \033[1;36m%-16s\033[0m %s\n", $$1, $$2}'

setup: ## Download dependencies in every module
	$(fanout)

build: ## Build every module
	$(fanout)

test: ## Unit-test every module
	$(fanout)

# Race detector needs cgo; only the library and CLI carry concurrency worth
# racing (the afk demo is a thin orchestrator), so it runs on pkg and cli.
test-race: ## Race-test pkg and cli
	@$(MAKE) -C pkg test-race
	@$(MAKE) -C cli test-race

# Host-only: the integration suite lives in the library module and needs the real
# workshop CLI + LXD. Not wired into the dev workshop or CI.
test-integration: ## Run library integration tests (requires workshop + LXD)
	@$(MAKE) -C pkg test-integration

fmt: ## Format every module
	$(fanout)

lint: ## Lint every module
	$(fanout)

vet: ## Vet every module
	$(fanout)

tidy: ## Tidy every module
	$(fanout)
