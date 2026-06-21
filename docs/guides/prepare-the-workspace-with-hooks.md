# Prepare the workspace with hooks

Run setup commands after the workshop starts and before the agent runs, for
example `go mod download` or installing a linter the agent will call.

Use hooks when the agent needs a prepared environment: dependencies fetched, a
tool installed, a fixture in place. The hook runs in the same worktree the agent
sees, so its effects are visible to the agent.

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

This program needs a workshop host: `plan.Run` launches a workshop and runs the
hook inside it.

## When and how often hooks run

`OnWorkshopReady` hooks run after the workshop starts with the run's worktree
mounted, and before the agent execs. They run on every run, not once per
workshop, because the worktree is swapped in per run. Keep them idempotent and
cheap. A hook like `go mod download` is safe to repeat; do not model expensive
one-time provisioning here.

Hooks run in order. A hook failure stops the sequence and fails the run before
the agent execs, with an error naming the offending hook (`hook <i> <command>`).
A `Hook` with an empty `Command` is skipped.

Hook output (stdout and stderr) goes to the run's `Stderr` writer, so set
`RunRequest.Stderr` to see what a hook printed when it fails.

## Host hooks versus in-workshop hooks

`InWorkshop` decides where the command runs:

- `InWorkshop: false` (the default) runs the command on the host, in the run's
  worktree directory. Use this for host-side preparation that writes into the
  worktree before it is handed to the agent.
- `InWorkshop: true` runs the command inside the workshop through
  `workshop exec`, with the working directory `/taboo/workspace`. The in-workshop hook
  sees the same mounts and the agent's credential env keys, so it runs with the
  agent's environment. Use this for commands that need the workshop's toolchain,
  for example fetching dependencies into the in-workshop module cache the agent
  will use.

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
