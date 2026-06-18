# Drive one agent run from Go

By the end of this tutorial you will have written a small Go program that runs one AI coding agent inside a workshop, and you will see the commit it made on a fresh host branch. taboo runs the agent in an isolated workshop with your git worktree bind-mounted in, so the agent commits in place and its commit lands directly on the branch. No extraction or sync step. For why that works, read [the isolation model](../explanation/isolation-model.md) after you finish here.

This tutorial uses the `opencode` agent. The Go pieces (`Config`, `New`, `RunRequest`, `Run`) come from `pkg/taboo/runner.go` and `pkg/taboo/template.go`.

## Before you start

You need a host that can launch a workshop. Confirm the host is ready. If you have the `taboo` CLI installed (see the [CLI first run](cli-first-run.md)), run:

```sh
taboo doctor
```

`doctor` checks for the `workshop` snap (version `0.9.1` or newer), LXD, and `git`. Without the CLI, check the same tools by hand:

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

Finally, the agent needs a credential. OpenCode reads `OPENROUTER_API_KEY` from the environment (`pkg/taboo/agent_opencode.go`). Export it in the shell you will run the program from:

```sh
export OPENROUTER_API_KEY=your-openrouter-key
```

taboo forwards this value into the workshop per run and never writes it to disk.

## Write the program

Create a new directory for the program and initialize a module:

```sh
mkdir -p "$HOME/taboo-demo"
git -C "$HOME/demo-repo" rev-parse --show-toplevel  # confirm the repo path
cd "$HOME/taboo-demo"
go mod init taboo-demo
go get github.com/josecabralf/taboo/pkg/taboo
```

Write `main.go`:

```go
package main

import (
	"context"
	"fmt"
	"os"

	taboo "github.com/josecabralf/taboo/pkg/taboo"
)

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg := taboo.Config{
		Workshop:   "taboo-demo-opencode",
		Base:       "ubuntu@24.04",
		Agent:      taboo.OpenCode("openrouter/qwen/qwen3-coder-plus"),
		RepoPath:   "/home/you/demo-repo",
		ProjectDir: "/home/you/taboo-demo/.taboo",
	}

	runner := taboo.New(cfg, taboo.NewExecCommander())

	res, err := runner.Run(ctx, taboo.RunRequest{
		Branch: "taboo/first-run",
		Prompt: "Add a one-line note to README.md describing this repository, then commit your work.",
		Stdout: os.Stderr,
		Stderr: os.Stderr,
	})
	if err != nil {
		return err
	}

	fmt.Printf("branch: %s\ncommit: %s\n", res.Branch, res.Commit)
	return nil
}
```

Replace the two absolute paths before you build:

- `RepoPath` is the absolute path to the git repository the agent works on. Set it to the output of the `git rev-parse --show-toplevel` command above (for example `/home/you/demo-repo`).
- `ProjectDir` is a host directory taboo owns: it holds the rendered workshop definition and the worktrees taboo creates. A `.taboo` directory beside your program is fine.

`Stdout` and `Stderr` are set to `os.Stderr` so you watch the agent work live while the program runs.

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

The `commit` value is the branch HEAD after the agent ran. taboo reads it with `git rev-parse HEAD` against the host worktree (`pkg/taboo/runner.go`, the `Exec` method).

## See the commit on the branch

The agent committed in place through the bind-mount, so the commit is already on the branch in your host repository. Look at it:

```sh
git -C "$HOME/demo-repo" log --oneline taboo/first-run
```

The top entry is the commit your program printed. The branch is named `taboo/first-run`, the value you passed as `RunRequest.Branch`. Nothing was pushed: taboo denies `git push` from inside the workshop, so integration is yours to do from the host.

## Where to go next

- To re-run the agent until it signals it is done, read [iterate until done](../guides/iterate-until-done.md).
- To run many prompts in parallel, read [fan out runs](../guides/fan-out-runs.md).
- To get a typed, validated result back from the agent, read [typed results](../guides/typed-results.md).
- For the full exported surface, see the [library API reference](../reference/library-api.md).
- For why commits land on the host branch with no sync step, read [the isolation model](../explanation/isolation-model.md).
