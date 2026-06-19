# afk — the taboo orchestrator

`afk` is a small Go application built on **`pkg/taboo`** that runs taboo's AFK
("away from keyboard") agent loop. It replaces the bash-around-`taboo run`
workflow (PR #65) with ordinary, testable Go: the orchestration logic lives here,
and the GitHub Actions layer keeps only checkout/setup and token plumbing. This
is the foundation for the AFK implement/review loop — the `implement` slice ships
first, later slices stack on the same seams. See
[ADR 0010](../../docs/adr/0010-go-orchestrator-on-pkg-taboo.md).

## Layout

- `main.go` — the `afk` binary; stdlib-`flag` dispatch and the `implement`
  subcommand that wires the end-to-end flow.
- `internal/ghio` — GitHub/git I/O (`gh issue view`, `git push`, draft-PR
  create, label add) behind a fakeable single-method `Exec` seam.
- `internal/taborun` — the single seam onto `pkg/taboo`: loads the config,
  resolves a named workflow, and drives the run through `taboo.Orchestrator`.

## Usage

```
afk implement --issue N
```

`implement` drives one issue end-to-end:

1. **Fetch** the issue title/body via `gh` (`internal/ghio`).
2. **Run** the `implement` workflow on `pkg/taboo`: the agent runs inside a
   taboo-provisioned workshop and **commits in place** — it is git-**push-denied**.
3. **Push** the run's branch to origin.
4. **Open a draft PR** whose body is the agent's plan (read from `.taboo-plan.md`
   in the worktree), prefixed with `Closes #N`.
5. **Label** the PR `agent:review` to cascade into the review workflow.

All GitHub I/O is in Go; none of it is workflow bash.

### Running it (nested-module constraint)

`afk` is a **nested** Go module (see below), so from the repository root:

```
cd .taboo/orchestrator && go run . implement --issue N
```

`go run ./.taboo/orchestrator` from the repo root does **not** work — Go excludes
nested modules and reports "main module does not contain package …". In CI the
binary is built inside the module and run from the repo root instead:

```
( cd .taboo/orchestrator && go build -o "$RUNNER_TEMP/afk" . )
"$RUNNER_TEMP/afk" implement --issue N
```

Either way, the binary must run with **cwd = the repository root**: it resolves
the repo from `os.Getwd()` and reads `.taboo/taboo.yaml` relative to it.

## Nested module

The module is `module afk` with `replace github.com/josecabralf/taboo => ../../`
pinning it to the in-tree taboo. It is nested under the dot-directory `.taboo/`
on purpose: Go tooling ignores directories beginning with `.`, so packages here
are invisible to the root module's `./...` (and therefore to `make build/test`).
Nesting isolates the example as its own module while `replace` keeps it building
against the parent.

Because `./...` skips it, build/vet/test it explicitly:

```
cd .taboo/orchestrator && go build ./... && go vet ./... && go test ./...
```

A dedicated step in `.github/workflows/ci.yml` runs exactly this on every PR, so
the agent-loop code is gated even though the root build skips it.

## Config

`afk` reads the **same** `.taboo/taboo.yaml` the taboo CLI reads — one source of
truth for the workshop, agent profile, prompts, and the iteration/timeout/
completion-signal knobs. There is no orchestrator-specific config.

## Environment (for a real run)

- `GH_TOKEN` / `GH_REPO` — consumed by `gh` for issue/PR/label I/O (inherited
  from the process environment).
- the agent's model key — e.g. `OPENROUTER_API_KEY` for the opencode/OpenRouter
  agent configured in `taboo.yaml`.
