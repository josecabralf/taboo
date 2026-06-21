# Iterate until the agent signals done

Re-run an agent in one worktree until it emits a completion signal or a maximum
iteration count is reached, then read why the loop stopped.

Use a looped run when a single agent pass is not enough: a fix that needs a
second look at the test output, a refactor that converges over a few passes.
taboo prepares the worktree once and re-execs the agent into it, so each pass
continues from the previous pass's commit.

!!! abstract "What you'll do"
    Set `max-iterations` and a `completion-signal` in `taboo.yaml`, run the loop
    from Go, then read `StopReason` and `Iterations` off the result to learn
    whether the agent signalled done or the loop hit its cap.

## How the loop works

A run loops when its resolved plan has a `MaxIterations` above 1. taboo prepares
the worktree once, then re-runs the agent into that same worktree. Every
iteration shares one worktree and the agent commits in place, so each pass
continues from the previous pass's commit.

The loop stops on the first of two conditions:

| Condition | `StopReason` | String value |
| --- | --- | --- |
| The agent's stdout contains the completion signal | `StopSignal` | `"signal"` |
| The loop has run `MaxIterations` times | `StopMaxIterations` | `"max-iterations"` |

`MaxIterations` below 1 means a single run. An empty completion signal disables
the early stop, so the loop always runs the full `MaxIterations`.

## Configure the loop in taboo.yaml

The loop knobs live in your `taboo.yaml`, so the one-call bridge picks them up
without extra Go code. `max-iterations` can sit on a workflow or in `defaults`;
`completion-signal` is a `defaults`-only key:

```yaml
workshop: demo
base: ubuntu@24.04
repo: /home/me/repos/demo
agent: opencode
model: openrouter/qwen/qwen3-coder-plus
defaults:
  completion-signal: DONE
workflows:
  iterate:
    prompt: "Fix the failing tests. Print DONE when all tests pass."
    max-iterations: 5
```

!!! warning "`completion-signal` is `defaults`-only"
    `taboo.yaml` is parsed with unknown keys rejected. A workflow has no
    `completion-signal` field, so putting it under `workflows.iterate` makes
    `LoadConfig` fail with `ErrConfigParse`. Set it under `defaults`.

`RunWorkflow` locates this config above the start directory, resolves the
`iterate` workflow into a plan, and runs the loop over a `Commander`:

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
		"/home/me/repos/demo", // start dir; taboo.yaml is found above it
		"iterate",             // workflow name
		nil,                   // template vars for {{VAR}} placeholders
		taboo.PlanOverrides{Branch: "taboo/iterate"},
		taboo.NewExecCommander(),
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("stopped after %d iteration(s): %s\n", res.Iterations, res.StopReason)
	fmt.Printf("branch %s at %s\n", res.Branch, res.Commit)
}
```

!!! note "Prerequisite"
    This program needs a workshop host: `RunWorkflow` launches a workshop and
    execs the agent. The rest of the page describes the values it returns.

`PlanOverrides` is the per-call override layer: a non-zero field wins over the
config. To raise the iteration count for one call without touching `taboo.yaml`,
set `taboo.PlanOverrides{Branch: "taboo/iterate", MaxIterations: 8}`.

## Inspect the plan before running

When you want to see or adjust the resolved loop before it runs, load the config,
resolve a `Plan`, then call `Run`. The plan is a pure, inspectable description of
one run; `Plan.Request` is the `OrchestratedRequest` the loop will execute.

```go
cfg, err := taboo.LoadConfig("/home/me/repos/demo/.taboo/taboo.yaml")
if err != nil {
	log.Fatal(err)
}

plan, err := cfg.Plan("/home/me/repos/demo", "iterate", nil, taboo.PlanOverrides{Branch: "taboo/iterate"})
if err != nil {
	log.Fatal(err)
}

// Inspect or tweak the resolved loop before it runs.
plan.Request.MaxIterations = 8

res, err := plan.Run(context.Background(), taboo.NewExecCommander())
if err != nil {
	log.Fatal(err)
}
fmt.Printf("stopped after %d iteration(s): %s\n", res.Iterations, res.StopReason)
```

## Read the result

Both `RunWorkflow` and `Plan.Run` return an `OrchestratedResult`. It embeds
`RunResult` (the final iteration's `Branch`, `Commit`, `Output`)
and adds three fields:

- `Iterations` is how many times the agent ran.
- `StopReason` is `StopSignal` or `StopMaxIterations`. It is only meaningful when
  the call returns a nil error. On a setup or exec failure, the call returns the
  partial result with the error and leaves `StopReason` at its zero value, so
  check `err` before reading `StopReason`.
- `Result` holds a decoded typed value when a result extractor is set, otherwise
  nil. See below.

The final `Commit` is the branch HEAD after the last iteration. Because every
iteration commits in place, the commit reflects all passes, not just the last
one.

## Decode a typed result after the loop

Use `RunWorkflowAs[T]` to pull a structured value out of the final iteration's
output. It runs the same locate-load-plan-run pipeline as `RunWorkflow`, but
threads a `JSONResult[T]` extractor into the plan so the agent's structured
output is decoded in-loop and returned as a statically typed `T`, with no caller
assertion.

```go
type review struct {
	Passed bool   `json:"passed"`
	Notes  string `json:"notes"`
}

got, res, err := taboo.RunWorkflowAs[review](
	context.Background(),
	"/home/me/repos/demo",
	"iterate",
	nil,
	taboo.PlanOverrides{Branch: "taboo/review"},
	taboo.NewExecCommander(),
)
if err != nil {
	log.Fatal(err)
}
fmt.Println(got.Passed, got.Notes)
```

The agent prints a `<result>{"passed":true,"notes":"..."}</result>` block in its
output and the extractor decodes it. `res.Result` carries the same value as
`any`; `got` is already typed. For the block convention, strict fields, and the
`Validator` interface, see [Get a typed result out of a run](typed-results.md).

Extraction failure is reported through the error, but the result stays populated:
`Branch`, `Commit`, `Output`, `Iterations`, and `StopReason` survive so a failed
decode never discards the agent's commit. A failed extraction returns
`ErrNoResult` or `ErrInvalidResult`, and `got` is the zero `T`.

To set an extractor on the inspect-then-run path instead, assign one to
`plan.Request.ResultExtractor` before calling `Plan.Run`, then type-assert
`res.Result`.

## Do not combine fork with a loop

`RunRequest` has a `Fork` field that forks a resumed session. A looped run
re-execs the same request every iteration, so a fork would re-fork the source
session on every pass rather than continuing one fork.

!!! warning "Fork plus a loop is rejected"
    A plan whose `Fork` is set and `MaxIterations` is greater than 1 returns
    `ErrForkLoop` before any setup runs — taboo rejects the combination up
    front, so no workshop or worktree is provisioned.

A single-iteration fork (`MaxIterations` at or below 1) is allowed, and a
multi-iteration plain resume (`ResumeSession` set, `Fork` false) is allowed.

## See also

- [Get a typed result out of a run](typed-results.md) for the `<result>` block
  and `JSONResult[T]`.
- [Library API reference](../reference/library-api.md) for the exact signatures
  of `RunWorkflow`, `Plan`, `OrchestratedRequest`, and `OrchestratedResult`.
