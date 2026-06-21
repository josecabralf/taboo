# afk orchestrator reference

`afk` is the AFK ("away from keyboard") agent loop that drives taboo's own
development. It is a small Go application built on the `taboo` library — a
**nested** Go module at `.taboo/orchestrator/` (`module afk`), not part of the
`taboo` binary. The GitHub Actions in `.github/workflows/` only check out, set up
the workshop, and plumb tokens; every step of the loop — including all GitHub
side effects — is ordinary, unit-tested Go. For why the loop is shaped this way,
see [Dogfooding the agent loop](../explanation/dogfooding.md) and
[ADR 0010](https://github.com/josecabralf/taboo/blob/main/docs/adr/0010-go-orchestrator-on-pkg-taboo.md).

This page is the command reference: every subcommand, its flags, and the
environment it needs.

## Invocation

`afk` dispatches on its first argument (stdlib `flag`, no cobra):

```text
afk implement --issue <n>
afk review --pr <n>
afk plan
afk write-pr [--branch <branch>] [--ready]
afk update-branch --pr <n>
afk to-issues --issue <n>
afk loop [--max-iterations <n>] [--parallelism <n>] [--dry-run]
```

`afk` is a nested module under `.taboo/`, so it is **not** `go run`-able from the
repo root ([why](../explanation/dogfooding.md#why-afk-is-a-nested-module-under-taboo)).
Run it one of two ways:

```bash
# from inside the module
cd .taboo/orchestrator && go run . implement --issue 85

# or build inside the module, run the binary from the repo root (what CI does)
( cd .taboo/orchestrator && go build -o "$RUNNER_TEMP/afk" . )
"$RUNNER_TEMP/afk" implement --issue 85
```

Either way, run from **inside the repository checkout** (the repo root is
simplest and matches CI). `afk` resolves the project by ascending from the
working directory to find `.taboo/taboo.yaml`; run it from outside the repo tree
and it fails with `taboo: no taboo.yaml found from <dir>`.

Every subcommand exits `0` on success and non-zero on failure (`main.go` maps a
returned error to a non-zero exit and prints it to stderr). A subcommand that
requires a number (`--issue`, `--pr`) fails fast with `--issue is required` /
`--pr is required` before any I/O when the flag is missing or non-positive.

## Environment

`afk` reads no environment of its own; it inherits the process environment and
forwards what its tools need.

| Variable | Used by | Meaning |
|---|---|---|
| `GH_TOKEN` | `gh` (via `internal/ghio`) | Token for all GitHub I/O — issue/PR fetch, branch push, PR create/edit, labels, reviews, comments. |
| `GH_REPO` | `gh` (via `internal/ghio`) | `owner/repo` the `gh` calls target. |
| `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` | the agent inside the workshop | The credential the configured agent authenticates with — the OAuth token (Claude subscription) or API key (API) for the `claude-code` agent in `taboo.yaml`. taboo forwards only the keys present in the environment. |

In CI these come from repository secrets. `CLAUDE_CODE_OAUTH_TOKEN` is the agent
credential. `GH_TOKEN` is set to `secrets.AGENT_PAT` (falling back to the default
`GITHUB_TOKEN`) on the GitHub-mutating steps: a label applied with the default
`GITHUB_TOKEN` does **not** trigger another workflow, so the `agent:review` label
the implement flow applies must go through a real-identity PAT to cascade into
`agent-review.yml`. See the
[one-time setup](../explanation/dogfooding.md#human-one-time-setup) for how
`AGENT_PAT` should be scoped.

All configuration — the workshop, agent profile, model, prompts, and the
iteration/timeout/completion-signal knobs — comes from the **same**
`.taboo/taboo.yaml` the `taboo` CLI reads (see
[taboo-yaml.md](taboo-yaml.md)). There is no orchestrator-specific config and no
second place to keep loop settings.

## implement

Drives one issue end-to-end: fetch it, have the agent implement it, push the
branch, open a draft PR, and hand off to review.

```text
afk implement --issue <n>
```

| Flag | Default | Meaning |
|---|---|---|
| `--issue` | `0` (required, must be > 0) | GitHub issue number to implement. |

1. **Fetch** the issue title/body via `gh issue view` (`internal/ghio`).
2. **Run** the `implement` workflow on `taboo` through `taboo.RunWorkflow`: the
   agent runs inside a taboo-provisioned workshop and **commits in place** — it
   is git-**push-denied**. The issue body reaches the prompt through taboo's
   variable substitution, never the shell, so backticks and `$()` in it are data,
   not commands.
3. **Push** the run's branch to `origin`.
4. **Open a draft PR** whose body is the agent's plan (read from
   `.taboo-plan.md` in the worktree), prefixed with `Closes #N`.
5. **Label** the PR `agent:review`, which (under a real-identity token) cascades
   into the review flow.

## review

Reviews one PR and posts exactly one review.

```text
afk review --pr <n>
```

| Flag | Default | Meaning |
|---|---|---|
| `--pr` | `0` (required, must be > 0) | GitHub pull-request number to review. |

1. **Fetch** the PR's unified diff via `gh pr diff` (`internal/ghio`).
2. **Run** the `review` workflow on `taboo` through the typed bridge
   `taboo.RunWorkflowAs[reviewResult]`, asking for a `<result>` block of
   `{summary, comments:[{path, line, body}]}` decoded in-loop.
3. **Drop** any inline comment whose `path:line` is not addressable in the diff
   (`internal/diffmap` — the new-side added and context lines), logging a notice
   for each. A mis-anchored comment costs a notice, never an error.
4. **Post** one PR review via `gh api`. An empty review (no summary and no
   in-diff comments survive) is **skipped**, so GitHub never 422s.

## plan

Lists the open `ready-for-agent` issues and prints the next parallel-safe batch
of them as JSON. Read-only — it claims nothing and touches no labels.

```text
afk plan
```

Takes no flags. The batch is the same selection `loop` runs each wave (see
below); `plan` is the preview of that selection. Output goes to stdout.

## write-pr

Composes a PR description from a branch's realized diff, and is the **finalize**
stage when run with `--ready`.

```text
afk write-pr [--branch <branch>] [--ready]
```

| Flag | Default | Meaning |
|---|---|---|
| `--branch` | `""` → the current branch | Branch to open or refresh a PR for. |
| `--ready` | `false` | After refreshing the PR, mark it ready for review (un-draft). An already-ready PR is a no-op. |

1. **Resolve** the branch and compute its diff against `main`
   (`git diff main...<branch>`). An empty diff is an error before any PR is
   touched.
2. **Run** the `write-pr` workflow on `taboo` through
   `taboo.RunWorkflowAs[prContent]`, asking for a `<result>` block of
   `{title, body}`. An empty title is an error.
3. **Update in place**: `PRForBranch` → `EditPR` refreshes the branch's existing
   open PR, so an idempotent re-run never opens a duplicate; only when none
   exists is a new PR created.
4. **`--ready`** (finalize): after the update, `gh pr ready` lifts the PR the
   implement flow opened as a draft out of draft. No push happens here — the
   branch is already on the remote, and finalize only reconciles the PR's
   description and draft state. Wired by the manually-applied
   `agent-finalize.yml`.

## update-branch

Brings a PR's branch up to date with `main`, merging and validating inside a
workshop before any push.

```text
afk update-branch --pr <n>
```

| Flag | Default | Meaning |
|---|---|---|
| `--pr` | `0` (required, must be > 0) | PR whose head branch to update with `main`. |

1. **Resolve** the PR's head branch via `gh pr view` and **fetch** `origin`, so
   `origin/main` and `origin/<branch>` are current.
2. **No-op gate**: if `origin/main` is already contained in the branch, do
   nothing — no workshop, no commit, no push — and exit 0.
3. **Run** the `update-branch` workflow on `taboo` through
   `taboo.RunWorkflowAs[updateBranchResult]`, with the worktree started at the PR
   branch's remote tip (`origin/<branch>`, via the bridge's `BaseRef` override):
   the agent merges `origin/main`, resolves conflicts, and validates the merged
   tree in-workshop (`make lint test build`), reporting
   `{updated, validated, summary}`. The agent is git-**push-denied**.
4. **Gate on validation**: if validation failed, label the PR `agent:blocked`
   with a diagnostic comment and **do not push**. If nothing was merged (a race),
   report and exit. Otherwise **push** the branch as a non-force fast-forward
   (`git push origin <branch>`) — distinct from the implement flow's force-push,
   so a remote branch that moved under us fails safely.

`update-branch` reuses the PR's branch name for its worktree (it is updating
*that* branch), so a second run for the same PR in a checkout that already has
the local branch fails on the worktree collision. CI avoids this by running from
a fresh `main` checkout; locally, `git worktree remove` the stale worktree before
re-running.

## to-issues

Decomposes a PRD-style parent issue into vertical-slice child issues.

```text
afk to-issues --issue <n>
```

| Flag | Default | Meaning |
|---|---|---|
| `--issue` | `0` (required, must be > 0) | GitHub PRD issue to decompose. |

1. **Fetch** the parent issue via `gh issue view`.
2. **Run** the `to-issues` workflow on `taboo` through
   `taboo.RunWorkflowAs[[]childIssue]`, asking for a `<result>` JSON array of
   `{title, body, blocked_by}` where `blocked_by` holds 0-based indices into the
   array (earlier entries only).
3. **Create** each child via `gh issue create` with the `ready-for-agent` label
   and a back-link to the parent, resolving each `blocked_by` index to the real
   created issue number and writing it into the child as a `Blocked by #N` line.

The `ready-for-agent` label is what `plan` and `loop` select on, so `to-issues`
feeds the loop's backlog.

## loop

The master orchestrator. Where `implement` drives one issue, `loop` drains the
whole `ready-for-agent` backlog wave by wave, fanning the implement flow out
bounded-parallel through `taboo.Pool`.

```text
afk loop [--max-iterations <n>] [--parallelism <n>] [--dry-run]
```

| Flag | Default | Meaning |
|---|---|---|
| `--max-iterations` | `10` | Maximum plan→fan-out waves before stopping — a safety bound against a queue that never empties. |
| `--parallelism` | `3` | Maximum concurrent implement runs per wave. |
| `--dry-run` | `false` | Plan and print the first batch without claiming, running, or touching any label. |

Each wave:

1. **Plan** the next parallel-safe batch (the same selection `plan` emits). An
   empty batch means the queue is drained, so the loop stops.
2. **Claim** every issue in the batch — remove `ready-for-agent`, add
   `agent:in-progress` — so a later wave's planner can never re-select one
   already in flight.
3. **Fan out** the implement workflow across the batch through `taboo.Pool`,
   bounded by `--parallelism`.
4. **Settle** each run: success releases `agent:in-progress`; failure also adds
   `agent:blocked` plus a diagnostic comment, taking the issue out of the ready
   pool until a human re-adds the label.

## See also

- [Dogfooding the agent loop](../explanation/dogfooding.md) — why the loop is a
  Go orchestrator, the trust model, and the single-source `taboo.yaml`.
- [ADR 0010](https://github.com/josecabralf/taboo/blob/main/docs/adr/0010-go-orchestrator-on-pkg-taboo.md)
  — the decision to replace the bash AFK loop with a Go orchestrator on
  `taboo`.
- [The isolation model](../explanation/isolation-model.md) — push-denied
  workshops and why the host owns integration.
- [taboo.yaml reference](taboo-yaml.md) — the config `afk` shares with the CLI.
- [Library API reference](library-api.md) — `RunWorkflow`, `RunWorkflowAs`,
  `Pool`, and the rest of the bridge `afk` builds on.
