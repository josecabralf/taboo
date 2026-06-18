# Iterate until the agent signals done

Re-run an agent in one worktree until it emits a completion signal or a maximum
iteration count is reached, then read why the loop stopped.

Use `Orchestrator` when a single agent pass is not enough: a fix that needs a
second look at the test output, a refactor that converges over a few passes. The
`Runner` runs the agent once; the `Orchestrator` wraps it in a loop.

## How the loop works

`Orchestrator` (`pkg/taboo/orchestrator.go`) prepares the worktree once with
`Runner.Setup`, then re-runs the agent with `Runner.Exec` into that same
worktree. Every iteration shares one worktree and the agent commits in place, so
each pass continues from the previous pass's commit.

The loop stops on the first of two conditions:

- The agent's stdout contains `CompletionSignal`. The loop stops early and
  `StopReason` is `StopSignal` (the string `"signal"`).
- The loop has run `MaxIterations` times. `StopReason` is `StopMaxIterations`
  (the string `"max-iterations"`).

`MaxIterations` below 1 means a single run. An empty `CompletionSignal` disables
the early stop, so the loop always runs the full `MaxIterations`.

## Build and run an orchestrated request

Construct an `Orchestrator` from a `Runner`, then call `Run` with an
`OrchestratedRequest`. `OrchestratedRequest` embeds `RunRequest`, so the
single-run fields (`Branch`, `Prompt`, `Timeout`, `Stdout`, `Stderr`) sit
alongside the loop knobs.

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
		Workshop:   "demo",
		Base:       "ubuntu@24.04",
		Agent:      taboo.OpenCode("openrouter/qwen/qwen3-coder-plus"),
		RepoPath:   "/home/me/repos/demo",
		ProjectDir: "/home/me/repos/demo/.taboo",
	}
	orch := taboo.NewOrchestrator(taboo.New(cfg, taboo.NewExecCommander()))

	res, err := orch.Run(context.Background(), taboo.OrchestratedRequest{
		RunRequest: taboo.RunRequest{
			Branch: "taboo/iterate",
			Prompt: "Fix the failing tests. Print DONE when all tests pass.",
			Stdout: os.Stdout,
			Stderr: os.Stderr,
		},
		MaxIterations:    5,
		CompletionSignal: "DONE",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("stopped after %d iteration(s): %s\n", res.Iterations, res.StopReason)
	fmt.Printf("branch %s at %s\n", res.Branch, res.Commit)
}
```

This program needs a workshop host: `orch.Run` launches a workshop and execs the
agent. The rest of the page describes the values it returns.

## Read the result

`Run` returns an `OrchestratedResult`. It embeds `RunResult` (the final
iteration's `Branch`, `WorktreePath`, `Commit`, `Output`) and adds three fields:

- `Iterations` is how many times the agent ran.
- `StopReason` is `StopSignal` or `StopMaxIterations`. It is only meaningful when
  `Run` returns a nil error. On a `Setup` or `Exec` failure, `Run` returns the
  partial result with the error and leaves `StopReason` at its zero value, so
  check `err` before reading `StopReason`.
- `Result` holds a decoded typed value when a `ResultExtractor` is set, otherwise
  nil. See below.

The final `Commit` is the branch HEAD after the last iteration. Because every
iteration commits in place, the commit reflects all passes, not just the last
one.

## Decode a typed result after the loop

Set `ResultExtractor` to pull a structured value out of the final iteration's
output. The orchestrator runs the extractor once after the loop ends, over the
last iteration's stdout, and records the value on `OrchestratedResult.Result` as
`any`. Type-assert it to your result type.

```go
type review struct {
	Passed bool   `json:"passed"`
	Notes  string `json:"notes"`
}

res, err := orch.Run(ctx, taboo.OrchestratedRequest{
	RunRequest:       taboo.RunRequest{Branch: "taboo/review", Prompt: prompt},
	MaxIterations:    3,
	CompletionSignal: "DONE",
	ResultExtractor:  taboo.JSONResult[review](),
})
if err != nil {
	log.Fatal(err)
}
r := res.Result.(review)
fmt.Println(r.Passed, r.Notes)
```

The agent prints a `<result>{"passed":true,"notes":"..."}</result>` block in its
output and the extractor decodes it. For the block convention, strict fields, and
the `Validator` interface, see [Get a typed result out of a run](typed-results.md).

Extraction failure is reported through the error, but the result stays populated:
`Branch`, `Commit`, `Output`, `Iterations`, and `StopReason` survive so a failed
decode never discards the agent's commit. A failed extraction returns
`ErrNoResult` or `ErrInvalidResult`.

## Do not combine fork with a loop

`RunRequest` has a `Fork` field that forks a resumed session. A looped run
re-execs the same `RunRequest` every iteration, so a fork would re-fork the
source session on every pass rather than continuing one fork. The orchestrator
rejects this before any `Setup`:

```go
res, err := orch.Run(ctx, taboo.OrchestratedRequest{
	RunRequest:    taboo.RunRequest{ResumeSession: "abc", Fork: true},
	MaxIterations: 3,
})
// err is taboo.ErrForkLoop; no workshop is launched.
```

`Run` returns `ErrForkLoop` when `Fork` is set and `MaxIterations` is greater
than 1. A single-iteration fork (`MaxIterations` at or below 1) is allowed, and a
multi-iteration plain resume (`ResumeSession` set, `Fork` false) is allowed.

## See also

- [Get a typed result out of a run](typed-results.md) for the `<result>` block
  and `JSONResult[T]`.
- [Library API reference](../reference/library-api.md) for the exact signatures
  of `Orchestrator`, `OrchestratedRequest`, and `OrchestratedResult`.
