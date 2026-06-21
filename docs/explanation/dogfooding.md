# Dogfooding the agent loop

taboo drives its own development. Label an issue and an agent implements it;
label the resulting pull request and an agent reviews it. The loop that does this
is itself built on the thing taboo ships — the `taboo` library — so the flagship
pipeline is the library's own reference consumer. This page explains why the loop
is shaped the way it is, and where its trust boundary sits. For the command
surface — every `afk` subcommand and flag — see the
[afk reference](../reference/afk.md); for how a single run works underneath, see
the [isolation model](isolation-model.md).

## A Go orchestrator on the taboo library

The orchestration is a small Go application — the `afk` ("away from keyboard")
binary at `.taboo/orchestrator/`. It is not the `taboo` CLI and it is not new
library code: it is an ordinary, unit-tested *consumer* of `taboo`, the same way
an external integrator would build a pipeline. Each step of the loop is a
subcommand:

- `afk implement --issue N` — fetch the issue, run the agent, push the branch,
  open a draft PR, hand off to review.
- `afk review --pr N` — fetch the PR diff, run the review agent, post one review.
- `afk write-pr [--ready]` — (re)compose a PR description from a branch's diff;
  with `--ready`, the finalize step that lifts the draft.
- `afk update-branch --pr N` — merge `main` into a PR's branch and validate.
- `afk to-issues --issue N` — decompose a PRD issue into ready child issues.
- `afk loop` — the master orchestrator that drains the backlog (below).

The runs themselves go onto taboo through two bridge one-liners. `afk implement`
calls `taboo.RunWorkflow`, which discovers `.taboo/taboo.yaml`, resolves the
named workflow into a plan, and drives the run. The flows that need a structured
answer back — `review`, `write-pr`, `update-branch`, `to-issues` — call the
typed bridge `taboo.RunWorkflowAs[T]`, which threads a JSON extractor into the
run so the agent's `<result>` block comes back as a statically typed value with
no caller-side parsing. `afk loop` fans those runs out across a wave of issues
with `taboo.Pool`, bounded-parallel. The orchestrator never re-implements
worktrees, workshops, or completion detection — it consumes taboo's contract and
adds only the GitHub-shaped glue around it. The decision to build the loop this
way, rather than as bash around the `taboo run` CLI, is
[ADR 0010](https://github.com/josecabralf/taboo/blob/main/docs/adr/0010-go-orchestrator-on-pkg-taboo.md).

## Labels are the state machine; `loop` drains the backlog

The loop is label-triggered. The workflows in `.github/workflows/` —
`agent-implement.yml`, `agent-review.yml`, `agent-finalize.yml` — fire on a label
event and do nothing but check out, set up the workshop, plumb tokens, claim the
issue/PR with the `agent:in-progress`/`agent:blocked` bookkeeping, and run the
`afk` binary; all the real orchestration is inside Go. Applying `agent:implement`
to an issue claims it: the workflow swaps it to `agent:in-progress`, runs
`afk implement`, and the orchestrator pushes the branch, opens a draft PR whose
body is the agent's plan, and labels that PR `agent:review`. The review workflow
swaps the PR to `agent:in-progress`, runs `afk review`, posts one review, and
clears the label. On either side a failure adds `agent:blocked` and a run-link
comment; re-adding the trigger label retries.

The chain from implement to review — and CI on the resulting PR — is automatic,
but only because the implement flow applies the `agent:review` label with a
personal access token rather than the default `GITHUB_TOKEN`.

!!! warning "The single non-obvious wiring fact in the whole loop"

    An event created with `GITHUB_TOKEN` does not trigger another workflow —
    GitHub suppresses that to prevent recursive runs. So the cascade depends on
    `secrets.AGENT_PAT`; without it the PR is opened and labelled but nothing
    downstream fires — neither the PR's CI nor the review workflow wakes up. The
    [one-time setup](#human-one-time-setup) below calls it out.

`afk loop` is the same machine run autonomously. Where `implement` drives one
issue, `loop` drains the whole `ready-for-agent` backlog wave by wave: each wave
it plans the next parallel-safe batch, *claims* every issue in it (removing
`ready-for-agent`, adding `agent:in-progress`) so a later wave can never
re-select one in flight, fans the implement flow out across the batch through
`taboo.Pool`, and settles each run — success releases the in-progress label, a
failure also adds `agent:blocked` with a diagnostic comment. It repeats up to
`--max-iterations` waves; an empty plan means the queue is drained and it stops.

## The trust boundary: GitHub I/O is on the host, the agent is push-denied

taboo's contract is to run an agent inside a workshop and land its commits on a
host branch. It denies `git push` from inside the workshop, by design, because a
linked worktree shares the host repository's object store and a push could mutate
host branches directly (see [the isolation model](isolation-model.md)). That deny
is the load-bearing reason the loop is split the way it is.

Everything GitHub-shaped lives on the host, in the Go orchestrator, not in the
workshop and not in the agent:

- The **agent** explores, plans, does TDD, and commits in place. It never
  fetches an issue, pushes a branch, opens a PR, or posts a comment.
- The **orchestrator** does all of that, in Go: issue/diff fetch, branch name,
  prompt-variable injection, the `taboo` run, the branch push, the draft PR, the
  labels, and the posted review. Every `gh`/`git` side effect funnels through one
  package, `internal/ghio`, behind a fakeable seam — so the whole loop is
  unit-tested and runs locally, not scripted in untestable workflow YAML.

This keeps taboo *core* frozen: the orchestrator adds no push-or-GitHub code to
the `taboo` library — it is a consumer of it. The library still only runs an
agent in a workshop and lands commits; the host still owns publishing. If you
wanted a different host (say GitLab), you would rewrite `internal/ghio` and leave
`taboo` untouched.

Inputs reach the agent through taboo's variable substitution, never through the
shell. The orchestrator builds a variables map and the run fills the prompt's
`{{VAR}}` placeholders from it literally (`taboo.Substitute`), so an issue body
full of backticks, `$()`, or quotes is injected as data and cannot be
interpreted as a command (issue #61). An undefined placeholder when variables are
supplied is a hard error, which is why each prompt declares exactly the variable
set its flow provides.

## One config drives both the CLI and the orchestrator

There is exactly one `.taboo/taboo.yaml`, and it is the single source of truth
for both `taboo run` on the command line and `afk` as a library consumer. The
workshop, the agent profile, the model, the prompt files, and the
iteration/timeout/completion-signal knobs all resolve from that one file. When
`afk implement` runs the `implement` workflow, it discovers and loads the *same*
config a developer running `taboo run implement` by hand would — there is no
orchestrator-specific configuration and no second place for loop settings to
drift out of sync. Dogfooding is honest precisely because the autonomous loop and
the manual CLI read identical config.

## Why `afk` is a nested module under `.taboo/`

`afk` is a **nested** Go module — `module afk` at `.taboo/orchestrator/`, with a
`replace github.com/josecabralf/taboo => ../..` pinning it to the in-tree
library. It has to be nested under a dot-directory: Go tooling ignores
directories beginning with `.`, so the root module's `./...` (and therefore
`make build/test`) cannot see it. That isolation is the point — the example's
surface and its own `main` stay out of the library's public module, while
`replace` keeps it building against the very taboo in the same tree.

The cost is two ergonomic quirks: it is **not** `go run`-able from the repo root
(Go excludes nested modules), and because `./...` skips it, CI must build/vet/test
it with a dedicated step. The [afk reference](../reference/afk.md) covers how to
invoke it; [ADR 0010](https://github.com/josecabralf/taboo/blob/main/docs/adr/0010-go-orchestrator-on-pkg-taboo.md)
records the decision in full.

## The agent self-validates; the PR's CI is the gate

The agent's workshop is not a bare rootfs carrying only the agent CLI. taboo
derives it from the project's own `workshop.yaml`
([ADR 0009](https://github.com/josecabralf/taboo/blob/main/docs/adr/0009-derive-workshop-from-project-definition.md)), so the
derived definition inherits the project's toolchain — Go, `golangci-lint`, and
the `make` action — the same environment humans and CI use. "The agent's
sandbox is the dev's sandbox," which means the agent can run the project's checks
itself.

It does. As part of the TDD loop the implement skill drives, the agent formats
with `make fmt`, then runs `make lint test build` in its workshop, fixing what
they flag before it commits. This is the inner correction loop a headless agent
otherwise lacks: a broken build or a lint failure surfaces in seconds, not a CI
round-trip later.

The PR's CI is still the authoritative gate. `ci.yml` runs the same
`make lint`, `make test`, and `make build` (via `workshop run`) on every pull
request to `main`, against the branch the agent produced — the implement workflow
opens a *draft* PR precisely so CI runs and a human can look before anything
lands. The two are
complementary, not redundant (ADR 0009): the agent's inner loop raises diff
quality before the PR exists; CI is what proves the change on the way to merge.

Self-validation does not touch the trust boundary. Running `make` needs no GitHub
access and no push — the agent is still push-denied and still works only inside
its workshop, and the host orchestrator still owns every GitHub side effect.

## Inline comments are dropped, not errored

The review agent emits a `<result>` block shaped as a top-level summary plus an
array of inline comments, each carrying a `path` and a `line`. GitHub's review
API rejects the entire review if any inline comment points at a line that is not
part of the diff — and an LLM, reading a unified diff, occasionally anchors a
comment to a line just outside a hunk or on the deleted side.

So the `afk review` orchestrator filters before it posts. Its `internal/diffmap`
package parses the unified diff into the set of valid `path:line` positions —
every line present on the **right** (new) side of the diff, which is both added
and context lines — and drops any inline comment that misses that set, logging a
notice for each drop. Only the survivors, plus the summary, go into the one
review the orchestrator posts. A mis-anchored comment costs a notice, not a
failed run. And if nothing survives — an empty summary and every comment dropped
— it skips the post entirely rather than send an empty review GitHub would 422.
The choice is to degrade the review gracefully rather than lose it entirely to
one bad line number.

## Trust and security model

The whole loop rests on one control: **a label is authorization.** Applying a
label requires write access to the repository, so only a trusted collaborator can
start an agent run. There is no other gate — no allow-list, no comment-command
parser, no per-run approval. If you can label, you can run the agent.

Given that, the review agent's inputs are inside the trust boundary on purpose.
The issue was vetted by the maintainer who labelled it; the diff was produced by
our own implement agent on a branch we control. The review workflow consumes them
with write-scoped tokens because it has to post a review, and it does so trusting
that a maintainer's label vouched for them.

!!! danger "Not hardened for untrusted public forks"

    The review and finalize workflows use `pull_request_target`, which runs with
    the base repository's secrets even for a fork's PR. On a repository that
    accepts drive-by fork PRs, a labeller could be tricked into running agent
    code with access to the agent credential (`CLAUDE_CODE_OAUTH_TOKEN`) those
    jobs expose — the classic `pull_request_target` secret-exfiltration footgun.

The loop assumes a trusted-contributor repository where everyone who can label is
already trusted with secrets. Hardening it for open public contribution (forks,
sandboxed review of untrusted diffs, scoped-down tokens) is a separate project,
not part of this loop.

## Human one-time setup

Before the loop can run, a repository admin sets up four things by hand:

- **Create the four `agent:*` labels:** `agent:implement`, `agent:review`,
  `agent:in-progress`, and `agent:blocked`. The workflows assume they already
  exist.
- **Add the agent credential secret:** `CLAUDE_CODE_OAUTH_TOKEN`, the Claude
  subscription token (from `claude setup-token`) the Claude Code agent
  authenticates with inside the workshop. API users set `ANTHROPIC_API_KEY`
  instead.
- **Add `AGENT_PAT`:** a credential with a real identity, used to open the draft
  PR and apply the `agent:review` label (see the wiring note above — without it
  neither the PR's CI nor `agent-review` fires). Prefer a **fine-grained PAT
  scoped to this repository** with `Contents: read/write`, `Pull requests:
  read/write`, `Issues: read/write` (labels go through the issues API), and the
  baseline `Metadata: read` — or, better, a short-lived **GitHub App** token
  (`actions/create-github-app-token`). The identity must have write access to the
  repo. A classic `repo`-scoped PAT works too but is far broader than needed; this
  is a long-lived, write-capable credential, so the narrowest scope wins. It is
  used only by `agent-implement` (an `issues`-triggered job), not by the
  `pull_request_target` review and finalize jobs.
- **Confirm Actions permissions:** GitHub Actions must be allowed to push
  branches, open and label pull requests, and post reviews (workflow `contents`,
  `issues`, and `pull-requests` write permissions, plus repository settings that
  permit Actions to create and approve pull requests where required).

With those in place, labelling an issue `agent:implement` runs the whole loop end
to end.

## See also

- [afk reference](../reference/afk.md) — every orchestrator subcommand, its
  flags, and the environment it needs.
- [ADR 0010](https://github.com/josecabralf/taboo/blob/main/docs/adr/0010-go-orchestrator-on-pkg-taboo.md)
  — the decision to build the loop as a Go orchestrator on the library.
- [The isolation model](isolation-model.md) — push-denied workshops and why the
  host owns integration.
- [Why the API is shaped this way](design.md)
- [CLI reference](../reference/cli.md) — `taboo run` and its flags.
