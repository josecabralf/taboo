# The isolation model

taboo runs an AI coding agent inside a Canonical workshop and lets it commit
straight onto a host git branch, with no copy-out or sync step afterward. That
property comes from a specific arrangement of git worktrees and bind-mounts. This
page explains why the arrangement is shaped the way it is. For the steps to drive
a run, see the [library](../tutorials/library-first-run.md) and
[CLI](../tutorials/cli-first-run.md) tutorials.

## What a workshop is

A workshop is an LXD-backed development environment defined declaratively and
provisioned by the `workshop` snap. taboo does not import workshop's Go client or
speak its REST socket; it shells out to the `workshop` binary, driving everything
through `Cmd{Name: "workshop", ...}` over the `Commander` seam in
`pkg/taboo/commander.go`. The verbs it uses are `launch`, `stop`, `start`,
`info`, `remount`, and `exec`, assembled in `pkg/taboo/workshop.go`.

A workshop is expensive to stand up. The LXD container has to be created and the
agent CLI installed into its rootfs, which takes minutes. That cost shapes the
rest of the model: taboo provisions a workshop once and reuses it across many
runs rather than creating one per run. `Runner.ensureWorkshop` in
`pkg/taboo/runner.go` probes with `workshop info` and only launches when the
probe fails, so the second run onward reuses the existing workshop.

## Why two mounts, not one

The agent works inside a fresh git worktree, one per run, created with
`git worktree add -b <branch>` (`Runner.Setup` in `pkg/taboo/runner.go`). A
linked worktree is not self-contained. Its `.git` entry is a pointer file of the
form `gitdir: <repo>/.git/worktrees/<name>`, and its objects and refs live in the
main repository's `.git`. Mounting only the worktree's working directory into the
workshop gives the agent a directory whose `.git` pointer dangles, and git
reports `fatal: not a git repository`.

So taboo bind-mounts two things into the workshop:

1. the run's worktree, at the fixed target `/workspace` (the `workspaceTarget`
   constant in `pkg/taboo/template.go`);
2. the repository's main `.git`, the git common directory, at its identical host
   absolute path inside the workshop.

The second mount is the load-bearing one. `gitCommonTarget` in
`pkg/taboo/template.go` returns `filepath.Join(repoPath, ".git")` and that value
is used both as the mount target baked into the workshop definition and as the
remount source, so the host `.git` path and the in-workshop `.git` path are the
same string.

## Why identical-path `.git` matters

Because the common directory is mounted at the same absolute path on both sides,
the worktree's `.git` pointer resolves to the same place whether git reads it from
the host or from inside the workshop. The agent commits in place, the commit's
objects and the branch ref land in the shared `.git`, and the branch HEAD is
visible to host-side git immediately. `Runner.Exec` reads it back with
`git -C <worktree> rev-parse HEAD` and records it on `RunResult.Commit`.

The alternative is to rewrite the worktree's `.git` pointer to a fixed in-workshop
path such as `/gitcommon`. That also lets the in-workshop commit succeed, but it
breaks the worktree for host-side git, because the host would then read a pointer
aimed at a path that does not exist outside the container. Keeping the path
identical avoids any rewriting and keeps the worktree valid on both sides at once.
ADR 0007 ([nested worktree placement](../adr/0007-nested-worktree-placement.md))
records the integration test that confirmed the commit lands on the host branch
and the worktree's `.git` pointer still resolves for host-side git afterward.

## The `/tmp` trap

The identical-path rule has a consequence that surprises people: the managed
repository cannot live under `/tmp` or `/run`. A mount target that resolves to one
of those paths lands on volatile tmpfs inside the workshop, and the mount silently
fails. Because the git-common target equals the host `.git` path, a repository at,
say, `/tmp/work/repo` would try to mount `.git` at `/tmp/work/repo/.git` inside the
container, hit tmpfs, and leave the worktree's `.git` pointer unresolvable
(`fatal: not a git repository` again).

The fix is to keep the repository on persistent storage, for example under `$HOME`.
This is not a soft recommendation; `validate` and `doctor` enforce it and refuse a
repository under `/tmp` rather than letting a run fail opaquely later. The
constraint is a property of the git-common mount, not of where the worktree sits,
so it holds for every layout.

## Why the per-run swap is cheap

Each run needs a different worktree mounted at `/workspace`, but the workshop is
long-lived and was launched once. So taboo repoints the existing mount rather than
relaunching. The mechanism is `workshop remount`, which points an existing mount
plug at a new host source.

A `remount` is only atomic when the new source is empty and on the same filesystem
as the current one. A worktree is a non-empty directory, so the swap cannot be
live. `Runner.Setup` does it as `stop`, then `remount` for `workspace` and
`gitcommon` (and `sessions` for a session-capable agent), then `start`. The
ordering is visible in the code: `verbArgs(proj, "stop", ws)`, the `remountArgs`
calls, then `verbArgs(proj, "start", ws)`.

This stop, remount, start, exec cycle takes seconds. The expensive work, creating
the container and installing the agent CLI, already happened at launch and is not
repeated. That is the whole point of reusing the workshop: the minutes-long launch
is paid once and amortized across every run that follows, while each run pays only
the seconds-long swap.

The swap has a second effect that explains why the agent must be baked in rather
than installed at runtime. A `stop` reprovisions the rootfs from the declared SDKs,
so anything installed ad hoc into the rootfs by a previous `exec` is wiped before
the next run. Only the bind-mounts survive. taboo therefore ships the agent CLI as
a workshop SDK embedded with `//go:embed sdk` in `pkg/taboo/runner.go` and seeds it
into the project on first run, so the agent is present in the rootfs every time the
workshop starts.

## One workshop per agent, one workshop per slot

A single workshop is pinned to one agent and, through the git-common mount, to one
repository. `WorkshopName(base, agent)` in `pkg/taboo/workshop.go` derives the name
as `base + "-" + agent`, so each distinct agent gets its own reused workshop. The
repository pinning is structural: the git-common target equals
`<repoPath>/.git`, and `remount` repoints a mount's source but cannot move its
target after launch, so a workshop launched for one repository cannot serve another
at a different absolute path without breaking the identical-path invariant.

Fan-out follows the same logic at the slot level. `Pool` in `pkg/taboo/pool.go`
gives each concurrency slot a distinct workshop named `"<Workshop>-<slot>"` under
its own project directory `"<ProjectDir>/slot-<slot>"`, so slots never collide on
definitions, worktrees, or session stores. Isolation is at the workshop level: each
slot reuses its own workshop across waves, and every request still gets its own
branch and worktree. Because all slots share the base repository, `Pool` serializes
`git worktree add` across them (worktree creation mutates the shared `.git`), while
the workshop swaps and agent execs run concurrently. The
[fan-out guide](../guides/fan-out-runs.md) covers the operational caveats, such as
not running `git gc` against the repository mid-run.

A warmer fan-out, where each slot starts from a clone of an already-provisioned
workshop, would save the per-slot provisioning cost, but it is deferred. ADR 0006
([defer warm-clone fan-out](../adr/0006-defer-warm-fanout-single-repo-workshops.md))
records that the `workshop` CLI exposes no clone or `launch --from` verb and
neither does its Go client, so the win is unreachable inside taboo's
shell-out-to-the-CLI contract until an upstream verb lands. The same ADR records
the decision to keep one workshop per repository rather than build multi-repo
reuse, because the per-repo git-common coupling makes the alternative impose a
single-host-parent layout constraint for a launch cost that is not yet measured to
justify it.

## Where worktrees live

A run's worktree path is derived by `Runner.worktreePath` as
`<ProjectDir>/worktrees/<branch>`, with slashes in the branch name replaced by
hyphens. The CLI sets `ProjectDir` to `<repo>/.taboo`, so worktrees nest at
`<repo>/.taboo/worktrees/<branch>`, inside the repository and git-ignored.

Nesting a worktree under the same repository whose `.git` is the second mount looks
risky, but it changes only the host path of the `/workspace` source. The two mounts
themselves are untouched: the worktree still goes to `/workspace`, and `<repo>/.git`
still mounts at `<repo>/.git`. The worktree's `.git` pointer references the common
directory, which is present through the git-common mount regardless of where the
worktree physically sits. ADR 0007 records that this nested arrangement was verified
on workshop 0.9.1 and LXD 6.8, and that the out-of-repo layout remains a sound
fallback. The only host-side cost the nested layout inherits is the
repo-not-under-`/tmp` constraint, which already existed for the git-common mount.

## Push is denied; the host owns integration

Agents run headless, with no interactive approver, and commit autonomously. Every
supported profile denies `git push` from inside the workshop by default (Claude
Code via `--disallowedTools "Bash(git push *)"`, Copilot via
`--deny-tool=shell(git push)`; see [agents.md](../reference/agents.md)). The deny is
deliberate. A linked worktree shares the host repository's object store and refs, so
a push from inside the workshop, forced or not, could mutate host branches directly.

taboo's contract is to commit in place and let the host own integration. The agent
never needs to push, because its commits are already on the host branch the moment
it makes them. A workflow that does need to publish a branch or open a pull request
adds an explicit push stage on the host side, after the run, rather than relying on
the agent to push from inside the workshop.

## See also

- [Why the API is shaped this way](design.md)
- [Library API reference](../reference/library-api.md)
- [ADR 0006: defer warm-clone fan-out, one workshop per repo](../adr/0006-defer-warm-fanout-single-repo-workshops.md)
- [ADR 0007: nested worktree placement](../adr/0007-nested-worktree-placement.md)
