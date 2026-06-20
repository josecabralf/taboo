# Drive one agent run from Go

By the end of this tutorial you will have written a small Go program that runs one AI coding agent inside a workshop, and you will see the commit it made on a fresh host branch. taboo runs the agent in an isolated workshop with your git worktree bind-mounted in, so the agent commits in place and its commit lands directly on the branch. No extraction or sync step. For why that works, read [the isolation model](../explanation/isolation-model.md) after you finish here.

This tutorial uses the `opencode` agent. The Go program calls one function, `taboo.RunWorkflow`, the library's one-call bridge: it locates a `taboo.yaml`, resolves a workflow into a run, and executes it.

## Before you start

You need a host that can launch a workshop, and the `taboo` CLI installed (see the [CLI first run](cli-first-run.md) for `go install`). The CLI scaffolds the `taboo.yaml` the library reads. With it installed, confirm the host is ready:

```sh
taboo doctor
```

`doctor` checks for the `workshop` snap (version `0.9.1` or newer), LXD, and `git`. You can also check the same tools by hand:

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
git -C "$HOME/demo-repo" init
git -C "$HOME/demo-repo" commit --allow-empty -m "initial commit"
```

Finally, the agent needs a credential. OpenCode reads `OPENROUTER_API_KEY` from the environment (see [the agents reference](../reference/agents.md)). Export it in the shell you will run the program from:

```sh
export OPENROUTER_API_KEY=your-openrouter-key
```

taboo forwards this value into the workshop per run and never writes it to disk.

## Scaffold a taboo.yaml

`RunWorkflow` reads a `taboo.yaml` for the workshop, agent, model, and repo, so scaffold one into the demo repo. The `taboo` CLI does this in one step (see [the CLI first run](cli-first-run.md) for the binary):

```sh
cd "$HOME/demo-repo"
taboo init --agent opencode --model openrouter/qwen/qwen3-coder-plus
```

This writes `.taboo/taboo.yaml` (plus `.gitignore` and `.env.example`) and never launches a workshop. The generated config records the repo path and the agent the bridge will run; you do not repeat any of it in Go.

## Write the program

Create a new directory for the program and initialize a module:

```sh
mkdir -p "$HOME/taboo-demo"
cd "$HOME/taboo-demo"
go mod init taboo-demo
go get github.com/josecabralf/taboo/pkg
```

Write `main.go`:

```go
package main

import (
	"context"
	"fmt"
	"os"

	taboo "github.com/josecabralf/taboo/pkg"
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
		"/home/you/demo-repo", // start dir; the .taboo/taboo.yaml is found from here
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

Replace the start-directory path before you build:

- The second argument is the absolute path to the git repository you scaffolded. `RunWorkflow` ascends from it to find the `.taboo/taboo.yaml`, which supplies the workshop, agent, model, and repo path. Set it to the output of `git -C "$HOME/demo-repo" rev-parse --show-toplevel` (for example `/home/you/demo-repo`).

The `Branch` and `Prompt` overrides name this run's branch and instruction; the empty workflow name makes it an ad-hoc, prompt-only run. `Stdout` and `Stderr` are set to `os.Stderr` so you watch the agent work live while the program runs.

## Run it

This step launches a real workshop and runs the agent. It is untested here, verify on a workshop host. The first run launches the workshop, which takes minutes; later runs reuse it.

```sh
go run .
```

The agent's live output streams to your terminal. When it finishes, the program prints two lines:

```
branch: taboo/first-run
commit: <40-character commit hash>
```

The `commit` value is the branch HEAD after the agent ran. taboo reads it with `git rev-parse HEAD` against the host worktree once the agent finishes.

## See the commit on the branch

The agent committed in place through the bind-mount, so the commit is already on the branch in your host repository. Look at it:

```sh
git -C "$HOME/demo-repo" log --oneline taboo/first-run
```

The top entry is the commit your program printed. The branch is named `taboo/first-run`, the value you passed as `PlanOverrides.Branch`. Nothing was pushed: taboo denies `git push` from inside the workshop, so integration is yours to do from the host.

## Where to go next

- To re-run the agent until it signals it is done, read [iterate until done](../guides/iterate-until-done.md).
- To run many prompts in parallel, read [fan out runs](../guides/fan-out-runs.md).
- To get a typed, validated result back from the agent, read [typed results](../guides/typed-results.md).
- For the full exported surface, see the [library API reference](../reference/library-api.md).
- For why commits land on the host branch with no sync step, read [the isolation model](../explanation/isolation-model.md).
