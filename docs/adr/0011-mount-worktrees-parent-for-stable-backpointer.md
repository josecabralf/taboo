# Mount the worktrees parent so a linked worktree survives in-workshop git

## Status

accepted

## Context & decision

taboo's two-mount rule (ADR 0007, CONTEXT.md) bind-mounts two things into the
workshop so a *linked* git worktree resolves: the worktree's working dir (at the
relocatable `/taboo/workspace`) and the repo's main `.git` (the *common dir*, at
its identical host path). With those two, `git rev-parse`, `git status`, and
`git commit` from inside the workshop all resolve — the worktree's `.git` pointer
chases through to the common dir and back.

But a linked worktree has a **third** path in play. Its admin dir,
`<repo>/.git/worktrees/<name>/`, holds a `gitdir` file that is a **back-pointer**
to the worktree's host working path:
`<ProjectDir>/worktrees/<name>/.git` (i.e. `<repo>/.taboo/worktrees/<name>/.git`).
Only the worktree (relocated to `/taboo/workspace`) and `.git` are mounted, so
that back-pointer path is **invisible inside the workshop** — the working dir is
there, but at a *different* path than the one the back-pointer names.

git treats a worktree whose back-pointer does not resolve as **stale / prunable**,
and a `git worktree prune` deletes its admin dir **with no grace period**. The
3-month `gc.worktreePruneExpire` window does not apply here: it covers a worktree
whose *gitdir file* is valid but whose working dir is merely missing. A
back-pointer to a path that does not exist is pruned immediately (reproduced with
plain git, see Verification). Because the host `.git` is bind-mounted, that
deletion lands **on the host too**. The branch is orphaned and
every subsequent in-place `git commit` and host-side `git rev-parse HEAD` fails:

```
fatal: not a git repository: <repo>/.git/worktrees/agent-issue-<n>-<slug>
```

First seen in CI run 28110176802 (issue #26): the agent ran ~13 min, the admin
dir vanished mid-run, the agent's edit sat uncommitted, and the host-side
rev-parse failed. The two-mount rule was sufficient for the read/commit path but
not against any in-workshop `git worktree`-class operation that validates or
prunes the registry (`prune`, and anything that triggers it).

**Decision: mount a third path — the worktrees parent — at its identical host
path.** taboo adds a `worktrees` mount plug whose target equals
`<ProjectDir>/worktrees` (`WorktreesCommonTarget`), the parent of every run's
worktree, mounted at the same absolute path inside the workshop. This makes the
back-pointer `<repo>/.taboo/worktrees/<name>/.git` resolve **identically inside
and on the host**, exactly the mechanism the git-common mount uses. The worktree
is no longer stale to in-workshop git, so `prune` is a no-op and the admin dir
survives. The two-mount rule becomes a **three-mount rule**.

The parent (not the per-branch worktree) is the mount target on purpose: it is
the **same path for every branch in a ProjectDir**, so the plug target is static
— declared once in the derived definition, no per-run `remount` target change,
no per-run `refresh`. (Mounting the per-branch worktree at its identical path
would also fix the back-pointer, but its target changes every run, which a
persistent workshop's fixed plug cannot express without re-deriving each run.)

## Why not the alternatives

- **Rewrite the back-pointer to an in-workshop path** (e.g. point the admin dir's
  `gitdir` at `/taboo/workspace/.git`). Makes the worktree non-stale *inside*, but
  breaks it for **host-side** git (the host worktree is not at `/taboo/workspace`)
  — the same "valid on both sides" violation that got pointer-rewriting rejected
  for the `.git` pointer in ADR 0007 / CONTEXT.md. Rejected for the same reason.
- **`git worktree repair` inside the workshop.** Would rewrite the back-pointer to
  the in-workshop cwd (`/taboo/workspace`), reintroducing the host-side breakage
  above. (Conversely, once the back-pointer resolves under the chosen fix, an
  incidental `repair` is a no-op — it does not touch a pointer that already
  resolves, verified.)
- **A "fail fast" probe** (the reverted `d4b1cec`). An `OnWorkshopReady` hook that
  ran `rev-parse`/`status` and aborted on failure only ever *detected* a problem
  at one instant, and those commands don't prune — so it went green while the real
  prune-driven failure was untouched. It diagnosed, it did not fix. Reverted.
- **Abandon linked worktrees** (a self-contained clone inside the workspace,
  reconciled to the host on success). Sidesteps the shared-registry fragility
  entirely, but reintroduces a copy/sync step that the whole bind-mount,
  commit-in-place design (CONTEXT.md) exists to avoid. Heavier; not warranted when
  one extra mount closes the gap.

## Mechanics

- `agentPlugs` (internal/workshop/derive.go) emits a `worktrees` mount plug, like
  `workspace`/`gitcommon`/`sessions`. Its `workshop-target` is
  `WorktreesCommonTarget(cfg.ProjectDir)` = `<ProjectDir>/worktrees`.
- `Runner.Setup` (internal/run/runner.go) remounts it (source == target) right
  after the `gitcommon` remount, inside the per-run `stop → remount… → start`
  swap. Source equals target and never varies per run.
- Like git-common, the target is **not** under the reserved `/taboo/...` prefix
  (ADR 0009): its path *is* the mechanism, so it must equal the host path.
- A persistent workshop launched before this change picks the plug up on its next
  run via the existing fingerprint-drift `refresh` (ADR/issue #70 path): the
  derived definition now carries three plugs, the fingerprint changes once, and
  `ensureWorkshop` refreshes once. No per-run cost thereafter.

## Verification

`TestIntegration_WorktreePruneInWorkshopKeepsAdminDir` (internal/run, `-tags
integration`) drives the nested arrangement CI hits: a deterministic script runs
`git worktree prune -v` inside the workshop and then commits. Before the fix the
prune self-deletes the worktree's admin dir and the commit fails 128 with the
exact CI error above; after it, the prune is a no-op, the admin dir survives, the
commit lands on the host branch, and host-side `rev-parse` resolves.

The git-level mechanism (immediate prune of a worktree with an unresolvable
back-pointer; `git gc`'s 3-month expire *not* covering it; `repair` being a no-op
on a resolvable pointer) was reproduced with plain `git` before choosing the fix.

## Consequences

- **Three-mount rule.** CONTEXT.md's two-mount note is updated; the worktree now
  requires worktree + git-common + worktrees-parent. All three always-on (sessions
  stays session-capable-only).
- **The worktrees parent is exposed read-write in the workshop.** It holds only
  this ProjectDir's own worktrees — all checkouts of the same repo, whose
  *committed* content is already reachable via the git-common mount — so the added
  surface is small. The new exposure is *uncommitted/untracked* working-tree files
  of any sibling worktree that happens to coexist: on sequential runs reusing a
  ProjectDir, if a prior run's worktree dir has not yet been disposed, a later
  agent can read or write it. This is the same repo the agent already controls,
  and `Dispose` removes the per-branch worktree dir, so the window is narrow; it is
  noted here only because git-common alone never exposed untracked content. taboo's
  other `.taboo` state (sessions, the derived def, the fingerprint) is **not**
  mounted — only the `worktrees/` subtree is.
- **Reversible.** Removing the plug + its remount reverts to the two-mount rule.
- Couples, like git-common, to a non-`/tmp` host repo path (already required).
