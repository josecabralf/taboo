# Drive one agent run from Go

!!! abstract "What you'll build"
    A small Go program that calls `taboo.RunWorkflow` once to run an AI coding agent inside a workshop, ending with the agent's commit on a fresh branch in your repo. Every step is concrete and runs in order.

By the end of this tutorial you will have written a small Go program that runs one AI coding agent inside a workshop, and you will see the commit it made on a fresh host branch. taboo runs the agent in an isolated workshop with your git worktree bind-mounted in, so the agent commits in place and its commit lands directly on the branch. No extraction or sync step. For why that works, read [the isolation model](../explanation/isolation-model.md) after you finish here.

This tutorial uses the `opencode` agent. The Go program calls one function, `taboo.RunWorkflow`, the library's one-call bridge: it locates a `taboo.yaml`, resolves a workflow into a run, and executes it.

## Before you start

This is a one-time setup of a few minutes; the interesting part, one Go call, is two short sections away. You need a host that can launch a workshop and the `taboo` CLI installed (see the [CLI first run](cli-first-run.md) for `go install`). The CLI scaffolds the `taboo.yaml` the library reads. With it installed, confirm the host is ready:

```sh
taboo doctor
```

`doctor` checks for the `workshop` snap (version `0.9.1` or newer), LXD, and `git`, and warns (without failing) if the Go toolchain is missing. Every line should read `[ok]` or `[warn]`; an `[error]` line blocks the run until you fix it. You can also check the same tools by hand:

```sh
workshop --version
lxc version
git --version
```

Install the missing ones:

```sh
sudo snap install workshop
sudo snap install lxd
```

You also need a git repository on persistent storage. Do not put it under `/tmp` or `/run`: those paths are tmpfs and the mount taboo relies on silently fails there. Put the repo under `$HOME`. Use an existing repo or create one:

```sh
mkdir -p "$HOME/demo-repo"
cd "$HOME/demo-repo"
git init
git config user.email you@example.com   # skip if your global git identity is set
git config user.name "You"
git commit --allow-empty -m "initial commit"
```

The repository must be a *workshop project*: it needs a `workshop.yaml` at its root naming the workshop and its base toolchain. taboo derives the agent's workshop from that definition (that is what lets the agent run, and commit, inside an isolated sandbox built from your project's own tools), so `taboo init` refuses a repo without one. Write a minimal one:

```yaml title="$HOME/demo-repo/workshop.yaml"
name: demo-repo
base: ubuntu@24.04
sdks:
    - name: go
      channel: 1.26/stable
```

Finally, the agent needs a credential. OpenCode authenticates with an OpenRouter key; create one at [openrouter.ai](https://openrouter.ai) if you do not have it (free models exist for a first run). It reads `OPENROUTER_API_KEY` from the environment (see [the agents reference](../reference/agents.md)). Export it in the shell you will run the program from:

```sh
export OPENROUTER_API_KEY=your-openrouter-key
```

Confirm it is set, without printing the key: `test -n "$OPENROUTER_API_KEY" && echo "key set"`. taboo forwards this value into the workshop per run and never writes it to disk.

## Scaffold a taboo.yaml

`RunWorkflow` reads a `taboo.yaml` for the workshop, agent, model, and repo, so scaffold one into the demo repo. The `taboo` CLI does this in one step (see [the CLI first run](cli-first-run.md) for the binary):

```sh
cd "$HOME/demo-repo"
taboo init --agent opencode --model openrouter/qwen/qwen3-coder-plus
```

The model string is `<provider>/<model>`, here OpenRouter's `qwen3-coder-plus`. This writes `.taboo/taboo.yaml` (plus `.gitignore` and `.env.example`) and never launches a workshop. Confirm it landed with `ls .taboo/`. The generated config records the repo path and the agent the bridge will run; you do not repeat any of it in Go.

## Write the program

Create a new directory for the program and initialize a module:

```sh
mkdir -p "$HOME/taboo-demo"
cd "$HOME/taboo-demo"
go mod init taboo-demo
```

Write `main.go`:

```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/josecabralf/taboo"
)

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	res, err := taboo.RunWorkflow(
		ctx,
		os.Getenv("TABOO_START_DIR"), // your demo repo; .taboo/taboo.yaml is found from here
		"",                    // no named workflow: this is an ad-hoc, prompt-only run
		nil,                   // template vars for {{VAR}} placeholders
		taboo.PlanOverrides{
			Branch: "taboo/first-run",
			Prompt: "Add a one-line note to README.md describing this repository, then commit your work.",
			Stdout: os.Stderr,
			Stderr: os.Stderr,
		},
		taboo.NewExecCommander(),
	)
	if err != nil {
		return err
	}

	fmt.Printf("branch: %s\ncommit: %s\n", res.Branch, res.Commit)
	return nil
}
```

The second argument is the absolute path to the git repository you scaffolded. The program reads it from the `TABOO_START_DIR` environment variable, so there is nothing to hand-edit; you set that variable in the next step. `RunWorkflow` ascends from that directory to find the `.taboo/taboo.yaml`, which supplies the workshop, agent, model, and repo path.

The `Branch` and `Prompt` overrides name this run's branch and instruction; the empty workflow name makes it an ad-hoc, prompt-only run. `Stdout` and `Stderr` are set to `os.Stderr` so you watch the agent work live while the program runs.

!!! note "Where the agent and model come from"
    The `PlanOverrides` here name only the branch and the prompt. The agent (`opencode`), model, and repo path come from the `.taboo/taboo.yaml` you scaffolded; `RunWorkflow` finds it by ascending from the start dir. To use a different agent, re-run `taboo init --agent claude-code ...` rather than touching this Go. The [library API reference](../reference/library-api.md) lists every `PlanOverrides` field and the precedence rules.

Fetch the dependency and confirm the program compiles before the costly run:

```sh
go mod tidy
go build ./...   # prints nothing and exits 0 if it compiles
```

## Run it

!!! warning "This step launches a real workshop"
    `RunWorkflow` starts a workshop and runs the agent for real, so run it on a workshop host. The first run launches the workshop, which takes minutes; later runs reuse it.

```sh
export TABOO_START_DIR="$HOME/demo-repo"
go run .
```

The agent's live output streams to your terminal. When it finishes, the program prints two lines:

```
branch: taboo/first-run
commit: <40-character commit hash>
```

The `commit` value is the branch HEAD after the agent ran. taboo reads it with `git rev-parse HEAD` against the run's worktree (the linked worktree taboo created for this run) once the agent finishes.

## See the commit on the branch

The agent committed in place through the bind-mount, so the commit is already on the branch in your host repository. Look at it:

```sh
git -C "$HOME/demo-repo" log --oneline taboo/first-run
```

You should see the agent's commit on top, something like:

```
1f3c9ab Add a one-line note to README.md describing this repository
```

The short hash matches the first seven characters of the `commit:` line your program printed. The branch is named `taboo/first-run`, the value you passed as `PlanOverrides.Branch`. Nothing was pushed: taboo denies `git push` from inside the workshop, so integration is yours to do from the host.

The worktree and branch stay on disk after the run; taboo does not reap them. Re-running reuses the workshop. To tear everything down run `taboo clean` from the repo, or call `res.Dispose()` to remove just this run's worktree.

## Next steps

[Library API reference](../reference/library-api.md){ .md-button .md-button--primary } [Iterate until done](../guides/iterate-until-done.md){ .md-button }

**Read this next:** the `RunWorkflow` and `PlanOverrides` entries in the [library API reference](../reference/library-api.md). They list every field you can set on the call you just made.

- To re-run the agent until it signals it is done, read [iterate until done](../guides/iterate-until-done.md).
- To run many prompts in parallel, read [fan out runs](../guides/fan-out-runs.md).
- To get a typed, validated result back from the agent, read [typed results](../guides/typed-results.md).
- For the full exported surface, see the [library API reference](../reference/library-api.md).
- For why commits land on the host branch with no sync step, read [the isolation model](../explanation/isolation-model.md).
