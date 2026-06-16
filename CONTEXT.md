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
2. **Bind-mounts** that worktree into a workshop at a fixed path (`/workspace`).
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

The primary deliverable is a Go module (`pkg/taboo`). The audience is engineers
building agent pipelines in Go, who want to express fan-out, review loops, and
custom orchestration in code. A CLI may follow but is not the primary contract.

### Working directory & results: bind-mount, commit in place

workshop has no file-copy primitive, but it does have writable **bind-mounts**
via `remount`. taboo uses this instead of sandcastle's copy-in / extract-commits
model:

- The worktree lives on the host and is bind-mounted into the workshop.
- The agent commits inside it; commits are immediately on the host.
- No sync machinery, no `copyOut`, no `git bundle` tricks for the main flow.

This sidesteps workshop's missing copy primitive entirely and matches its grain.

**Two mounts are required, not one.** A *linked* git worktree is **not
self-contained**: its `.git` is a pointer to the main repo's
`.git/worktrees/<name>`, and its objects/refs live in the main `.git`. Mounting
only the worktree's working dir yields `fatal: not a git repository` inside. So
taboo bind-mounts **both**:

1. the worktree → a fixed target (`/workspace`);
2. the repo's main `.git` (the git *common dir*) → mounted at its **identical
   host absolute path** inside the workshop.

Mounting `.git` at the same absolute path makes the worktree's `.git` pointer
resolve identically inside and on the host, so **no pointer rewriting** is needed
and the worktree stays valid on *both* sides. (Rewriting the pointer to a fixed
in-workshop path like `/gitcommon` also lets the commit succeed, but it breaks
the worktree for host-side git — rejected.) Implication: the git-common mount's
`workshop-target` is **per-repo** (it equals the host `.git` path), so it is
templated into `workshop.yaml` per repo, not fixed. This couples a persistent
workshop to one repo unless all managed repos live under a single host parent
that is mounted at its identical path. ~~Revisit this when
persistent-reuse-across-repos is built.~~ Resolved in ADR 0006: multi-repo reuse
is **not** built — one workshop per repo; the common-parent mount is the recorded
design of record only if that stance reverses.

**Mount-plug mechanics.** A `mount` plug is declared **inline in `workshop.yaml`**
under any SDK entry (`plugs: { <name>: { interface: mount, workshop-target:
<path> } }`) — no custom mount-SDK needs authoring; it auto-connects to
`system:mount` and gets a default host source, which `remount` then repoints.
taboo owns this `workshop.yaml` template. Two `remount` caveats:

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
  cost down; ~~workshop's ZFS snapshot/clone may make warm clones cheap (to be
  measured)~~ — resolved in ADR 0006: warm-clone fan-out is deferred, blocked
  upstream (no `workshop` clone / `launch --from` verb; the Go client shares the
  same API surface; direct LXD coupling is rejected).

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
Only bind-mounts (worktree, git-common, sessions, secrets) survive a
`stop`/`refresh`.

OpenCode is baked via an **in-project SDK** authored with `workshop sketch-sdk …
--eject --name opencode` (lives at `.workshop/opencode/`: `sdk.yaml` +
`hooks/setup-base`). Its `setup-base` hook installs OpenCode (`curl -fsSL
https://opencode.ai/install | bash`, then copies the binary to
`/usr/local/bin`). taboo ships these files embedded (`//go:embed sdk`) and seeds
them into each managed project dir. The in-project SDK is referenced as
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
*linked* worktree shares the host repo's object store and refs (see the two-mount
note above), so a `git push` from inside the workshop, forced or not, could
mutate host branches. taboo's contract is **commit in place; the host owns
integration** — the agent never needs to push.

A workflow automation that *does* need to publish (push a branch, open a PR) must
add its own explicit `git push` stage on the host side, after the run — not rely
on the agent to push from inside the workshop.

### Session capture: redirect storage to a mounted dir

For session resume/fork, agent session files (normally in `$HOME` inside the
workshop, e.g. `~/.claude/projects/...`) must reach the host — but there is no
`copyOut`. taboo mounts a host sessions directory at a known path (a second mount
plug) and points each agent's session storage there via env. Files write
straight through to the host using the same bind-mount trick as the worktree.

OpenCode resolves its data dir from `XDG_DATA_HOME` (via `xdg-basedir`); its
store is a single **SQLite DB** (`opencode/opencode.db` + WAL sidecars), *not*
loose per-session JSON. Setting `XDG_DATA_HOME` to the mount target redirects it
through the bind-mount and survives the per-run swap. Session ids are captured
from `opencode run --format json` stdout (`sessionID`), since the SQLite store is
opaque from the host. `AgentProfile.Sessions()` returns the env var + on-disk
subpath per agent (`{XDG_DATA_HOME, "opencode"}` for OpenCode).

## Build order

The **full sandcastle feature set is the target spec**, built iteratively,
tracer-bullet first. The walking skeleton proves the entire risk surface before
any feature work.

1. ✅ **Walking skeleton.** Built as the Go library `pkg/taboo` (deep
   `Runner.Run`, `Commander` seam, embedded agent SDK), test-first: unit tests on
   a fake `Commander`, plus a build-tagged integration test (`go test -tags
   integration`) that runs the whole path against real workshop — a deterministic
   shell agent's commit lands on the host branch, and a second integration test
   exercises the real OpenCode agent when `OPENROUTER_API_KEY` is set.
2. **Iteration loop + completion-signal** early stop.
3. **Structured output extraction** (ADR 0002: generics over `encoding/json`,
   the caller's struct is the schema). A `ResultExtractor` parses the last
   `<result>{...}</result>` block from the agent's final output and
   `JSONResult[T]` decodes it into `T`; `Validator` + `WithStrictFields()` add
   opt-in validation; no new dependency.
4. **Parallel fan-out.** N workshops, worktree-per-agent, caller-driven
   concurrency. **Sessions constraint:** the host sessions dir is
   `<ProjectDir>/sessions`, shared by every run in a ProjectDir, so concurrent
   runs must not share one — give each its own ProjectDir (or a per-slot
   sessions subdir), else they share OpenCode's single SQLite session DB and can
   corrupt it with interleaved WAL writes.
5. ✅ **Session capture + resume/fork** (OpenCode) via the mounted sessions dir.
   Resume-by-id and fork are exercised end-to-end by
   `TestIntegration_OpenCodeResumeFork` (gated on `OPENROUTER_API_KEY`): it
   captures a run's session id from the host store, resumes it on a fresh
   worktree and asserts the conversation continued, then forks it and asserts the
   source session is untouched and the fork lands on its own branch.
6. **Hooks + prompt templating** ergonomics.

## Open questions & risks (to verify)

1. **Configurable session/home path — other agents.** Session redirect depends on
   each agent honoring a configurable session/home location (e.g.
   `CLAUDE_CONFIG_DIR`). Verified for OpenCode (`XDG_DATA_HOME`, see
   [Session capture](#session-capture-redirect-storage-to-a-mounted-dir));
   verify per remaining agent (Claude Code, Codex).
2. **Table-parsing fragility.** `list` / `changes` / `tasks` emit human tables
   (no JSON). Prefer `info` / `actions` (real YAML) and minimize reliance on
   table output; watch for breakage across workshop versions.
3. **Persistent reuse across repos.** The git-common mount target is per-repo, so
   a persistent workshop couples to one repo unless all managed repos share a
   host parent mounted at its identical path. Revisit when this is built.

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
  *AgentProvider*). One value fully describes an agent: build its exec argv
  (`BuildCommand`), its credential env vars (`CredentialEnvKeys`), its session
  redirect (`Sessions` → env var + on-disk subpath), and the workshop **SDK**
  that bakes its CLI in (`Name`, which doubles as the SDK qualifier). The model
  is baked in at construction (`OpenCode(model)`). OpenCode is the first concrete
  profile.
- **agent registry** — the `pkg/taboo` lookup that resolves a canonical agent
  name to its **AgentProfile** and enumerates the supported names. The name is
  one identity with `AgentProfile.Name()` and the SDK qualifier — no separate
  alias. Fuzzy "did you mean" matching lives in the CLI, not the registry.
- **result block** — a delimited span in the agent's output (default
  `<result>…</result>`) whose JSON payload is the run's structured result. The
  **last** one in the final iteration's output is authoritative.
- **ResultExtractor** — taboo's structured-output abstraction (ADR 0002; see
  Build order step 3). A pure function over the agent's output that finds the
  result block and decodes/validates it into a typed value; constructed by
  `JSONResult[T]`. Distinguishes *no block* (`ErrNoResult`) from *invalid block*
  (`ErrInvalidResult`).
