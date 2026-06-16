# Nested worktree placement: `<repo>/.taboo/worktrees/<branch>`, verified

## Status

accepted

## Context & decision

The taboo CLI (PRD #19) puts everything for a repo under one directory inside the
repo: `ProjectDir = <repo>/.taboo`, with `worktrees/` git-ignored within it. taboo
derives a run's worktree path as `<ProjectDir>/worktrees/<branch>` (slashes in the
branch replaced by `-`; see `Runner.worktreePath`), so that layout nests each
worktree at **`<repo>/.taboo/worktrees/<branch>`** — inside the target repo. PRD
#19 flagged this as an **open risk to verify on real workshop + LXD before `run`
is built on it** (issue #35), with the known fallback being worktrees in a
standalone directory outside the repo.

The risk is the two-mount rule (CONTEXT.md): a linked worktree's `.git` is only a
pointer into `<repo>/.git/worktrees/<name>`; taboo mounts the worktree at
`/workspace` and the repo's `.git` at its **identical host absolute path**, so the
pointer resolves with no rewriting. The question was whether nesting the worktree
*under* the repo — whose `.git` is itself the second mount — still produces a
worktree whose `.git` pointer resolves identically inside the workshop and on the
host, leaving the commit landing on the host branch.

**Decision: adopt the nested arrangement.** It is verified to work on real
workshop (0.9.1) + LXD (6.8). The out-of-repo arrangement remains a sound,
already-exercised fallback, but nested is chosen because it realizes the PRD's
single-`.taboo/` layout (one git-ignored directory per repo; the git-ignored
subset never appears inside an agent's worktree checkout).

### Why it works

Worktree placement is a **host-side** concern; it does not change the mount
topology. Regardless of where the worktree physically lives, exactly two things
are mounted into the workshop: the worktree → `/workspace`, and `<repo>/.git` →
`<repo>/.git`. The worktree's `.git` pointer (`gitdir: <repo>/.git/worktrees/
<name>`) references the **common dir**, which is present via the gitcommon mount —
not the worktree's own location. Nesting therefore changes only the host path of
the `/workspace` source, leaving the identical-path invariant for `.git`
untouched. (The git back-pointer from the common dir to the worktree's host path
is already non-resolvable inside the workshop in *both* arrangements, since the
worktree is mounted at `/workspace`, not at its host path; git commits from the
worktree side regardless, as the out-of-repo tests have always shown.)

The one host-side caveat the nested layout adds: `<repo>/.git` is bind-mounted at
its identical path, so the **repo must not live under `/tmp`** (or `/run`) — a
target on the container's volatile tmpfs silently fails to mount and the pointer
becomes unresolvable. This constraint already exists for the out-of-repo layout
(it is a property of the gitcommon mount, not of worktree nesting) and is already
recorded in CONTEXT.md and enforced by `doctor`/`validate` (PRD #19).

### Verified evidence

`TestIntegration_NestedWorktreeArrangement` (`pkg/taboo/integration_test.go`,
`go test -tags integration`) drives the full launch → nested-worktree →
stop/remount/start → exec → commit-in-place path against real workshop + LXD using
the deterministic shell agent (no LLM, no credential — it gates on workshop + LXD
alone). It asserts:

- the worktree is actually nested under `<repo>/.taboo/worktrees/` (not a
  standalone dir);
- the agent's commit lands on the host branch (`RunResult.Commit` non-empty, the
  committed file present on the host worktree, the commit in `git log`);
- the worktree's `.git` pointer resolves for **host-side** git afterwards
  (`git -C <worktree> rev-parse --abbrev-ref HEAD` returns the run's branch),
  proving no pointer rewriting broke either side.

Result: **PASS** (29.3s) on workshop 0.9.1 + LXD 6.8. The run's own log line:

```
nested arrangement OK: workshop=taboo-it-613468
  commit=d7c0ef2293cfba8d0e17e1d7d692dee378eccd1e
  worktree=<repo>/.taboo/worktrees/agent-nested
```

The nested arrangement is settled.

## Considered options

- **Nested in-repo at `<repo>/.taboo/worktrees/<branch>` (chosen).** Realizes the
  PRD's single-directory-per-repo layout; the git-ignored `worktrees/` never
  enters an agent's worktree checkout (a worktree checkout contains only tracked
  files). Verified above. Cost: inherits the repo-not-under-`/tmp` constraint
  (already present regardless of nesting).
- **Out-of-repo, standalone `ProjectDir` (fallback, not chosen).** What the other
  integration tests use (`newIntegrationRunner` with a `nonTmpDir` project dir
  separate from the repo). Equally correct on the mount topology, but spreads a
  repo's taboo state across two locations and reintroduces per-repo bookkeeping
  the `.taboo/` layout exists to avoid. Retained as the fallback if the nested
  layout ever proves problematic on a future workshop/LXD version.

## Consequences

- **No production code change.** Worktree placement is fully determined by
  `Config.ProjectDir`; the CLI sets `ProjectDir = <repo>/.taboo` and the existing
  `Runner.worktreePath` nests the worktrees. The only change is the verification
  test plus a refactor of `newIntegrationRunner` to take an explicit project dir
  (so the same helper drives both arrangements).
- **`run` and `clean` consume a fixed layout.** Worktrees live at
  `<repo>/.taboo/worktrees/<sanitized-branch>`, where `sanitized` replaces `/`
  with `-`. `clean` removes them with `git worktree remove` (per PRD #19), and
  `list` enumerates them there. No re-decision needed downstream of this gate.
- **Reversible.** Switching to the out-of-repo fallback is a one-line change to how
  the CLI computes `ProjectDir`; nothing in the library or mount wiring depends on
  the worktree being nested.
- CONTEXT.md's worktree note is updated to record the verified placement and point
  here.
