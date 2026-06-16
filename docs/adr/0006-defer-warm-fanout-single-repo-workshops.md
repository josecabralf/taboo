# Defer warm-clone fan-out (blocked upstream); keep one workshop per repo

## Status

accepted

## Context & decision

Issue #29 (parent PRD #1) is a *measure-before-building* investigation into two
items PRD #1 listed as **Out of Scope**: (1) **warm workshops** via ZFS
snapshot/clone, so `Pool` fan-out starts each slot from a warm clone instead of a
cold provision; and (2) **persistent workshop reuse across different repos**,
decoupling a workshop from the single repo its git-common mount is pinned to. The
spike (spike 0001) carries the full cost model, citations, and option tables; this
ADR records the decision only.

**The decision is to adopt neither now:**

- **Warm-clone fan-out — deferred, blocked upstream.** The win is real (it saves
  the dominant per-slot cost, `(setup-base + create) × (slots−1)`), but it is
  unrealizable inside taboo's settled CLI-only contract. The `workshop` CLI
  exposes no clone / snapshot / `launch --from` verb, and — confirmed this
  investigation — neither does workshop's Go `client/`: it is a typed wrapper over
  the *same* daemon HTTP API, with the identical verb surface. The ZFS clone
  (`CopyInstance`) lives below that API line, reachable only by workshop's internal
  Stash/snapshot machinery, whose snapshots are keyed per-`(project-id,
  workshop-name)` (so no sibling can warm-clone another) and whose `Restore`
  (`UnstashWorkshop`) fails if a workshop of the same name exists. Defer until the
  `workshop` CLI grows a clone / `launch --from` verb; re-quantify with the shipped
  benchmark harness before revisiting.

- **Multi-repo reuse — not building it; one workshop per repo.** Weighing the
  motivation (amortize the minutes-long launch across repos, not just across runs)
  against the cost, the realistic usage is single-repo-focused: a workshop launched
  per repo leaves at most one or two idle containers, a negligible cost, while the
  only in-contract decoupling (common-parent mount) imposes a real
  filesystem-layout constraint (all managed repos under one host parent). PRD #1
  also gates this on a measured launch cost that justifies it, which is unmeasured
  here. **One workshop per repo is the accepted approach.**

The coupling that drives the second decision is structural: `gitCommonTarget`
returns `<repoPath>/.git` and is used **both** as the baked `workshop-target` and
as the per-run remount *source*; `remount` repoints the **source only** (the CLI
cannot move a mount target after launch), so a workshop launched for repo A cannot
serve repo B at a different absolute path without breaking the identical-path
invariant.

| Capability needed | CLI verb | Go `client/` verb | Reachable in-contract? |
|-------------------|----------|-------------------|------------------------|
| Warm-clone a provisioned base into N siblings | none | none (`CopyInstance` is internal-only) | No — needs an upstream verb |
| Save/restore one workshop's own state | `restore` | `Restore` (fails if name exists) | Yes, but wrong shape for fan-out |
| Move a mount *target* after launch | none (`remount` = source only) | `Remount` (source only) | No |

## Considered options

Warm-clone fan-out:

- **Direct LXD coupling** (taboo calls `lxc copy` / the LXD API). *Rejected:* breaks
  the CLI-only contract and couples taboo to the substrate — the exact thing that
  contract exists to prevent (CONTEXT.md).
- **Use the Go `client/` instead of the CLI.** *Rejected:* same daemon API surface
  as the CLI, no clone verb either — it buys typed calls (useful against the
  table-parsing fragility, a separate concern) but **zero** new capability for
  warm-clone.
- **Upstream `workshop clone` / `launch --from` verb.** The clean unblocker, but a
  *workshop* feature outside taboo's control. **Defer until it exists (chosen).**
- **Volume-backed agent SDK** — package the agent CLI as a shared store/volume SDK
  instead of an in-project `setup-base` hook, shifting the dominant per-slot cost
  into the already-shared bucket. In-contract and captures most of the win *without*
  cloning, but it is a meaningfully different provisioning model. Tracked as a
  future direction, not a #29 deliverable.

Multi-repo reuse:

- **One workshop per repo (chosen).** Sidesteps the per-repo target coupling
  entirely; cost is N idle containers, negligible for realistic single-repo-focused
  usage.
- **Common-parent mount** — bake the git-common target as a shared host parent (e.g.
  `$HOME/repos`) mounted at its identical path; any repo under it resolves, the
  identical-path invariant preserved one level up. In-contract and low-risk, and
  remains the **design of record *if* this stance reverses** — but not built; it
  imposes a single-host-parent layout constraint and the launch cost that would
  justify it is unmeasured.
- *Rejected:* source-only remount with a fixed target (cannot satisfy the invariant
  for a second repo); rewriting the worktree `.git` pointer to a fixed in-workshop
  path (breaks host-side git — already rejected in CONTEXT.md).

## Consequences

- **No production code ships from #29.** The deliverables are the benchmark harness
  (`pkg/taboo/bench_test.go`, integration-tagged, `TABOO_BENCH=1`-gated), the spike
  report, and this ADR. `go build ./... && go vet ./... && go test ./...` stays
  green (126 unit tests); the integration build tag still compiles (`go vet -tags
  integration ./pkg/taboo/`).
- **Reversibility is high.** Both decisions are pure "do not build" — no seam, type,
  or contract changed. Warm-clone reverses the day an upstream verb lands (re-run the
  harness, then implement behind the `Commander` seam). Multi-repo reverses by
  adopting the recorded common-parent design (a configured parent + a
  template/remount tweak + an invariant guard).
- **The benchmark is documented-but-unrun** — this environment has no LXD host. The
  honest state: the warm-clone win rests on CONTEXT.md's prior measurement (swap
  stop≈2s/start≈3.6s, launch "minutes"); run the harness on real infra before
  reopening the warm-clone decision.
- **Two upstream/future asks are now tracked here:** a `workshop clone` /
  `launch --from` verb (unblocks warm-clone), and the volume-backed agent SDK
  (in-contract partial win). Neither is taboo's to ship under #29.
- CONTEXT.md's deferred-item notes for both are annotated as resolved by this ADR.
