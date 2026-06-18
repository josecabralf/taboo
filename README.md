# taboo

taboo is a Go library that orchestrates AI coding agents inside Canonical
workshop environments and lands their commits on a host git branch.

Each agent run gets its own workshop, an LXD-backed dev sandbox provisioned by
the `workshop` snap, with a fresh git worktree bind-mounted in at `/taboo/workspace`.
The repo's main `.git` is mounted at its identical host absolute path inside the
workshop, so a linked worktree's `.git` pointer resolves the same on both sides
(the two-mount rule in `pkg/taboo/template.go`, `gitCommonTarget`). The agent
edits and commits in place, and those commits land directly on the host branch
with no extraction or sync step. `git push` is denied inside the workshop, so
the host owns integration. The library is the primary contract; a thin CLI
(`taboo`) wraps the common paths.

## Prerequisites

- The `workshop` snap. The CLI's `doctor` and `run` enforce a floor of
  `minWorkshopVersion = "0.9.1"` (`cmd/taboo/version.go`). The library has no
  compile-time dependency on workshop; it shells out to the `workshop` binary at
  runtime. Install with `sudo snap install workshop`.
- LXD, installed and initialized. Install with `sudo snap install lxd`.
- `git`.
- A baked agent SDK. taboo ships the agent SDKs embedded
  (`//go:embed sdk` in `pkg/taboo/runner.go`) and seeds them into the project on
  first run, so the agent CLI exists inside the workshop. You do not author the
  workshop definition.
- Agent credentials in the host environment, per agent (see
  [docs/reference/agents.md](docs/reference/agents.md)). They are forwarded per
  run via `workshop exec --env` and never written to disk.
- A Go toolchain, only if you scaffold and run a `main.go` against the library.
- The managed repo must live on persistent storage, not under `/tmp` or `/run`.
  Those paths are tmpfs inside the workshop and the `.git` mount silently fails
  there. See [docs/explanation/isolation-model.md](docs/explanation/isolation-model.md).

## Install

Library (package `taboo`, import path `github.com/josecabralf/taboo/pkg/taboo`):

```sh
go get github.com/josecabralf/taboo/pkg/taboo
```

CLI (binary `taboo`):

```sh
go install github.com/josecabralf/taboo/cmd/taboo@latest
```

The library and the CLI are one Go module, `github.com/josecabralf/taboo`.

## Quickstart (library)

Construct a `taboo.Config`, build a `Runner` with `taboo.New` and the production
`Commander`, then call `Run` with a `RunRequest`:

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	taboo "github.com/josecabralf/taboo/pkg/taboo"
)

func main() {
	cfg := taboo.Config{
		Workshop:   "myrepo-opencode",
		Base:       "ubuntu@24.04",
		Agent:      taboo.OpenCode("openrouter/qwen/qwen3-coder-plus"),
		RepoPath:   "/home/me/code/myrepo",
		ProjectDir: "/home/me/code/myrepo/.taboo",
	}

	runner := taboo.New(cfg, taboo.NewExecCommander())

	res, err := runner.Run(context.Background(), taboo.RunRequest{
		Branch: "taboo/fix-readme",
		Prompt: "Fix the typos in README.md and commit.",
		Stdout: os.Stderr,
		Stderr: os.Stderr,
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("branch: %s\ncommit: %s\n", res.Branch, res.Commit)
}
```

`Run` does Setup (ensure the workshop, add a fresh worktree on `req.Branch`, swap
it into the workshop) then Exec (run the agent once). The agent commits in place,
so `res.Commit` is the branch HEAD on the host after the run. `res.Branch` and
`res.WorktreePath` come from Setup; `res.Output` holds the captured agent stdout.
The run itself needs a workshop host (workshop + LXD).

The godoc example `ExampleRunner_Run` in `pkg/taboo/example_test.go` mirrors this
shape. For a fuller walkthrough, see
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

The library exposes three run drivers and a set of building blocks. Full
signatures are in [docs/reference/library-api.md](docs/reference/library-api.md).

`Runner` (`pkg/taboo/runner.go`) is the single-run primitive. `New(cfg, cmd)`
builds one; `Run` does Setup then Exec. `Setup` and `Exec` are also exported, so
you can Setup once and Exec repeatedly into the same worktree.

`Orchestrator` (`pkg/taboo/orchestrator.go`) wraps a `Runner` in an iteration
loop. `NewOrchestrator(runner).Run(ctx, req)` re-execs the agent up to
`MaxIterations`, stopping early when `CompletionSignal` appears in the agent's
stdout. `OrchestratedResult` adds `Iterations`, `StopReason`, and a decoded
`Result`. See [docs/guides/iterate-until-done.md](docs/guides/iterate-until-done.md).

`Pool` (`pkg/taboo/pool.go`) fans out runs. `NewPool(cfg, limit, cmd).Run(ctx,
reqs)` runs many `RunRequest`s with at most `limit` in flight, one workshop per
slot. Results return in input order; a per-run failure is recorded on
`results[i].Err` without aborting the batch. See
[docs/guides/fan-out-runs.md](docs/guides/fan-out-runs.md).

`AgentProfile` (`pkg/taboo/agent.go`) is the agent contract: `Name`,
`BuildCommand`, `CredentialEnvKeys`, `Sessions`. The constructors are
`OpenCode(model)`, `ClaudeCode(model)`, `Copilot(model)`. See
[docs/reference/agents.md](docs/reference/agents.md).

`ResultExtractor` (`pkg/taboo/result.go`) decodes a typed result from agent
output. `JSONResult[T]()` finds the last `<result>...</result>` block and decodes
its JSON into `T`, with `WithStrictFields`, `WithDelimiters`, and an optional
`Validator`. See [docs/guides/typed-results.md](docs/guides/typed-results.md).

`Substitute` (`pkg/taboo/prompt.go`) is a pure prompt-template helper:
`Substitute(tmpl, vars)` fills `{{VAR}}` placeholders and errors on any missing
variable.

`Hooks` (`pkg/taboo/hooks.go`) run lifecycle commands. `Hooks{OnWorkshopReady:
[]Hook{...}}` runs after the workshop starts and before the agent execs, on every
run. A hook runs on the host in the worktree by default, or via `workshop exec`
when `InWorkshop` is true. See
[docs/guides/prepare-the-workspace-with-hooks.md](docs/guides/prepare-the-workspace-with-hooks.md).

## CLI

The `taboo` binary (`cmd/taboo/main.go`) registers these subcommands. Full flag
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
  [design](docs/explanation/design.md).

## Design and decisions

The library is the primary contract and the CLI is a thin consumer of it. All
host side effects pass through a single `Commander` seam (`pkg/taboo/commander.go`),
so tests substitute a fake while `NewExecCommander` shells out in production.
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

## Status

The library is feature-complete and tested. A run produces a named, isolated
branch per worktree, driven by `Runner`, `Orchestrator`, or `Pool`. Three agents
are supported: `opencode`, `claude-code`, and `copilot`. The CLI covers `init`,
`run`, `validate`, `doctor`, `list`, and `clean`; its `run` drives the iteration
loop through `--iterations` and `--signal`. Fan-out, typed structured output,
and lifecycle hooks are available through the Go API only. The module is
pre-`v0.1.0` until a release is tagged.

## License

MIT. Copyright (c) 2026 Jose Ignacio Cabral Farre. See [LICENSE](LICENSE).
