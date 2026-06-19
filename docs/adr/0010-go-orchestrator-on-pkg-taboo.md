# Replace the bash AFK loop with a Go orchestrator built on pkg/taboo

## Status

accepted

## Context & decision

The AFK ("away from keyboard") agent loop runs an issue end-to-end with no human
at the terminal: a labeled issue is fetched, an agent implements it inside a
taboo workshop, a branch is pushed, a draft PR is opened, and a review label is
applied to cascade into the review workflow. PR #65 wired this as **bash around
`taboo run`** in a GitHub Actions workflow — the CLI ran the agent, and shell
steps did every GitHub side effect (issue fetch, push, PR, label) with `gh`.

That arrangement has the orchestration logic — branch naming, prompt-variable
injection, plan-to-PR-body assembly, error handling — living in workflow YAML,
where it is untestable, unrunnable locally, and shells out to taboo's *CLI*
rather than its primary deliverable, the `pkg/taboo` library. taboo sells itself
as a library for "engineers building agent pipelines in Go" (CONTEXT.md); the
canonical AFK pipeline not using that library was a credibility gap and a
dogfooding miss.

**Decision: the AFK loop becomes a small Go application built on `pkg/taboo`.**
A new `afk` binary owns the whole `implement` flow as ordinary, testable Go;
GitHub I/O moves into Go behind a fakeable seam; the workflow keeps only
checkout/setup, token plumbing, and label bookkeeping. This is the **foundation
tracer bullet** for AFK — the implement slice — that later slices (review,
multi-issue waves) stack on. Four choices make it up.

### Orchestrate in Go on `pkg/taboo`, not bash on the CLI

`afk` drives runs through the library directly:
`taboo.NewOrchestrator(taboo.New(cfg, taboo.NewExecCommander())).Run(...)`. The
`internal/taborun` package is the single seam onto taboo: it loads the **same**
`.taboo/taboo.yaml` the CLI reads (one source of truth — workshop, agent
profile, completion signal, iteration/timeout knobs all resolve from config),
substitutes prompt variables with `taboo.Substitute`, and runs the named
workflow. Replacing `exec("taboo", "run", …)` with library calls makes the loop
the reference consumer of taboo's own contract, and keeps the agent
**git-push-denied**: it commits in place (CONTEXT.md), the host owns publishing.

### GitHub I/O inside Go, behind a fakeable exec seam

All `gh`/`git` side effects — `gh issue view`, `git push`, `gh pr create
--draft`, `gh pr edit --add-label` — live in `internal/ghio`, behind a
single-method seam (`type Exec interface { Run(ctx, name, args...) (string,
error) }`). Production shells out via `os/exec` inheriting the environment (so
`gh` reads `GH_REPO`/`GH_TOKEN`); tests substitute a fake and assert on the
argv, with no real processes. The workflow no longer scripts GitHub; it provides
credentials and runs the binary.

### A nested Go module under `.taboo/`

`afk` is a **nested** module at `.taboo/orchestrator/` (`module afk`, with
`replace github.com/josecabralf/taboo => ../../` onto the parent). It must be
nested under a dot-directory: Go tooling ignores directories beginning with `.`,
so the root module's `./...` (and therefore `make build/test`) cannot see it. A
flat package in the root module would either drag the example's surface into the
library's public module or, if placed under `.taboo/`, simply be invisible to the
parent's build. Nesting isolates the example as its own module while `replace`
keeps it pinned to the in-tree taboo.

The cost is two ergonomic quirks, both documented where they bite. It **cannot**
be `go run ./.taboo/orchestrator` from the parent module — Go reports "main
module does not contain package …" because the nested module is excluded. You
either `cd .taboo/orchestrator && go run . implement --issue N`, or build inside
the module and run the binary from the repo root (what CI does); the binary
resolves the repository from `os.Getwd()`, so it must run with cwd = repo root to
find `.taboo/taboo.yaml`. And because `./...` skips it, **CI must build/vet/test
it explicitly** — a dedicated `ci.yml` step (`cd .taboo/orchestrator && go build
./... && go vet ./... && go test ./...`) gates the agent-loop code on every PR.

### A lean, stdlib-`flag` example

The binary uses stdlib `flag` for subcommand dispatch (`afk implement --issue
N`) — no cobra. The CLI under `cmd/taboo` carries cobra because it is the
product; the AFK example is a *demonstration of the library*, and it stays as
lean as `pkg/taboo` itself, whose only dependency is yaml.v3. Adding cobra to a
two-subcommand-at-most example would be more framework than the example is code.

## Considered options

- **Go orchestrator on `pkg/taboo` (chosen).** Makes the AFK loop testable,
  locally runnable, and the reference consumer of taboo's primary deliverable.
  Costs the nested-module ceremony (cd-in / build-then-run, an explicit CI step).
- **Keep the bash-around-`taboo run` loop (PR #65, rejected).** Zero new code,
  but the orchestration logic stays in untestable workflow YAML, shells out to
  the CLI rather than the library, and leaves the flagship AFK pipeline not
  dogfooding the thing taboo ships.
- **A flat package in the root module instead of a nested module (rejected).**
  Avoids the `go run` quirk and the extra CI step, but either pollutes the
  library's public module with example code and its own `main`, or — if tucked
  under `.taboo/` — is invisible to `./...` anyway *and* shares the library's
  dependency set. Nesting keeps the example's surface and deps out of the
  library.
- **cobra instead of stdlib `flag` (rejected).** Buys nothing for one
  subcommand; adds a dependency to an example whose whole point is to stay as
  lean as the library it showcases.
- **Leave GitHub I/O in the workflow (rejected).** Keeps `ghio` out of Go, but
  the side effects stay unfakeable bash, untestable and unrunnable locally —
  exactly the coupling this ADR removes.

## Consequences

- **The AFK implement loop is Go.** Branch naming, prompt injection, plan-to-PR
  assembly, and error handling are ordinary unit-tested code; the workflow keeps
  only checkout/setup, token plumbing, and label bookkeeping.
- **`pkg/taboo` is dogfooded.** The flagship pipeline is the library's reference
  consumer, exercising `LoadConfig`, `Substitute`, `WorkshopName`, and
  `Orchestrator` exactly as an external integrator would.
- **One config, one source of truth.** `afk` reads the same `.taboo/taboo.yaml`
  the CLI reads; there is no second place to keep workshop/agent/loop settings.
- **Nested-module tax, paid in two places.** It is not `go run`-able from the
  parent (`cd`-in or build-then-run, cwd = repo root); and `ci.yml` carries a
  dedicated build/vet/test step because `make build/test` skips dot-dirs.
- **Foundation for later AFK slices.** The `ghio`/`taborun` seams and the
  `flag`-dispatched binary are the substrate the review loop and multi-issue
  fan-out build on, without re-litigating these choices.
- Implemented in issue #78 (the implement tracer bullet), replacing the bash
  loop from PR #65.
