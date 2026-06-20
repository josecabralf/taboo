# afk — the taboo orchestrator

`afk` runs taboo's AFK ("away from keyboard") agent loop: take a GitHub issue,
have an agent implement it, push the branch, and open a draft PR; then, when the
PR is labelled, review it. The orchestration is ordinary Go built on **`pkg`**
(imported as `github.com/josecabralf/taboo/pkg`, package `taboo`) —
unit-testable and runnable locally — and GitHub Actions only does checkout,
setup, and token plumbing. Both the `implement` and `review` flows replaced
earlier bash-around-`taboo run` workflows (PRs #65, #79). See
[ADR 0010](../../docs/adr/0010-go-orchestrator-on-pkg-taboo.md).

## Layout

- `main.go` — the `afk` binary; stdlib-`flag` dispatch and the `implement`
  subcommand that wires the implement flow end-to-end. The run goes onto `pkg`
  through the bridge one-liner `taboo.RunWorkflow`, which discovers `taboo.yaml`,
  resolves the named workflow, and drives the run.
- `review.go` — the `review` subcommand wiring the review flow end-to-end,
  through the typed bridge `taboo.RunWorkflowAs[reviewResult]`.
- `internal/ghio` — GitHub/git I/O (`gh issue view`, `git push`, draft-PR
  create, label add, `gh pr diff`, and `gh api` PR-review POST) behind a fakeable
  `Exec` seam.
- `internal/diffmap` — parses a unified diff into the addressable `path:line`
  positions a review comment may target (the new-side added/context lines).

## Usage

```
afk implement --issue N
afk review --pr N
```

`implement` drives one issue end-to-end:

1. **Fetch** the issue title/body via `gh` (`internal/ghio`).
2. **Run** the `implement` workflow on `pkg`: the agent runs inside a
   taboo-provisioned workshop and **commits in place** — it is git-**push-denied**.
3. **Push** the run's branch to origin.
4. **Open a draft PR** whose body is the agent's plan (read from `.taboo-plan.md`
   in the worktree), prefixed with `Closes #N`.
5. **Label** the PR `agent:review`, which triggers the review workflow.

`review` reviews one PR and posts exactly one review:

1. **Fetch** the PR's unified diff via `gh pr diff` (`internal/ghio`).
2. **Run** the `review` workflow on `pkg`, asking for a `<result>` block of
   `{summary, comments:[{path, line, body}]}`, decoded in-loop by the typed
   bridge `taboo.RunWorkflowAs[reviewResult]`.
3. **Drop** any inline comment whose `path:line` is not addressable in the diff
   (`internal/diffmap`), logging a notice for each — never an error.
4. **Post** one PR review via `gh api`; an empty review (no summary, no in-diff
   comments) is skipped rather than posted, so GitHub never 422s.

All GitHub I/O is in Go; none of it is workflow bash.

## Nested module

The module is `module afk` with `replace github.com/josecabralf/taboo/pkg => ../../pkg`
pinning it to the in-tree taboo library module. It is nested under the dot-directory `.taboo/`
on purpose: Go tooling ignores directories beginning with `.`, so packages here
are invisible to the root module's `./...` (and therefore to `make build/test`).
Nesting isolates the example as its own module while `replace` keeps it building
against the parent.

Because it is nested, `go run ./.taboo/orchestrator` from the repo root does
**not** work — Go excludes nested modules and reports "main module does not
contain package …". Run it from inside the module:

```
cd .taboo/orchestrator && go run . implement --issue N
```

In CI the binary is built inside the module and run from the repo root instead:

```
( cd .taboo/orchestrator && go build -o "$RUNNER_TEMP/afk" . )
"$RUNNER_TEMP/afk" implement --issue N
```

Either way, run from somewhere **inside the repository** (the root is simplest,
and matches CI). afk hands the start directory (`os.Getwd()`) to the taboo
bridge, which ascends from there to find `.taboo/taboo.yaml`, so any
subdirectory of the repo works. Run it from outside the repo tree and the bridge
fails with `taboo: no taboo.yaml found from <dir>`.

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
