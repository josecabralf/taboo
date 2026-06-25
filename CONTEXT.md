# taboo

A Go library that orchestrates AI coding agents inside Canonical **workshop**
environments. Each agent gets an isolated, reproducible dev sandbox, and its
commits land back on the host git repository.

taboo is to [workshop](../workshop) roughly what
[sandcastle](../sandcastle) is to Docker/Firecracker: a thin orchestration layer
that handles sandbox lifecycle, the agent's working directory, agent invocation,
and result capture. The difference is the substrate. taboo builds on workshop's
LXD-backed, SDK-driven environments instead of containers/microVMs, and is
written in Go instead of TypeScript.

## What it does

For each agent run, taboo:

1. Creates a git **worktree** on the host (one per run, for isolation).
2. **Bind-mounts** that worktree into a workshop at a fixed path (`/taboo/workspace`).
3. **Execs** the agent CLI inside the workshop, streaming its output.
4. Lets the agent commit *in place* — because the worktree is a bind-mount, the
   commits land directly on the host worktree's branch. No extraction step.

Parallel agents each get their own (worktree + workshop) pair, so they never
touch each other's files.

## Core design decisions

These are settled.

### Integration: shell out to the `workshop` snap CLI

taboo drives workshop by invoking the `workshop` binary installed by its snap. It
does **not** import workshop's Go `client/` package, and does **not** speak the
REST socket directly.

Rationale:

- **No compile-time dependency on a private repo.** taboo's only runtime
  dependency is "the `workshop` snap is installed." This lets taboo be
  open-sourced independently of workshop's visibility.
- **The CLI is the most stable, documented contract** workshop offers (it
  auto-generates CLI reference docs). It is *more* stable than the raw REST
  socket, not less.
- **Decoupled release trains.** taboo follows workshop's published CLI, nothing
  internal.

The load-bearing command is `workshop exec`, which provides everything an agent
runner needs: live stdout/stderr streaming over websockets, TTY/interactive mode,
exit-code and signal forwarding, and `--cwd` / `--env` / `--uid` / `--gid` /
`--timeout` flags.

> **Snap-confinement caveat:** if taboo itself ships as a *strictly*-confined
> snap, calling another snap's binary requires the right interface. If taboo is a
> plain binary or classic-confined (like workshop), this is a non-issue.

### Interface: library-first

The primary deliverable is the Go module `taboo` (import path
`github.com/josecabralf/taboo`). The audience is engineers
building agent pipelines in Go, who want to express fan-out, review loops, and
custom orchestration in code. The CLI (`cli/`, module
`github.com/josecabralf/taboo/cli`) is a thin consumer of the library, not the
primary contract.

### Working directory & results: bind-mount, commit in place

workshop has no file-copy primitive, but it does have writable **bind-mounts**
via `remount`. taboo uses this instead of sandcastle's copy-in / extract-commits
model:

- The worktree lives on the host and is bind-mounted into the workshop.
- The agent commits inside it; commits are immediately on the host.
- No sync machinery, no `copyOut`, no `git bundle` tricks for the main flow.

This sidesteps workshop's missing copy primitive entirely and matches its grain.

**Worktree placement is host-side and free of the mount topology.** A run's
worktree lives at `<ProjectDir>/worktrees/<branch>` (slashes → `-`). The CLI sets
`ProjectDir = <repo>/.taboo`, so worktrees **nest at
`<repo>/.taboo/worktrees/<branch>`**, inside the repo and git-ignored. Nesting the
worktree under the repo whose `.git` is a mount is sound because placement changes
only the host path of the `/taboo/workspace` source, never the mounts themselves
(`TestIntegration_NestedWorktreeArrangement`): the commit lands on the host branch
and the worktree's `.git` pointer resolves on both sides. The out-of-repo layout
remains a sound fallback. See ADR 0007.

**Three mounts are required, not just the worktree.** A *linked* git worktree is
**not self-contained**: its `.git` is a pointer to the main repo's
`.git/worktrees/<name>`, and its objects/refs live in the main `.git`. Mounting
only the worktree's working dir yields `fatal: not a git repository` inside. So
taboo bind-mounts **three** things:

1. the worktree → a fixed target (`/taboo/workspace`);
2. the repo's main `.git` (the git *common dir*) → mounted at its **identical
   host absolute path** inside the workshop;
3. the worktrees *parent* (`<ProjectDir>/worktrees`) → mounted at its **identical
   host absolute path** too.

Mounting `.git` at the same absolute path makes the worktree's `.git` pointer
resolve identically inside and on the host, so **no pointer rewriting** is needed
and the worktree stays valid on *both* sides. (Rewriting the pointer to a fixed
in-workshop path like `/gitcommon` also lets the commit succeed, but it breaks
the worktree for host-side git — rejected.) Implication: the git-common mount's
`workshop-target` is **per-repo** (it equals the host `.git` path), so it is
templated into `workshop.yaml` per repo, not fixed. This couples a persistent
workshop to one repo unless all managed repos live under a single host parent
that is mounted at its identical path. Multi-repo reuse is **not** built
(ADR 0006): one workshop per repo; the common-parent mount is the design of
record only if that stance reverses.

The **third** mount (the worktrees parent) closes a subtler gap: the linked
worktree's admin dir (`<repo>/.git/worktrees/<name>`) holds a *back-pointer* to
the worktree's host path (`<ProjectDir>/worktrees/<name>/.git`, where
`<ProjectDir>` is `<repo>/.taboo`). With only mounts 1 and 2, that back-pointer
path is invisible inside the workshop: the working dir is mounted, but at
`/taboo/workspace`, not at the path the back-pointer names. In-workshop git
therefore treats the worktree as stale and prunable. A `git worktree prune` then
deletes the admin dir **with no grace period**, on the shared host `.git` too. The
branch is orphaned mid-run, and every later commit and `rev-parse HEAD` fails with
`fatal: not a git repository: .../.git/worktrees/<name>` (CI run 28110176802).
Mounting the worktrees parent at its identical host path makes the back-pointer
resolve on both sides for every branch, so the worktree is never stale and `prune`
is a no-op. The parent is the target rather than the per-branch worktree because
it is the same path for every branch. That makes the plug static, with no per-run
target change. Like git-common, it is exempt from the
`/taboo/...` namespacing: its path **is** the mechanism. See ADR 0011.

**The `branch` strategy opts out of the three-mount rule.** The two extra mounts
above exist only because a linked worktree is not self-contained. When a run can
own the checkout outright, those two mounts are unnecessary. `Runner.Setup`
therefore dispatches on a `strategy` seam (`workshop.Config.Strategy`):

- **`branch`** operates in place on the checkout (`cfg.RepoPath`): it creates the
  run's branch with `git switch -c` and binds only the checkout as the single
  `/taboo/workspace` mount. The checkout's `.git` is a real, self-contained
  directory, so it needs none of the git-common or worktrees machinery. That
  deletes the exact mechanism that fails in CI on LXD and GitHub Actions: the
  back-pointer and prune trap above. It first refuses a dirty checkout (a `git
  switch -c` in place would otherwise carry uncommitted changes onto the run's
  branch). Dispose is a no-op, because the workspace is the checkout, so there is
  no linked worktree to `git worktree remove`. The cost is one run per checkout:
  a single working tree and a single HEAD, so it cannot back the concurrent
  `Pool` or the local daemon. Use it for CI and other disposable checkouts.
- **`worktree`** (and `""`) keeps today's linked-worktree
  behavior, including the three-mount rule above. It is what the concurrent
  `Pool` (`internal/run/pool.go`) requires: each slot fans a run out onto its own
  branch and worktree, which only the worktree strategy provides.

`branch` and `worktree` are the only accepted values (with `""` defaulting to
`worktree`); `Setup` rejects anything else, so a typo fails loudly instead of
silently selecting the worktree path. The concurrent `Pool` forces the worktree
strategy on every slot (`Pool.slotConfig`), so fan-out stays correct regardless
of the configured default.

The one-run-per-disposable-checkout contract, and the consequences of breaking
it (HEAD left on the run branch, a second no-`BaseRef` run chaining off the
prior tip, and `git switch -c` aborting on an existing branch), are explained
for users in `docs/explanation/isolation-model.md` ("The branch strategy: one
run per disposable checkout"). Each is reachable only by reusing a checkout,
which the contract forbids; the fix for a reusable checkout is the worktree
strategy, not hardening the branch path.

**Mount-plug mechanics.** A `mount` plug is declared **inline in `workshop.yaml`**
under any SDK entry (`plugs: { <name>: { interface: mount, workshop-target:
<path> } }`) — no custom mount-SDK needs authoring; it auto-connects to
`system:mount` and gets a default host source, which `remount` then repoints.
taboo owns the rendered `.taboo/workshop.yaml`, but **no longer authors it from
scratch**: for a managed project it is *derived* from the project's own
`workshop.yaml` by opaque-tree injection (see "Derive the workshop from the
project's definition" below and ADR 0009). The relocatable mount targets
(`/taboo/workspace`, `/taboo/sessions`) move under a reserved `/taboo/...` prefix so they
cannot collide with the project's own mounts; the git-common and worktrees
targets stay at their host paths (the mount rule pins them there). Two `remount`
caveats:

- `remount` is atomic **only** when the new source is empty / non-existent and on
  the same filesystem as the current source. A worktree is non-empty, so the
  per-run swap is **`stop → remount → start`**, not a live swap. It takes seconds,
  not the minutes a launch costs.
- Adding a new plug to a running workshop requires a `refresh`.
- **Mount targets must not resolve to a volatile tmpfs inside the workshop.** A
  target under `/run` *or* `/tmp` silently fails to mount. Because the
  git-common target equals the host `.git` path, **the managed repo must live at
  a non-`/tmp` host path** (e.g. under `$HOME`); a repo under `/tmp` makes the
  worktree's `.git` pointer unresolvable inside (`fatal: not a git repository`).

The command form is `workshop remount <ws>/<sdk>:<plug> <host-source-path>`.
The `<sdk>` segment is the **bare** SDK name (`opencode`), not the
`project-`-prefixed reference used in the definition's `sdks:` list.

### Execution model: persistent workshop, one per concurrent agent

A workshop is expensive to stand up (LXD container + SDK install, minutes),
unlike a cheap ephemeral sandbox. So:

- **Default:** launch/reuse a *long-lived* workshop, and create a fresh worktree
  per run, remounting it for each run. The expensive launch is amortized across
  many sequential runs.
- **Parallelism:** for N concurrent agents, stand up N workshops, each with its
  own worktree. Isolation is at the workshop level. Reuse across waves keeps the
  cost down. Warm-clone fan-out is deferred (ADR 0006), blocked upstream: no
  `workshop` clone / `launch --from` verb; the Go client shares the same API
  surface; direct LXD coupling is rejected.

### Agent provisioning: agent-as-SDK

The agent CLI (Claude Code, Codex, OpenCode, etc.) must exist inside the workshop
before taboo can exec it. taboo packages/requires each supported agent as a
**workshop SDK**, and its templated `workshop.yaml` declares the agent SDK
alongside the worktree-mount SDK. SDKs are workshop's native, versioned,
store-sourced dependency mechanism.

**Why agent-as-SDK and not runtime install.** Rootfs changes made by `exec`
(e.g. `apt-get install nodejs`, `npm i -g opencode-ai`) persist across plain
`exec` calls but are **wiped by `refresh` and by `stop`** — they reprovision the
rootfs from the declared SDKs. Since the per-run loop is `stop → remount → start`
(the worktree swap above), any ad-hoc-installed agent would be erased before it
ran. The agent therefore *must* be baked in via an SDK, not installed at runtime.
Only bind-mounts (worktree, git-common, worktrees-parent, sessions) survive a
`stop`/`refresh`.

OpenCode is baked via an **in-project SDK**. The agent SDKs ship embedded under
`internal/workshop/sdk/<agent>/` (each a `sdk.yaml` + `hooks/setup-base`). The
OpenCode `setup-base` hook installs OpenCode (`curl -fsSL
https://opencode.ai/install | bash`, then copies the binary to
`/usr/local/bin`). taboo embeds these trees (`//go:embed sdk`) and seeds the
configured agent's tree per run into `<ProjectDir>/.workshop/<agent>/` (i.e.
`.taboo/.workshop/<agent>/`). The in-project SDK is referenced as
**`project-opencode`** in the definition's `sdks:` list; workshop resolves it to
the bare **`opencode`** used for the `remount <ws>/opencode:<plug>` qualifier and
in `info`.

### Agent auth: env at exec time, never persisted

Credentials (`ANTHROPIC_API_KEY` and equivalents) are read from the host
environment / keyring and passed per run via `workshop exec --env`. They are
never written into `workshop.yaml` or baked into an image; they live only for the
duration of the exec. OpenCode authenticates from `OPENROUTER_API_KEY` in the env
alone (no `auth login`).

### Agent tool permissions: git push blocked by default

Agents run headless (`claude -p` and equivalents) with no interactive approver.
The Claude Code profile launches with `--permission-mode auto` — the agent edits
files and commits autonomously — plus a hard `--disallowedTools "Bash(git push
*)"` deny (deny outranks the auto-mode classifier). The deny is deliberate: a
*linked* worktree shares the host repo's object store and refs (see the three-mount
note above), so a `git push` from inside the workshop, forced or not, could
mutate host branches. taboo's contract is **commit in place; the host owns
integration** — the agent never needs to push.

The argv-level deny is wired only for the Claude Code and GitHub Copilot profiles
(`--disallowedTools "Bash(git push *)"` and `--deny-tool=shell(git push)`
respectively). The OpenCode profile carries no in-argv push deny and relies
solely on the workshop container as the boundary.

A workflow automation that *does* need to publish (push a branch, open a PR) must
add its own explicit `git push` stage on the host side, after the run — not rely
on the agent to push from inside the workshop.

### Session capture: redirect storage to a mounted dir

For session resume/fork, agent session files (normally in `$HOME` inside the
workshop, e.g. `~/.claude/projects/...`) must reach the host — but there is no
`copyOut`. taboo mounts a host sessions directory at a known path (another mount
plug) and points each agent's session storage there via env. Files write
straight through to the host using the same bind-mount trick as the worktree.

OpenCode resolves its data dir from `XDG_DATA_HOME` (via `xdg-basedir`); its
store is a single **SQLite DB** (`opencode/opencode.db` + WAL sidecars), *not*
loose per-session JSON. Setting `XDG_DATA_HOME` to the mount target redirects it
through the bind-mount and survives the per-run swap. Resume and fork thread a
caller-supplied session id to the agent (`--session <id>` / `--fork`) and read
the SQLite DB directly, since the store is opaque from the host.
`AgentProfile.Sessions()` returns the env var + on-disk subpath per agent
(`{XDG_DATA_HOME, "opencode"}` for OpenCode).

### Derive the workshop from the project's definition

So the agent can run the project's own validation flow (lint/format/test) inside
its loop — a TDD inner loop, not a commit-blind one — its workshop must carry the
project's toolchain, not just the agent CLI. taboo therefore **derives** the
agent's workshop from the project's *own* `workshop.yaml` instead of rendering a
minimal one: it reads the project definition and **opaque-tree injects** only the
agent SDK (with the mount plugs), a minted `name:`, and the inherited `base:`,
passing every other field (`actions:`, slots, custom plugs) through verbatim. The
result is the gitignored `.taboo/workshop.yaml`, regenerated every run. This buys
the invariant **the agent's sandbox is the dev's sandbox** and *inverts* taboo's
earlier ownership model (taboo augments the project's definition, it no longer
authors one). Settled in **ADR 0009**; key points:

- **Scope: workshop-using projects only.** A project without a `workshop.yaml` is
  a hard early error; taboo does not infer a toolchain.
- **In-project SDK resolution.** `project-<x>` SDKs the source def references are
  symlinked into `.taboo/.workshop/<x>` (reconciled every run); store SDKs need
  nothing. `clean` deletes the links, never recurses through them into the repo's
  real `.workshop/`.
- **Drift.** The derived def is fingerprinted; the long-lived workshop is
  `refresh`ed/relaunched when the project's `workshop.yaml` changes, reused
  otherwise.
- **Source-independent provisioning holds.** SDK setup hooks run before the
  project is mounted, so the `.taboo` quarantine cannot break toolchain install.
- `renderDefinition` retires for the managed-project flow.

## Known risks

- **Shared-rootfs toolchain conflict.** The agent SDK and the project's toolchain
  SDKs now provision the *same* rootfs (ADR 0009). A version/PATH clash (e.g. both
  install a runtime) is possible but low-probability; agent CLIs install to
  `/usr/local/bin` and most toolchains arrive via their own SDKs. Watch for it.
- **Cold toolchain cache every run.** The per-run `stop → remount → start` swap
  wipes the rootfs, so the toolchain's dependency caches (`GOMODCACHE`, npm cache,
  …) are cold on each run; the agent's first `make test` pays a `go mod download`
  / `npm ci`. Deferred optimization: a persistent host-side cache bind-mount (the
  sessions-mount pattern), which needs per-toolchain cache-path modeling.
- **Table-parsing fragility.** `workshop`'s `list` / `changes` / `tasks` emit
  human tables, not JSON. Prefer `info` / `actions` (real YAML) and minimize
  reliance on table output; watch for breakage across workshop versions.

## Deferred design decisions

Not workshop-specific; follow sandcastle's proven design when reached:

- **Branch-naming convention** — head / merge-to-head / named-branch strategies.

## Glossary

- **workshop** — Canonical's tool for ephemeral, declaratively-defined LXD dev
  environments. taboo's substrate.
- **SDK** — workshop's unit of installed dependency (versioned, store-sourced).
  taboo ships agents and the worktree mount as SDKs.
- **plug / slot** — workshop's connection model (snap-like). A `mount` plug with
  a `workshop-target` is how a host path is bound into a workshop.
- **remount** — the `workshop` CLI command that points an existing mount plug at
  a new host source path. taboo uses it to attach a worktree.
- **worktree** — a host git worktree, one per agent run, bind-mounted into a
  workshop as the agent's working directory.
- **sandcastle** — the TypeScript reference product taboo is modeled on.
- **AgentProfile** — taboo's per-agent abstraction (mirrors sandcastle's
  *AgentProvider*): one value describes how to build an agent's exec argv, its
  credential env vars, its session redirect, and the workshop SDK that bakes its
  CLI in.
