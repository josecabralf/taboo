# Run many prompts in parallel

Dispatch a batch of agent runs across a bounded set of workshops and collect one
result per request.

Use `Pool` when you have several independent prompts to run against the same
repo: try three approaches to one bug on three branches, or run the same workflow
over a list of files. A single-call run handles one prompt at a time; `Pool` fans
the work out across concurrency slots.

## Build a pool and run a batch

`NewPool` takes a `Config`, a concurrency `limit`, and a `Commander`. `Run` takes
a slice of `RunRequest` and returns a slice of `RunResult`. The `Config` is the
workshop runner input: build one directly, or take it from a resolved plan's
`Config` field (`plan.Config`, from `(*ProjectConfig).Plan`) to reuse your
`taboo.yaml` settings.

```go
package main

import (
	"context"
	"fmt"
	"log"

	taboo "github.com/josecabralf/taboo/pkg"
)

func main() {
	agent, err := taboo.NewProfile("opencode", "openrouter/qwen/qwen3-coder-plus")
	if err != nil {
		log.Fatal(err)
	}
	cfg := taboo.Config{
		Workshop:   "demo",
		Base:       "ubuntu@24.04",
		Agent:      agent,
		RepoPath:   "/home/me/repos/demo",
		ProjectDir: "/home/me/repos/demo/.taboo",
	}
	pool := taboo.NewPool(cfg, 4, taboo.NewExecCommander())

	reqs := []taboo.RunRequest{
		{Branch: "taboo/fix-a", Prompt: "Fix bug A."},
		{Branch: "taboo/fix-b", Prompt: "Fix bug B."},
		{Branch: "taboo/fix-c", Prompt: "Fix bug C."},
	}

	results, err := pool.Run(context.Background(), reqs)
	if err != nil {
		log.Fatal(err)
	}
	for i, res := range results {
		if res.Err != nil {
			fmt.Printf("run %d (%s) failed: %v\n", i, res.Branch, res.Err)
			continue
		}
		fmt.Printf("run %d: branch %s at %s\n", i, res.Branch, res.Commit)
	}
}
```

This program needs a workshop host: `pool.Run` launches workshops and execs
agents. The rest of the page describes its behaviour.

## Results come back in input order

`results[i]` corresponds to `reqs[i]`. The pool runs requests concurrently but
returns them in the order they were submitted, so you can match each result to
its request by index without tracking branch names.

## A failed run does not abort the batch

A run that fails has its error recorded on `results[i].Err`, and the remaining
runs proceed. Check `Err` on each result rather than the batch error.

The batch error from `Run` is non-nil only when the batch cannot start at all,
which happens when the context is already canceled at entry. Per-run failures
never surface there.

If the context is canceled after the batch starts, in-flight runs finish on their
own and queued runs are skipped. Each skipped run's result carries its `Branch`
and `ctx.Err()` on `Err`, with no workshop or git commands issued for it. The
batch error stays nil; cancellation shows up per run like any other failure.

## One workshop per slot

`limit` bounds both the number of concurrent workshops and the number of worker
goroutines. Each concurrency slot owns a distinct workshop named
`"<Workshop>-<slot>"` under its own project directory `"<ProjectDir>/slot-<slot>"`,
so a slot's rendered definition, worktrees, and session store never collide with
another slot's.

A slot processes its queued requests in sequence and reuses its workshop across
waves, so the launch cost is paid once per slot. With more requests than the
limit, requests queue and run in waves. Every request still gets its own branch
and worktree, so concurrent runs never touch each other's files. Isolation is at
the workshop level. See [The isolation model](../explanation/isolation-model.md)
for how workshops and worktrees fit together.

## Constraints on the shared repo

All slots share the base `RepoPath`. The two-mount rule pins the gitcommon mount
to the host `.git`, so the pool serializes `git worktree add` across slots. Other
commands (workshop swaps, agent exec) still run concurrently. Concurrent commits
to distinct branches are safe because refs are per-worktree and the object store
is append-only.

Two rules hold while a batch is in flight:

- Do not run `git gc`, `git repack`, or `git prune` against `RepoPath`. They
  rewrite the object store the in-flight runs commit into.
- Do not call `Run` on the same `Pool` concurrently. Overlapping calls would
  collide on the same slot directories and workshop names. Serialize `Run` calls
  per `Pool` instance.

`Config.Agent` must be safe for concurrent use. The built-in profiles returned by
`NewProfile` are immutable values and meet this.

## Choosing the limit

`limit` is the number of concurrent workshops. A `limit` below 1 is treated as 1.
When the limit exceeds the number of requests, the pool starts only as many
workers as there are requests.

Each workshop is an LXD instance, so the ceiling is the host's memory and CPU, not
a fixed number. Start with a small limit (the example uses 4) and raise it while
the host keeps up. There is no warm-clone fan-out: every slot launches its own
workshop on its first request. See
[Defer warm-clone fan-out](../adr/0006-defer-warm-fanout-single-repo-workshops.md)
for the reasoning.

## See also

- [The isolation model](../explanation/isolation-model.md) for the two-mount rule
  and one-workshop-per-slot model.
- [Library API reference](../reference/library-api.md) for the signatures of
  `NewPool`, `Pool.Run`, and `RunResult`.
