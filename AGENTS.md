# taboo

Go library + thin CLI (Go 1.26) that runs AI coding agents inside Canonical
**workshop** (LXD) sandboxes and lands their commits on a host git branch. This
is a monorepo of three modules with the library at the repo root and no
`go.work`: the library `github.com/josecabralf/taboo` (repo root, package
`taboo`, yaml.v3-only) is the primary contract; the CLI
`github.com/josecabralf/taboo/cli` (`cli/`) and
the afk demo (`.taboo/orchestrator/`, module `afk`) are thin consumers wired to
the library via `replace`.

## Layout

- the repo root — the library and the primary contract. The curated public
  surface is `facade.go` + `bridge.go`; everything under `internal/` is
  implementation.
- `internal/run/` — the run drivers: `runner.go` (single run),
  `orchestrator.go` (iteration loop), and `pool.go` (fan-out).
- `internal/agent/agent_<name>.go` (+ `_test.go`) — one file per agent
  profile; `internal/agent/registry.go` is the declarative roster.
- `internal/workshop/sdk/<name>/` — agent SDKs, embedded via `//go:embed sdk`
  and seeded into the managed repo at runtime.
- `internal/config/` — `taboo.yaml` parsing and plan/profile resolution. The
  one-call bridge (`RunWorkflow`/`RunWorkflowAs`) is `bridge.go` at the repo root,
  not here.
- `cli/` — the `taboo` CLI module (cobra + huh). A thin `cli/main.go` delegates
  to `cli/internal/app/` (`init`, `run`, `validate`, `doctor`, `list`, `clean`),
  which imports only the `taboo` facade.
- `.taboo/orchestrator/` — the afk demo (module `afk`), the adopter-layout
  reference consumer of the library.

## Commands

Run from the repo root. Each target runs the root library directly, then fans
out across the subdir modules (cli, afk); the unit gate runs directly:

```sh
make build   # go build ./...   in every module
make vet     # go vet ./...     in every module
make test    # go test ./... -count=1 -cover   in every module (also runs the godoc examples)
```

- **Lint through the workshop, not the host**: `workshop run -- make lint`. Raw
  `make lint` on the host emits stale-cache warnings and can report false
  results; the workshop has a clean, isolated cache. Needs a launched workshop
  (`workshop launch taboo`).
- `make test-integration` (runs at the repo root: `go test -tags integration ./...`,
  the suite in `internal/run/`) drives real `workshop` + LXD; host-only,
  never in the dev workshop or CI.
- `make test-race` forces `CGO_ENABLED=1` (needs a C compiler).
- `make fmt` formats, `make tidy` runs `go mod tidy` across modules, and
  `make setup` installs the dev tools.

## Conventions

- Assert behavior through the single `Commander` seam: inject a fake in tests;
  `NewExecCommander()` is production. Pure logic is table-driven; anything
  touching real `workshop`/LXD goes behind the `integration` build tag.
- Adding an agent = new `internal/agent/agent_<name>.go` (+ `_test.go` with
  `BuildCommand`/`CredentialEnvKeys` assertions, ADR 0001), a
  `internal/workshop/sdk/<name>/` dir, a constructor named `New<Name>`, a
  `Name` constant of type `AgentName` in the same file, and one line in
  `internal/agent/registry.go` (ADR 0005). The facade re-exports the public
  constant from `facade.go`.
- Keep the CLI thin: only scalars and file paths are CLI config; fan-out, typed
  results, and hooks stay library-only.

## Gotchas

- Only `opencode`, `claude-code`, and `github-copilot` are real agents. The `codex` and
  `pi` SDK dirs under `internal/workshop/sdk/` are unregistered stubs — not in
  `internal/agent/registry.go`, not supported.

## Pointers

- `README.md` — overview, install, library + CLI quickstarts, entry points.
- `docs/` (Diátaxis; start at `docs/README.md`): `reference/` is the verified
  library/CLI/config surface; `adr/0001`–`0010` record load-bearing decisions.
  Link here instead of re-deriving the API. The published site excludes `adr/`,
  `spikes/`, `RELEASING.md`, and `docs/README.md` (`mkdocs.yml` `exclude_docs`), so
  those are repo-only.
