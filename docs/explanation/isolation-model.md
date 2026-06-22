# The isolation model

taboo runs an AI coding agent inside a Canonical workshop and lets it commit
straight onto a host git branch, with no copy-out or sync step afterward. That
property comes from a specific arrangement of git worktrees and bind-mounts. This
page explains why the arrangement is shaped the way it is. For the steps to drive
a run, see the [library](../tutorials/library-first-run.md) and
[CLI](../tutorials/cli-first-run.md) tutorials.

!!! note "Assumed knowledge"
    This page assumes you know what a git *linked worktree* is (`git worktree
    add`) and that its `.git` is a pointer into the main repository, not a full
    repository of its own. New to worktrees? Read `git help worktree` first.

!!! abstract "The one idea"
    A linked worktree's `.git` is a pointer file, not a repository. Everything on
    this page follows from one rule: mount **both** the worktree and the main
    `.git`, and mount `.git` at the **same absolute path** inside the workshop as
    on the host. Call it the **identical-path rule**. The `/tmp` trap, one
    workshop per repo, and nested worktree placement are all consequences of it.

## What a workshop is

A workshop is an LXD-backed development environment defined declaratively and
provisioned by the `workshop` snap. taboo does not import workshop's Go client or
speak its REST socket; it shells out to the `workshop` binary, driving everything
through `Cmd{Name: "workshop", ...}` over the `Commander` seam in
`internal/exec/commander.go`. On the run path the verbs it uses are `info`,
`launch`, `refresh`, `stop`, `remount`, `start`, and `exec`, assembled in
`internal/workshop/workshop.go`; `clean` adds `remove`.

A workshop is expensive to stand up. The LXD container has to be created and the
agent CLI installed into its rootfs, which takes minutes. That cost shapes the
rest of the model: taboo provisions a workshop once and reuses it across many
runs rather than creating one per run. `Runner.ensureWorkshop` in
`internal/run/runner.go` probes with `workshop info` and only launches when the
probe fails, so the second run onward reuses the existing workshop. Reuse is
guarded: taboo fingerprints the derived workshop definition (a sha256, persisted
at `<ProjectDir>/workshop.fingerprint`) and `refresh`es or relaunches the
workshop when the project's `workshop.yaml` drifts, reusing it untouched otherwise.

## Why two mounts, not one

The agent works inside a fresh git worktree, one per run, created with
`git worktree add -b <branch>` (`Runner.Setup` in `internal/run/runner.go`). A
linked worktree is not self-contained. Its `.git` entry is a pointer file of the
form `gitdir: <repo>/.git/worktrees/<name>`, and its objects and refs live in the
main repository's `.git`. Mounting only the worktree's working directory into the
workshop gives the agent a directory whose `.git` pointer dangles, and git
reports `fatal: not a git repository`.

!!! tip "Observe it"
    Run `cat <worktree>/.git` on any linked worktree: you get a single line,
    `gitdir: <repo>/.git/worktrees/<name>`, a path rather than a directory. Then
    `git -C <worktree> rev-parse --git-common-dir` resolves it back to
    `<repo>/.git`, and that resolves to the same value on the host and inside the
    workshop. That sameness is the identical-path rule at work.

So taboo bind-mounts two things into the workshop:

1. the run's worktree, at the fixed target `/taboo/workspace` (the `WorkspaceTarget`
   constant in `internal/workshop/template.go`);
2. the repository's main `.git`, the git common directory, at its identical host
   absolute path inside the workshop.

The second mount is the load-bearing one. `GitCommonTarget` in
`internal/workshop/template.go` returns `filepath.Join(repoPath, ".git")`,
and that value is used both as the mount target baked into the workshop
definition and as the remount source, so the host `.git` path and the
in-workshop `.git` path are the same string.

## Why identical-path `.git` matters

Because the common directory is mounted at the same absolute path on both sides,
the worktree's `.git` pointer resolves to the same place whether git reads it from
the host or from inside the workshop. The agent commits in place, the commit's
objects and the branch ref land in the shared `.git`, and the branch HEAD is
visible to host-side git immediately. `Runner.Exec` reads it back with
`git -C <worktree> rev-parse HEAD` and records it on `RunResult.Commit`. That same
shared object store is why the agent is push-denied: a push from inside the
workshop, forced or not, could mutate host branches directly (see **Push is
denied**, below).

The alternative is to rewrite the worktree's `.git` pointer to a fixed in-workshop
path such as `/gitcommon`. That also lets the in-workshop commit succeed, but it
breaks the worktree for host-side git, because the host would then read a pointer
aimed at a path that does not exist outside the container. Keeping the path
identical avoids any rewriting and keeps the worktree valid on both sides at once.
ADR 0007 ([nested worktree placement](https://github.com/josecabralf/taboo/blob/main/docs/adr/0007-nested-worktree-placement.md))
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

Reusing the workshop is what makes taboo usable at scale: a 50-run fan-out pays
the minutes-long container build once, not 50 times, and every run after the first
costs only seconds. Here is the mechanism that keeps the reuse safe. Each run
needs a different worktree mounted at `/taboo/workspace`, but the workshop is
long-lived and was launched once, so taboo repoints the existing mount rather than
relaunching. The mechanism is `workshop remount`, which points an existing mount
plug at a new host source.

A `remount` is only atomic when the new source is empty (or non-existent) and on
the same filesystem as the current one. A worktree is a non-empty directory, so the swap cannot be
live. `Runner.Setup` does it as `stop`, then `remount` for `workspace` and
`gitcommon` (and `sessions` for a session-capable agent), then `start`. The
ordering is visible in the code: `workshop.VerbArgs(proj, "stop", ws)`, the
`workshop.RemountArgs` calls, then `workshop.VerbArgs(proj, "start", ws)`.

This stop, remount, start, exec cycle takes seconds. The expensive work, creating
the container and installing the agent CLI, already happened at launch and is not
repeated. That is the whole point of reusing the workshop: the minutes-long launch
is paid once and amortized across every run that follows, while each run pays only
the seconds-long swap.

The swap has a second effect that explains why the agent must be baked in rather
than installed at runtime. A `stop` reprovisions the rootfs from the declared SDKs,
so anything installed ad hoc into the rootfs by a previous `exec` is wiped before
the next run. Only the bind-mounts survive. taboo therefore ships the agent CLI
as a workshop SDK embedded with `//go:embed sdk` in
`internal/workshop/materialize.go` and seeds it into the project on first run,
so the agent is present in the rootfs every time the workshop starts. The sessions
mount survives the same wipe, which is what lets an agent's resume/fork state
persist across runs; each `Pool` slot gets its own sessions directory under its
own `ProjectDir`, so parallel runs never share a session store.

## One workshop per agent, one workshop per slot

The planning consequence first: you cannot point one provisioned workshop at a
second repository, so plan one workshop per repo (and one per agent). A single
workshop is pinned to one agent and, through the git-common mount, to one
repository. `WorkshopName(base, agent)` in `internal/workshop/workshop.go`
derives the name as `base + "-" + agent`, so each distinct agent gets its own
reused workshop. The repository pinning is structural: the git-common target
equals `<repoPath>/.git`, and `remount` repoints a mount's source but cannot move
its target after launch, so a workshop launched for one repository cannot serve
another at a different absolute path without breaking the identical-path invariant.

Fan-out follows the same logic at the slot level. `Pool` in `internal/run/pool.go`
gives each concurrency slot a distinct workshop named `"<Workshop>-<slot>"` under
its own project directory `"<ProjectDir>/slot-<slot>"`, so slots never collide on
definitions, worktrees, or session stores. Isolation is at the workshop level: each
slot reuses its own workshop across waves, and every request still gets its own
branch and worktree. Because all slots share the base repository, `Pool` serializes
`git worktree add` and `git worktree remove` across them (both mutate the shared
`.git` worktree registry), while the workshop swaps and agent execs run concurrently. The
[fan-out guide](../guides/fan-out-runs.md) covers the operational caveats, such as
not running `git gc` against the repository mid-run.

A warmer fan-out, where each slot starts from a clone of an already-provisioned
workshop, would save the per-slot provisioning cost, but it is deferred. ADR 0006
([defer warm-clone fan-out](https://github.com/josecabralf/taboo/blob/main/docs/adr/0006-defer-warm-fanout-single-repo-workshops.md))
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
`<repo>/.taboo/worktrees/<branch>`, inside the repository and git-ignored. Under
`Pool`, each slot gets its own `ProjectDir` (`<ProjectDir>/slot-<N>`), so a
fan-out run's worktree lands one level deeper, at
`<ProjectDir>/slot-<N>/worktrees/<branch>`. The layout is deterministic and
private; it is the same knowledge `clean` uses to find what to tear down.

Nesting a worktree under the same repository whose `.git` is the second mount looks
risky, but it changes only the host path of the `/taboo/workspace` source. The two mounts
themselves are untouched: the worktree still goes to `/taboo/workspace`, and `<repo>/.git`
still mounts at `<repo>/.git`. The worktree's `.git` pointer references the common
directory, which is present through the git-common mount regardless of where the
worktree physically sits. ADR 0007 records that this nested arrangement was verified
on workshop 0.9.1 and LXD 6.8, and that the out-of-repo layout remains a sound
fallback. The only host-side cost the nested layout inherits is the
repo-not-under-`/tmp` constraint, which already existed for the git-common mount.

## Teardown is not on the run path

Setup creates a worktree; nothing on the run path removes it. `Runner.Run`,
`Setup`, `Exec`, the orchestrator loop, and `Pool.Run` all leave the worktree on
disk after they return, and the `RunResult`'s worktree handle keeps pointing at
it (read its files with `res.Artifact(relpath)`). This is
deliberate: the commit lives in the shared `.git`, so a finished run has nothing
left to extract from the working directory, but the directory itself is not
reaped. A long-running session, a daemon, or a `Pool` that fans many runs out
therefore accumulates worktrees until something clears them.

!!! tip "Observe it"
    `git -C <repo> worktree list` shows every worktree under `.taboo/worktrees/`
    that no run will remove for you.

That something is the `clean` command. It probes the host for the project's
taboo-managed artifacts and tears them down: `git worktree remove` for each
worktree, `workshop remove` for each workshop, and `git branch -D` for the run
branches under the configured prefix when asked. It is the only teardown path, and
it is interactive by design — `--dry-run` prints the plan and a confirmation
prompt gates the mutation. See the [CLI reference](../reference/cli.md) for the
flag matrix.

!!! warning "The library tears down one worktree, not the whole run"
    `RunResult` carries the teardown most embedders need. `res.Dispose()` removes
    the run's worktree with a non-force `git worktree remove` (the same teardown
    `clean` uses) and is idempotent, so an already-removed worktree counts as
    success. It is explicit and never automatic: nothing on the run path calls it
    for you, so a long-running session, a daemon, or a `loop` on a persistent
    checkout must call `res.Dispose()` itself or accumulate worktrees. Read a
    finished run's files first with `res.Artifact(relpath)`.

    Two gaps remain. `Dispose` removes only the worktree; it leaves the branch ref
    and the workshop intact, since persistence is the default. And there is no
    library-level equivalent of the full `clean` command, which also runs
    `workshop remove` and `git branch -D`. A client that needs full teardown
    drives `clean` (or shells out to those verbs) itself. The orchestrator
    sidesteps all of this because CI hands it a fresh, ephemeral checkout per run
    and throws the whole tree away afterward.

## Push is denied; the host owns integration

Agents run headless, with no interactive approver, and commit autonomously. The
two profiles that expose a tool-permission flag wire in a hard `git push` deny by
default — Claude Code via `--disallowedTools "Bash(git push *)"`, Copilot via
`--deny-tool=shell(git push)` — while OpenCode's argv carries no push deny at all
and leans solely on the workshop container as the boundary (see
[agents.md](../reference/agents.md)). The
deny is deliberate. A linked worktree shares the host repository's object store and
refs, so a push from inside the workshop, forced or not, could mutate host branches
directly.

taboo's contract is to commit in place and let the host own integration. The agent
never needs to push, because its commits are already on the host branch the moment
it makes them. A workflow that does need to publish a branch or open a pull request
adds an explicit push stage on the host side, after the run, rather than relying on
the agent to push from inside the workshop.

## The model in four facts

Remember four things and you can regenerate the rest:

1. A linked worktree's `.git` is a **pointer**, not a repository.
2. So taboo mounts **two** things: the worktree and the main `.git`.
3. The `.git` mount sits at its **identical absolute path** on both sides, so the
   pointer resolves the same inside the workshop and on the host.
4. The agent therefore **commits in place** onto the host branch, and for the same
   reason (a shared object store) it is **push-denied**.

## See also

- **Start here:** [ADR 0007: nested worktree placement](https://github.com/josecabralf/taboo/blob/main/docs/adr/0007-nested-worktree-placement.md) — the integration test (`TestIntegration_NestedWorktreeArrangement`) that proved, on real workshop 0.9.1 and LXD 6.8, that the commit lands on the host branch and the worktree's `.git` pointer still resolves. It is the empirical ground for everything above.
- [Why the API is shaped this way](design.md)
- [Library API reference](../reference/library-api.md) — the `RunResult.Dispose` / `Artifact` signatures.
- [ADR 0006: defer warm-clone fan-out, one workshop per repo](https://github.com/josecabralf/taboo/blob/main/docs/adr/0006-defer-warm-fanout-single-repo-workshops.md)
