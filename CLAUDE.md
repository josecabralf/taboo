# taboo

Go library + thin CLI (`github.com/josecabralf/taboo`, Go 1.26) that runs AI
coding agents inside Canonical **workshop** (LXD) sandboxes and lands their
commits on a host git branch. The library (`pkg/taboo`) is the primary contract;
`cmd/taboo` is a thin consumer of it.

## Layout

- `pkg/taboo/` — the library and the primary contract. One concern per file; the
  run drivers are `runner.go` (single run), `orchestrator.go` (iteration loop),
  and `pool.go` (fan-out).
- `pkg/taboo/agent_<name>.go` (+ `_test.go`) — one file per agent profile;
  `registry.go` is the declarative roster.
- `pkg/taboo/sdk/<name>/` — agent SDKs, embedded via `//go:embed sdk` and seeded
  into the managed repo at runtime.
- `cmd/taboo/` — the `taboo` CLI (cobra + huh): `init`, `run`, `validate`,
  `doctor`, `list`, `clean`.

## Commands

Run from the repo root. The unit gate runs directly:

```sh
make build   # go build ./...
make vet     # go vet ./...
make test    # go test ./... -count=1 -cover   (also compiles + runs the godoc examples)
```

- **Lint through the workshop, not the host**: `workshop run -- make lint`. Raw
  `make lint` on the host emits stale-cache warnings and can report false
  results; the workshop has a clean, isolated cache. Needs a launched workshop
  (`workshop launch taboo`).
- `make test-integration` (`go test -tags integration ./pkg/taboo/`) drives real
  `workshop` + LXD; host-only, never in the dev workshop or CI.
- `make test-race` forces `CGO_ENABLED=1` (needs a C compiler).

## Conventions

- Assert behavior through the single `Commander` seam: inject a fake in tests;
  `NewExecCommander()` is production. Pure logic is table-driven; anything
  touching real `workshop`/LXD goes behind the `integration` build tag.
- Adding an agent = new `agent_<name>.go` (+ `_test.go` with
  `BuildCommand`/`CredentialEnvKeys` assertions, ADR 0001), a
  `pkg/taboo/sdk/<name>/` dir, and one line in `registry.go` (ADR 0005).
- Keep the CLI thin: only scalars and file paths are CLI config; fan-out, typed
  results, and hooks stay library-only.

## Gotchas

- Only `opencode`, `claude-code`, and `copilot` are real agents. `sdk/codex` and
  `sdk/pi` are unregistered stubs — not in `registry.go`, not supported.

## Pointers

- `README.md` — overview, install, library + CLI quickstarts, entry points.
- `docs/` (Diátaxis; start at `docs/README.md`): `reference/` is the verified
  library/CLI/config surface; `adr/0001`–`0008` record load-bearing decisions.
  Link here instead of re-deriving the API.
