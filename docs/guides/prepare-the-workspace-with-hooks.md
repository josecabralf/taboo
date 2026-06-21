# Prepare the workspace with hooks

Run setup commands after the workshop starts and before the agent runs, for
example `go mod download` or installing a linter the agent will call.

Use hooks when the agent needs a prepared environment: dependencies fetched, a
tool installed, a fixture in place. The hook runs in the same worktree the agent
sees, so its effects are visible to the agent.

!!! note "Before you start"
    This guide uses the inspect-then-run path, so it assumes you can already
    drive a run from Go. If you have not yet, work through
    [the library first run](../tutorials/library-first-run.md). You also need a
    `taboo.yaml` in the target repository and a workshop host installed
    (`plan.Run` launches a workshop).

## Attach a hook to a run

Hooks live on `RunRequest.Hooks`. The one hook point is `OnWorkshopReady`, a
slice of `Hook`. Each `Hook` has a `Command` (the executable and its arguments)
and an `InWorkshop` flag.

Set them through the inspect-then-run path: load the config, resolve a `Plan`, and
assign the hooks to `plan.Request.Hooks` before running. `plan.Request` is an
`OrchestratedRequest` that embeds `RunRequest`, so its `Hooks` field is the same
one. The `taboo.yaml` supplies the workshop, agent, and repo.

```go
package main

import (
	"context"
	"log"
	"os"

	"github.com/josecabralf/taboo"
)

func main() {
	cfg, err := taboo.LoadConfig("/home/me/repos/demo/.taboo/taboo.yaml")
	if err != nil {
		log.Fatal(err)
	}

	plan, err := cfg.Plan("/home/me/repos/demo", "fix", nil, taboo.PlanOverrides{
		Branch: "taboo/with-deps",
		Prompt: "Add a benchmark for the parser.",
		Stderr: os.Stderr,
	})
	if err != nil {
		log.Fatal(err)
	}

	plan.Request.Hooks = taboo.Hooks{
		OnWorkshopReady: []taboo.Hook{
			{Command: []string{"go", "mod", "download"}, InWorkshop: true},
		},
	}

	if _, err := plan.Run(context.Background(), taboo.NewExecCommander()); err != nil {
		log.Fatal(err)
	}
}
```

Here `InWorkshop: true` means `plan.Run` runs `go mod download` inside the
launched workshop, against the worktree it just mounted.

## When and how often hooks run

`OnWorkshopReady` hooks run at the end of `Setup`: after the workshop is started
with the run's worktree mounted, and before the agent first execs. Setup prepares
the worktree once, so the hooks fire **once per worktree**, not once per agent
iteration. A looped run (`MaxIterations > 1`) reuses the same worktree across
iterations and does not re-run the hooks between them.

The long-lived workshop is reused across separate runs, but each run mounts a
fresh worktree through Setup, so the hooks run again every time. Keep them
idempotent and cheap.

!!! warning "Hooks are not one-time provisioning"
    Because Setup runs on every fresh run, treat `OnWorkshopReady` as per-run
    preparation, not one-time setup. A hook like `go mod download` is safe to
    repeat; do not model expensive one-time provisioning here.

Hooks run in order. A hook failure stops the sequence and fails the run before
the agent execs; `Setup` returns an error of the form
`on-workshop-ready hook: hook <i> [<command>]: ...`, naming the offending hook by
its index and command (for example `hook 0 [go mod download]`). A `Hook` with an
empty `Command` is skipped.

Hook output (stdout and stderr) goes to the run's `Stderr` writer, so set
`Stderr` on the run — through `PlanOverrides{Stderr: ...}` when resolving the
plan, as the example does — to see what a hook printed when it fails.

## Host hooks versus in-workshop hooks

`InWorkshop` decides where the command runs:

| Aspect | `InWorkshop: false` (default) | `InWorkshop: true` |
| --- | --- | --- |
| Where it runs | On the host | Inside the workshop via `workshop exec` |
| Working directory | The run's worktree | `/taboo/workspace` |
| Credential env keys | No | Yes (the agent's) |
| Session-dir redirect | No | Yes |

- `InWorkshop: false` (the default) runs the command on the host, in the run's
  worktree directory. Use this for host-side preparation that writes into the
  worktree before it is handed to the agent.
- `InWorkshop: true` runs the command inside the workshop through
  `workshop exec`, with the working directory `/taboo/workspace`. The in-workshop
  hook sees the same mounts and the agent's credential env keys, so it runs with
  the agent's environment. Use this for commands that need the workshop's
  toolchain, for example fetching dependencies into the in-workshop module cache
  the agent will use.

A host hook does not get the credential env keys or the session-dir redirect,
because those are workshop paths. Pick `InWorkshop: true` when the command must
run in the same context as the agent.

## Timeout

The run's `Timeout` (carried on `plan.Request.Timeout`) bounds each hook the same
way it bounds the agent exec. A hook that hangs cannot stall the run past the
timeout. When `Timeout` is zero, hooks are unbounded. Set a timeout through
`PlanOverrides` when resolving the plan if a hook might hang:

```go
plan, err := cfg.Plan("/home/me/repos/demo", "fix", nil, taboo.PlanOverrides{
	Branch:  "taboo/with-deps",
	Prompt:  prompt,
	Timeout: 5 * time.Minute,
})
if err != nil {
	log.Fatal(err)
}

plan.Request.Hooks = taboo.Hooks{
	OnWorkshopReady: []taboo.Hook{
		{Command: []string{"go", "mod", "download"}, InWorkshop: true},
	},
}
```

## See also

- [Drive one agent run from Go](../tutorials/library-first-run.md) for the basic
  one-call `RunWorkflow` flow without hooks.
- [Library API reference](../reference/library-api.md) for the `Hook` and `Hooks`
  type definitions.
