# Session resume/fork: a `{ResumeSession, Fork}` command contract

## Status

accepted

## Context & decision

taboo persists agent sessions through a bind-mounted sessions directory (PRD #1,
slice 5 / #8). This slice (#10) adds **resume** (continue a prior session by id so
the agent picks up where it left off) and **fork** (continue a prior session into
a *new* one, on a fresh branch/worktree, so a divergent continuation is isolated
from the source).

The decision is to extend the existing `AgentProfile` command seam (ADR 0001)
rather than special-case OpenCode. Two agent-agnostic inputs are added to
`CommandOptions` (the `BuildCommand` input) and mirrored on `RunRequest`:

```go
type CommandOptions struct {
    Prompt        string
    ResumeSession string // continue this session id (empty = fresh)
    Fork          bool   // when resuming, fork into a new session (ignored without ResumeSession)
}
```

Each `AgentProfile.BuildCommand` maps `{ResumeSession, Fork}` into that agent's
own CLI dialect; the orchestration code (`Runner.Exec`, `Orchestrator`, `Pool`)
stays agent-neutral and simply threads `RunRequest.ResumeSession`/`Fork` through.

**Fork has two independent halves, and taboo owns one of them unconditionally:**

- *Filesystem isolation* — a **fresh branch ⇒ fresh worktree**. `Runner.Setup`
  already allocates a new worktree per `RunRequest.Branch`, so fork is, at the
  orchestration level, just "resume + a new branch." This half is identical for
  every agent and needs no agent support.
- *Session isolation* — the agent forks the conversation so the source session is
  not mutated. This is the agent's job, expressed by `Fork`. Agents with a native
  headless fork render it as a flag; agents without one degrade to
  filesystem-only isolation (see Consequences).

## Roster: resume/fork across the embedded SDK agents

Verified against each CLI's docs/`--help`/source (only OpenCode is wired up as a
Go profile today; the rest are confirmed so the contract is known to generalize,
exactly as the prompt-delivery table in ADR 0001 did). The headless invocation is
carried over from ADR 0001.

| Agent       | Resume by id            | Native headless fork | `BuildCommand` mapping of `{ResumeSession=s, Fork=true}` |
|-------------|-------------------------|----------------------|---------------------------------------------------------|
| opencode    | `--session <id>`        | `--fork`             | `--session s --fork` (chosen; implemented)              |
| claude-code | `--resume <id>`         | `--fork-session`     | `--resume s --fork-session`                             |
| pi          | `--session <id>`        | `--fork <id>`        | `--fork s` (the fork flag *takes* the id)              |
| codex       | `exec resume <id>`      | none in `exec`       | `exec resume s` + emulate/degrade fork                  |
| copilot     | `--resume <id>`         | none                 | `--resume s` + emulate/degrade fork                     |

Notes that shaped the contract:

- **Resume is universal**; only the surface differs (flag vs. subcommand). A
  single `ResumeSession string` covers all five.
- **`Fork` is a boolean, not an id**, because the agent already has the source id
  (`ResumeSession`). This absorbs pi's quirk, where the id is the *argument* to
  `--fork`, as cleanly as OpenCode's separate `--fork` flag.
- **Resume by id, not "continue last."** Every CLI also has a recency-based
  continue (`-c`/`--continue`/`--last`) that is not id-addressable; taboo always
  has the id, so it targets the deterministic resume-by-id path. (pi is a trap:
  its `-r/--resume` is an *interactive picker*; the id-targeted flag is
  `--session`.)
- **Codex/Copilot have no headless fork.** Codex's fork is TUI-only
  (`codex exec fork` is open upstream); Copilot has none. For these, `Fork=true`
  must be emulated by copying the on-disk session store to a new id before
  resuming, or it degrades to filesystem-only isolation.

## Considered options

- **`{ResumeSession string, Fork bool}` on the existing seam (chosen).** Smallest
  surface that expresses every agent's resume *and* fork without an interface
  change, reusing the `BuildCommand` seam ADR 0001 built for exactly this ("the
  sessions slice adds `ResumeSession`/`ForkSession` non-breakingly"). Fork stays
  composable: `RunRequest{Branch: new, ResumeSession: s, Fork: true}`.

- **`ResumeSession string` only; fork = resume + a new branch (no session fork).**
  The literal #10 phrasing. Rejected: 3 of 5 agents fork the *session* natively,
  and without it a "fork" silently appends to and mutates the source
  conversation — not the isolated divergence the user asked for. Worktree
  isolation alone does not protect the shared session store.

- **A dedicated `ForkRequest` type or `Runner.Fork` method.** More discoverable,
  but a second request type to keep in sync with `RunRequest` for no added
  expressiveness — fork is fully described by the two fields plus a fresh
  `Branch`. Rejected as ceremony; kept as plain composition.

- **`ResumeSession` + `ForkSession string` (two ids).** ADR 0001's tentative
  naming. Rejected: the two ids would always be equal (you fork the session you
  resume), so the second field is redundant; a `Fork bool` is the honest shape.

## Consequences

- `Runner.Exec` builds `CommandOptions{Prompt, ResumeSession, Fork}` from the
  `RunRequest`; the orchestration layer (`Orchestrator`, `Pool`) inherits the
  resume/fork *plumbing* for free because both embed `RunRequest`. The semantics,
  however, do not come entirely for free:
  - A *looped* fork is rejected. `Orchestrator.Run` re-execs the unchanged
    `RunRequest` each iteration, so `Fork` with `MaxIterations > 1` would re-fork
    the source session every iteration rather than continue the fork; it returns
    `ErrForkLoop`. A single-iteration fork, or a multi-iteration plain resume
    (which mutates the source in place and so accumulates monotonically), is fine.
  - Fan-out fork across `Pool` slots is *not* free. Each slot has its own
    `ProjectDir` and therefore its own session store (`sessionsDir`), so a source
    session id only exists in the slot that created it. Forking one session into N
    divergent continuations across slots requires first replicating that session
    store into each slot — which depends on the session-id capture follow-up below.
- `Fork` without `ResumeSession` is ignored (an agent can only fork a session it
  is continuing); the OpenCode profile drops the flag in that case and it is
  argv-asserted in tests.
- **Resume mutates the source session; fork does not.** Callers who want the
  source conversation preserved must set `Fork` (on an agent that supports it).
- **Session-id capture is out of scope here.** `ResumeSession` is caller-supplied;
  taboo does not yet surface the id a run created on `RunResult`. The roster shows
  the id is reachable per agent (a stdout JSON field — OpenCode/claude `session_id`,
  codex `thread.started`, pi session header — or the newest file under the mounted
  store named by `SessionSpec`). Surfacing it on `RunResult` is a clean follow-up
  that does not change this contract.
- **Reversible per agent.** Because resume/fork live behind `BuildCommand`, a
  later codex/copilot profile can choose native-when-available, emulate-by-copy,
  or degrade-to-worktree-only without touching `RunRequest` or the orchestrator.
- Tested test-first with the `fakeCommander` sequence assertions
  (`Runner`-level: id reaches exec, fork allocates a new worktree) and direct
  `BuildCommand` argv assertions (resume/fork/fork-without-resume), matching the
  prior-art split in `runner_test.go` and `agent_test.go`.
