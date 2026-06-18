# Spike: warm-workshop fan-out (ZFS snapshot/clone) + multi-repo reuse

Investigation for issue #29 (two deferred items from PRD #1 "Out of Scope").
This is the **report** the issue asks for; the **decision** it drives is recorded
in [ADR 0006](../adr/0006-defer-warm-fanout-single-repo-workshops.md).

> **Measure before building.** #29 is `needs-decision`: an optimization, not a
> correctness gap. The conclusion below is *don't build it in taboo yet* — the
> win is real but blocked by taboo's own CLI-only contract, and the magnitude is
> unmeasured. A runnable benchmark ships with this spike
> (`pkg/taboo/bench_test.go`) so the magnitude can be recorded on real infra.

## 1. What was investigated

Two PRD #1 Out-of-Scope items:

- **Warm workshops via ZFS snapshot/clone.** Pool fan-out (`pkg/taboo/pool.go`)
  launches/provisions N workshops; CONTEXT records per-run swap stop≈2s,
  start≈3.6s, and launch as "minutes". Can a fan-out slot start from a *warm*
  clone of a provisioned base instead of a cold provision?
- **Persistent workshop reuse across different repos.** The git-common mount
  target equals the host `.git` absolute path (`gitCommonTarget`,
  `pkg/taboo/template.go:63`), coupling a persistent workshop to one repo.

Both were studied against the real `workshop` source tree
(`../../workshop`), taboo's own code, and the sandcastle reference product.

## 2. Cost model — what a cold launch actually pays

A cold `workshop launch` decomposes into four components with very different
sharing properties (workshop `internal/overlord/workshopstate/request.go`,
`internal/workshop/lxd/`):

| # | Component | Cost | Shared across slots? |
|---|-----------|------|----------------------|
| a | Base image download (`ubuntu@24.04`) | one-time, minutes | **Yes** — cached host-wide in LXD's image store by fingerprint (`lxd_base_manager.go`); instance is a ZFS clone off the cached image |
| b | Store/system SDK volume import (e.g. `go` 1.26) | one-time per revision | **Yes** — imported once as a shared LXD storage volume `<name>-<rev>` (`lxd_backend_sdk.go`), mounted read-only into each workshop |
| c | **Local/project SDK `setup-base` hook** (agent install: `apt-get install`, `curl\|bash` opencode/claude/…) | **minutes, per slot** | **No** — baked into a per-`(project-id, workshop-name)` ZFS snapshot |
| d | Container create + cloud-init + boot | seconds, per slot | **No** |

The dominant *per-slot* cold cost is **(c)**: the agent SDK's `setup-base` hook
(`pkg/taboo/sdk/<agent>/hooks/setup-base` runs `apt-get` + a `curl … | bash`
network install). It runs cold on every slot's first launch and is captured into
a snapshot keyed by `config.user.workshop.project-id` **and**
`config.user.workshop.name` (`lxd_backend_snapshots.go` `TakeSnapshot`,
`snapshotNamesAfter`).

`Pool.slotConfig` (`pkg/taboo/pool.go:51`) gives each slot **both** a distinct
project dir (`<ProjectDir>/slot-<i>` → distinct LXD project-id) **and** a distinct
workshop name (`<Workshop>-<i>`). So slot 1's baked snapshot is invisible to slot
2: **every slot's first launch re-runs (c)+(d) from scratch, scaling ~linearly
with slot count.** (a) and (b) are paid once per host and reused.

**Warm-clone savings ceiling** = `(setup-base + create/cloud-init) × (slots − 1)`.
It would *not* save (a)/(b) — those are already shared, so a warm-clone design
must not be credited for them.

## 3. The capability exists in the substrate — but not at the CLI

Workshop **already uses LXD instance-copy (ZFS clone on a ZFS pool) internally**:
`Backend.copyInstance` / LXD `CopyInstance` underpin `TakeSnapshot`,
`LaunchOrRebuildWorkshop`, and `Stash`/`Unstash`
(`internal/workshop/lxd/lxd_backend_snapshots.go`). The ZFS snapshot/clone the
issue imagines is real and in daily use for SDK provisioning.

**But it is internal-only.** The full `workshop` CLI surface
(`cmd/workshop/root.go:84-108`) is: launch, list, changes, tasks, refresh,
restore, start, stop, info, exec, shell, run, actions, remove, remount,
connections, connect, disconnect, warnings, okay, sketch-sdk, sketches, docs.

There is **no** `clone` / `copy` / `snapshot` / `export` / `import` /
`launch --from` verb. Verified by independent skeptical review:

- `launch` (`cmd/workshop/launch.go`) builds from a definition; no
  `--from/--copy/--source`.
- `restore` (`cmd/workshop/restore.go`) reverts **the same** workshop's rootfs to
  its own last launch/refresh — not a cross-workshop clone.
- `sketch-sdk --stash/--restore` stashes a per-workshop **SDK YAML template**
  dir, not the rootfs, and cannot seed a different workshop.
- The daemon route table (`internal/daemon/api.go`) and client
  (`client/projects.go`, `client/workshop.go`) expose only
  launch/refresh/restore/start/stop/remove — no copy/clone endpoint.
- SDK snapshots are looked up **by `(project-id, name)`, never by content hash**:
  `HashSnapshot`'s sha3-384 digest is stored as metadata but is *never* used as a
  `GetInstances` filter. So even two byte-identical sibling workshops cannot reuse
  each other's snapshot — the name filter excludes it.
- `copyInstance` *can* copy across names/projects, but is only ever invoked by
  Stash/Unstash with the **same** `(project, name)` identity.

## 4. Why this blocks adoption *in taboo*

taboo's settled core design (CONTEXT "Integration: shell out to the `workshop`
snap CLI") is: **drive workshop through its CLI only — no import of workshop's Go
`client/`, no speaking LXD directly.** This is the property that lets taboo be
open-sourced independently of workshop and follow a stable contract.

Warm-cloning a provisioned base therefore has only three routes, none currently
adoptable:

1. **Direct LXD coupling** (taboo calls `lxc copy` / the LXD API itself).
   **Rejected** — it breaks the CLI-only contract and couples taboo to the
   substrate, the exact thing that decision exists to avoid.
2. **An upstream `workshop` CLI verb** (`workshop clone <src> <dst>` or
   `launch --from <ws>`). Does not exist. This is the clean unblocker, but it is
   a *workshop* feature request, outside taboo's control.
3. **Shift cost (c) into the already-shared bucket (b)** — package the agent CLI
   as a **store/volume SDK** instead of a project-source `setup-base` hook, so its
   payload is imported once as a shared volume and mounted into every slot. This
   is **in-contract** and would erase most of the warm-clone win *without any
   cloning* — but it depends on publishing agents to a store/registry and is a
   meaningfully different provisioning model (CONTEXT's agent-as-SDK currently
   uses in-project SDKs as the proven path). A candidate future direction, not a
   #29 deliverable.

The sandcastle reference offers nothing to borrow: it has **no** warm pool, no
base-sandbox snapshot/clone fan-out. Its `createSandbox()` is a single long-lived
sandbox coupled to one repo+branch; `.fork()` is session-only; multi-repo is "one
sandbox per repo via `cwd`" (sandcastle ADR 0002/0003/0018). It reports no
warm-vs-cold speedup number.

## 5. Multi-repo reuse

The coupling is structural: `gitCommonTarget(repoPath)` returns
`filepath.Join(repoPath, ".git")` and is used **both** as the baked
`workshop-target` (`template.go:71`) **and** as the per-run remount *source*
(`runner.go:207`). `remountArgs` (`workshop.go:32`) repoints the **source only** —
the CLI has no way to move a mount *target* after launch. So a workshop launched
for repo A (target = `A/.git`) cannot serve repo B at a different absolute path:
B's worktree `.git` pointer would resolve to `A/.git` inside and fail
(`fatal: not a git repository`).

Decoupling options (CLI-only):

| Option | In-contract? | Cross-repo reuse? | Risk to identical-path invariant |
|--------|--------------|-------------------|----------------------------------|
| **ii. Common-parent mount** — bake the gitcommon target as a shared host parent (e.g. `$HOME/repos`) mounted at its identical path; any repo under it resolves | **Yes** | **Yes** | None — the invariant is preserved, just hoisted up a level (the CONTEXT escape hatch) |
| iv. Per-repo ProjectDir+workshop | Yes | No (sidesteps) | None, but one workshop per repo |
| v. Relaunch on repo change | Yes | No | None, but pays full launch cost per switch |
| i. Source-only remount, fixed target | No | — | Cannot satisfy the invariant for a 2nd repo |
| iii. Rewrite worktree `.git` pointer to a fixed in-workshop path | No | — | Breaks host-side git (rejected in CONTEXT) |

**Option ii is the chosen design *if/when* multi-repo reuse is adopted.** It is
in-contract and low-risk, but PRD #1 explicitly defers it and #29 gates it on
"only if the measured launch cost justifies it." With the launch cost unmeasured
here, it stays deferred; the design is recorded so adoption is a small, bounded
change (a configured parent + a template/remount tweak + an invariant guard
validating `RepoPath` is under a non-`/tmp` parent).

## 6. The benchmark harness (criterion 1)

`pkg/taboo/bench_test.go` (`//go:build integration`,
`TestBenchmark_PoolFanoutColdStart`, gated by `TABOO_BENCH=1`) fans a cold batch
out across N slots through a `phaseTimer` Commander decorator and reports
wall-clock cost per phase (verb), so `launch` (the cold provision) is isolated
from the `stop`/`remount`/`start` swap and the agent `exec`:

```
TABOO_BENCH=1 TABOO_BENCH_SLOTS=4 \
  go test -tags integration ./pkg/taboo/ \
  -run TestBenchmark_PoolFanoutColdStart -timeout 60m -v
```

Methodology notes baked into the harness doc-comment: time first-launch-per-slot
separately from same-slot reuse; run cold vs warm-host (base image + `go` volume
present) to attribute the one-time (a)+(b) host tax away from the per-slot
(c)+(d); scale slots 1→8 to confirm (c) is linear; treat `setup-base` as
high-variance (network/apt) and average ≥3 trials.

**Known baseline (from CONTEXT, prior measurement):** per-run swap stop≈2s,
start≈3.6s; launch "minutes", dominated by (c). The harness records the missing
piece — the absolute per-slot cold-launch wall time — on real infra. It was
**not** run for this spike: this environment has no LXD host.

## 7. Conclusion (→ ADR 0006)

- **Warm workshops via ZFS clone: defer / do not adopt in taboo now.** The win is
  real (saves `(setup-base + create) × (slots−1)`) but unrealizable within the
  CLI-only contract: no `workshop` clone verb exists, internal snapshots are
  per-`(project,name)`, and direct LXD coupling is rejected. Track the upstream
  ask (a `workshop clone`/`launch --from` verb) and the in-contract alternative
  (volume-backed agent SDK). Quantify with the shipped harness before revisiting.
- **Multi-repo reuse: defer per PRD #1.** Chosen design recorded (common-parent
  mount, option ii) for adoption once the launch cost justifies it.
