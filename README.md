# taboo

taboo is a Go library that orchestrates AI coding agents inside Canonical
workshop environments and lands their commits on a host git branch.

Each agent run gets its own workshop, an LXD-backed dev sandbox provisioned by
the `workshop` snap, with a fresh git worktree bind-mounted in at `/taboo/workspace`.
The repo's main `.git` is mounted at its identical host absolute path inside the
workshop, so a linked worktree's `.git` pointer resolves the same on both sides
(the two-mount rule). The agent edits and commits in place, and those commits
land directly on the host branch
with no extraction or sync step. `git push` is denied inside the workshop, so
the host owns integration. The library is the primary contract; a thin CLI
(`taboo`) wraps the common paths.

## Prerequisites

- The `workshop` snap. The CLI's `doctor` and `run` enforce a floor of
  `minWorkshopVersion = "0.9.1"` (`cli/internal/app/version.go`). The library has no
  compile-time dependency on workshop; it shells out to the `workshop` binary at
  runtime. Install with `sudo snap install workshop`.
- LXD, installed and initialized. Install with `sudo snap install lxd`.
- `git`.
- A baked agent SDK. taboo ships the agent SDKs embedded and seeds them into the
  project on first run, so the agent CLI exists inside the workshop. You do not
  author the workshop definition.
- Agent credentials in the host environment, per agent (see
  [docs/reference/agents.md](docs/reference/agents.md)). They are forwarded per
  run via `workshop exec --env` and never written to disk.
- A Go toolchain, only if you scaffold and run a `main.go` against the library.
- The managed repo must live on persistent storage, not under `/tmp` or `/run`.
  Those paths are tmpfs inside the workshop and the `.git` mount silently fails
  there. See [docs/explanation/isolation-model.md](docs/explanation/isolation-model.md).

## Install

Library (package `taboo`, import path `github.com/josecabralf/taboo`):

```sh
go get github.com/josecabralf/taboo
```

CLI (binary `taboo`):

```sh
go install github.com/josecabralf/taboo/cli@latest
```

The library and the CLI are separate Go modules in this monorepo —
`github.com/josecabralf/taboo` (yaml.v3-only) and
`github.com/josecabralf/taboo/cli` — so depending on the library does not pull in
the CLI's cobra/huh stack.

## Quickstart (library)

Scaffold a `taboo.yaml` once (`taboo init`, see the CLI quickstart below), then
call `taboo.RunWorkflow`: it locates the nearest `taboo.yaml` above the start
directory, resolves the named workflow into a run, and executes it over the
production `Commander`.

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/josecabralf/taboo"
)

func main() {
	res, err := taboo.RunWorkflow(
		context.Background(),
		"/home/me/code/myrepo", // start dir; taboo.yaml is found above it
		"fix",                  // workflow name
		nil,                    // template vars for {{VAR}} placeholders
		taboo.PlanOverrides{Branch: "taboo/fix-readme"},
		taboo.NewExecCommander(),
	)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("branch: %s\ncommit: %s\n", res.Branch, res.Commit)
}
```

`RunWorkflow` prepares the workshop, adds a fresh worktree on the override branch,
and runs the agent. The agent commits in place, so `res.Commit` is the branch HEAD
on the host after the run. `res.Branch` names the worktree's branch and
`res.Output` holds the captured agent stdout; read files the agent left behind
with `res.Artifact(relpath)`. The run itself needs a workshop host (workshop + LXD).

`RunWorkflowAs[T]` is the typed variant: it decodes the agent's structured output
into a `T` with no caller assertion. For a fuller walkthrough, see
[docs/tutorials/library-first-run.md](docs/tutorials/library-first-run.md).

## Quickstart (CLI)

```sh
taboo init --agent opencode --model openrouter/qwen/qwen3-coder-plus
cp .taboo/.env.example .taboo/.env
# edit .taboo/.env to set the one credential your agent needs
taboo doctor
taboo run fix
```

`taboo init` scaffolds `.taboo/` (`taboo.yaml`, `.gitignore`, `.env.example`, and
seeded `prompts/`) and never launches a workshop. `taboo doctor` checks host
readiness. `taboo run fix` runs the `fix` workflow on a fresh branch. The `run`
step needs a workshop host. See
[docs/tutorials/cli-first-run.md](docs/tutorials/cli-first-run.md).

## Entry points

The library has three entry patterns and a set of building blocks, all reached
through the single `github.com/josecabralf/taboo` import. Full signatures are
in [docs/reference/library-api.md](docs/reference/library-api.md).

`RunWorkflow` / `RunWorkflowAs[T]` is the one-call bridge from a `taboo.yaml`. It
locates the nearest config above a start directory, resolves the named workflow
into a run, and executes it over a `Commander`. `RunWorkflowAs[T]` decodes the
agent's structured output into a statically typed `T`. Both return an
`OrchestratedResult` with `Iterations`, `StopReason`, and a decoded `Result`. See
[docs/guides/iterate-until-done.md](docs/guides/iterate-until-done.md).

`Plan` is the inspect-then-run path. `LoadConfig` parses a `taboo.yaml` into a
`ProjectConfig`; `(*ProjectConfig).Plan` resolves a workflow plus per-call
`PlanOverrides` into a `Plan`, a pure, inspectable description of one run; tweak
`plan.Config` and `plan.Request`, then `(*Plan).Run` executes it. The iteration
loop (`MaxIterations`, `CompletionSignal`) lives on `plan.Request`.

`Pool` fans out runs. `NewPool(plan.Config, limit, cmd).Run(ctx, reqs)` runs many
`RunRequest`s with at most `limit` in flight, one workshop per slot. Results
return in input order; a per-run failure is recorded on `results[i].Err` without
aborting the batch. See [docs/guides/fan-out-runs.md](docs/guides/fan-out-runs.md).

`AgentProfile` is the agent contract: `Name`, `BuildCommand`, `CredentialEnvKeys`,
`Sessions`. Build one with `NewProfile(taboo.OpenCode, model)`,
`NewProfile(taboo.ClaudeCode, model)`, or `NewProfile(taboo.GitHubCopilot, model)`;
it returns a wrapped `ErrUnknownAgent` for an unknown name. See
[docs/reference/agents.md](docs/reference/agents.md).

`ResultExtractor` decodes a typed result from agent output. `JSONResult[T]()`
finds the last `<result>...</result>` block and decodes its JSON into `T`, with
`WithStrictFields`, `WithDelimiters`, and an optional `Validator`. See
[docs/guides/typed-results.md](docs/guides/typed-results.md).

`Substitute` is a pure prompt-template helper: `Substitute(tmpl, vars)` fills
`{{VAR}}` placeholders and errors on any missing variable.

`Hooks` run lifecycle commands. Set `RunRequest.Hooks` to
`Hooks{OnWorkshopReady: []Hook{...}}` and the hooks run after the workshop starts
and before the agent execs, on every run. A hook runs on the host in the worktree
by default, or via `workshop exec` when `InWorkshop` is true. See
[docs/guides/prepare-the-workspace-with-hooks.md](docs/guides/prepare-the-workspace-with-hooks.md).

## CLI

The `taboo` binary (`cli/internal/app/main.go`) registers these subcommands. Full flag
detail is in [docs/reference/cli.md](docs/reference/cli.md); the config file is
documented in [docs/reference/taboo-yaml.md](docs/reference/taboo-yaml.md).

| Command | What it does |
|---|---|
| `taboo init` | Scaffold `.taboo/` (`taboo.yaml`, `.gitignore`, `.env.example`, optional `prompts/` and `main.go`). Never launches a workshop. |
| `taboo run [workflow]` | Run a workflow or ad-hoc `--prompt` on a fresh branch. STDOUT is the machine result; STDERR streams agent output. |
| `taboo validate` | Check `taboo.yaml` for config errors. |
| `taboo doctor` | Check host readiness (`workshop`, `lxd`, `git`, and config-aware checks). |
| `taboo list` | Read-only inventory of workshops, worktrees, and branches. |
| `taboo clean` | Remove worktrees, and optionally workshops and branches. |

## Documentation

The `docs/` tree is organized by Diátaxis type. Start at
[docs/README.md](docs/README.md), or jump in:

- Tutorials: [library-first-run](docs/tutorials/library-first-run.md),
  [cli-first-run](docs/tutorials/cli-first-run.md).
- How-to guides: [iterate until done](docs/guides/iterate-until-done.md),
  [fan out runs](docs/guides/fan-out-runs.md),
  [typed results](docs/guides/typed-results.md),
  [prepare the workspace with hooks](docs/guides/prepare-the-workspace-with-hooks.md).
- Reference: [library API](docs/reference/library-api.md),
  [agents](docs/reference/agents.md), [CLI](docs/reference/cli.md),
  [taboo.yaml](docs/reference/taboo-yaml.md).
- Explanation: [isolation model](docs/explanation/isolation-model.md),
  [design](docs/explanation/design.md),
  [dogfooding](docs/explanation/dogfooding.md).

## Design and decisions

The library is the primary contract and the CLI is a thin consumer of it. All
host side effects pass through a single `Commander` seam, so tests substitute a
fake while `NewExecCommander` shells out in production.
taboo runs one workshop per distinct agent, launched lazily and reused. The
load-bearing decisions are recorded as ADRs:
[argv/stdin command contract](docs/adr/0001-agentprofile-argv-stdin-command-contract.md),
[structured output with generics](docs/adr/0002-structured-output-generics-encoding-json.md),
[session resume and fork](docs/adr/0003-session-resume-fork-command-contract.md),
[multi-key credential env](docs/adr/0004-multi-key-credential-env.md),
[declarative agent registry](docs/adr/0005-agent-registry-declarative-roster.md),
[deferred warm fan-out](docs/adr/0006-defer-warm-fanout-single-repo-workshops.md),
[nested worktree placement](docs/adr/0007-nested-worktree-placement.md), and
[model-format hint and fuzzy agent match](docs/adr/0008-model-format-hint-and-fuzzy-agent-match.md).
See [docs/explanation/design.md](docs/explanation/design.md) for the reasoning.

## Testing

From the `Makefile`:

```sh
make test              # unit tests (go test ./... -count=1 -cover), includes godoc examples
make test-integration  # integration tests; requires workshop + LXD, build tag `integration`
```

`make test` runs everywhere. `make test-integration` exercises the real
`workshop` CLI and LXD and only runs on a host that has both installed.

## taboo dogfoods itself

taboo runs its own issue-to-review loop with a small Go orchestrator (`afk`,
built on `pkg`), a pair of GitHub Actions, and some prompts and skills. There is
no new GitHub or push machinery in taboo core; the loop is scaffolding layered
around the library's single-run primitive.

Label an issue `agent:implement` and GitHub Actions runs `afk implement`
(Claude Code / Opus) inside a workshop on the runner. The agent explores the
repo, writes a plan, does test-driven development, validates against the
project's own checks, and commits in place — it is push-denied, so it never
touches GitHub. The orchestrator then pushes the branch,
opens a draft PR whose body is the agent's plan, and labels that PR
`agent:review`. The label fires the second workflow (`agent-review.yml`), which
runs `afk review` (Claude Code / Opus): it reads the PR diff and posts a single
review with inline plus top-level comments. Throughout, the labels form a small state machine
(`agent:implement` → `agent:in-progress` → `agent:review`, with `agent:blocked`
on failure).

The split is deliberate: the agent, inside its workshop, writes code and
commits; the host workflow layer (`.github/`) owns every GitHub side effect —
fetching the issue, pushing, opening and labelling PRs, posting the review.
taboo core stays frozen.

- [Dogfooding the agent loop](docs/explanation/dogfooding.md) — the two
  workflows, the label state machine, the trust model, and the one-time setup.

## Status

The library is feature-complete and tested. A run produces a named, isolated
branch per worktree, driven by `RunWorkflow`, a resolved `Plan`, or `Pool`. Three
agents are supported: `opencode`, `claude-code`, and `github-copilot`. The CLI covers `init`,
`run`, `validate`, `doctor`, `list`, and `clean`; its `run` drives the iteration
loop through `--iterations` and `--signal`. Fan-out, typed structured output,
and lifecycle hooks are available through the Go API only. The module is
pre-`v0.1.0` until a release is tagged.

## License

MIT. Copyright (c) 2026 Jose Ignacio Cabral Farre. See [LICENSE](LICENSE).
