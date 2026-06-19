# Dogfooding the agent loop

taboo drives its own development. Label an issue and an agent implements it;
label the resulting pull request and an agent reviews it. The whole loop is built
out of two GitHub Actions, two prompt files, two skills, and the existing
`taboo run` command — taboo core gains nothing. This page explains why the loop
is shaped the way it is, and where its trust boundary sits. For how a single run
works underneath, see the [isolation model](isolation-model.md).

## Two workflows and a label state machine

The loop is two label-triggered workflows in `.github/workflows/`:

- `agent-implement.yml` fires on an issue gaining the `agent:implement` label.
- `agent-review.yml` fires on a pull request gaining the `agent:review` label.

Four labels carry the state. Applying `agent:implement` to an issue claims it:
the implement workflow swaps the issue to `agent:in-progress`, runs
`taboo run implement` (opencode / qwen) inside a workshop on the runner,
pushes the agent's branch, opens a draft PR whose body is the agent's plan, and
labels that PR `agent:review`. The review workflow swaps the PR to
`agent:in-progress`, runs `taboo run review` (opencode / qwen-coder), posts a
single PR review, and clears the label. On either side a failure adds
`agent:blocked` and comments a run link plus a retry hint; re-adding the trigger
label retries.

The chain from implement to review — and CI on the resulting PR — is automatic,
but only because the implement workflow opens the PR and applies the
`agent:review` label with a personal access token rather than the default
`GITHUB_TOKEN`. An event created with `GITHUB_TOKEN` does not trigger another
workflow — GitHub suppresses that to prevent recursive runs. So the cascade
depends on `secrets.AGENT_PAT`; without it the PR is opened and labelled but
nothing downstream fires — neither the PR's CI nor the review workflow wakes up.
This is the single non-obvious wiring fact in the whole loop, and the one-time
setup below calls it out.

## Why the boundary is "scaffolding only"

taboo's contract is to run an agent inside a workshop and land its commits on a
host branch. It denies `git push` from inside the workshop, by design, because a
linked worktree shares the host repository's object store and a push could mutate
host branches directly (see [the isolation model](isolation-model.md)). That deny
is the load-bearing reason the loop is split the way it is.

Everything GitHub-shaped lives on the host, in the workflow YAML, not in taboo
and not in the agent:

- The **agent** explores, plans, does TDD, and commits in place. It never
  fetches an issue, pushes a branch, opens a PR, or posts a comment.
- The **workflow** does all of that: it fetches the issue body, computes the
  branch name, builds the variables file, invokes `taboo run`, pushes the branch,
  opens the draft PR, labels it, and (on the review side) posts the review.

This keeps taboo core frozen. The loop is config (`taboo.yaml`), prompts
(`.taboo/prompts/`), skills (`.agents/skills/`), and two Actions — no new
push-or-GitHub code anywhere in `pkg/taboo`. If you wanted a different host (say
GitLab), you would rewrite the workflow layer and leave taboo untouched.

Inputs reach the agent through taboo's variable substitution, never through the
shell. The workflows build a JSON variables file and pass it with
`taboo run --vars-file`. taboo fills the prompt's `{{VAR}}` placeholders from
that file literally, so an issue body full of backticks, `$()`, or quotes is
injected as data and cannot be interpreted as a command (issue #61). An undefined
placeholder when variables are supplied is a hard error, which is why each prompt
declares exactly the variable set its workflow provides.

## The agent self-validates; the PR's CI is the gate

The agent's workshop is not a bare rootfs carrying only the agent CLI. taboo
derives it from the project's own `workshop.yaml`
([ADR 0009](../adr/0009-derive-workshop-from-project-definition.md)), so the
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
its workshop, and the host workflow still owns every GitHub side effect.

## Inline comments are dropped, not errored

The review agent emits a JSON file shaped as a top-level comment plus an array of
inline comments, each carrying a `path` and a `line`. GitHub's review API rejects
the entire review if any inline comment points at a line that is not part of the
diff — and an LLM, reading a unified diff, occasionally anchors a comment to a
line just outside a hunk or on the deleted side.

So the review workflow filters before it posts. It computes the set of valid
`path:line` keys — every line present on the **right** (new) side of the diff,
which is both added and context lines — and drops any inline comment that misses
that set, logging a warning to the run log for each drop. Only the survivors, plus
the top-level comment, go into the one review the workflow posts. A
mis-anchored comment costs a warning, not a failed run. The choice is to degrade
the review gracefully rather than lose it entirely to one bad line number.

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

This is **not hardened for untrusted public forks.** The review workflow uses
`pull_request_target`, which runs with the base repository's secrets even for a
fork's PR. On a repository that accepts drive-by fork PRs, a labeller could be
tricked into running agent code with access to `OPENROUTER_API_KEY` and
`AGENT_PAT` — the classic `pull_request_target` secret-exfiltration footgun. The
loop assumes a trusted-contributor repository where everyone who can label is
already trusted with secrets. Hardening it for open public contribution (forks,
sandboxed review of untrusted diffs, scoped-down tokens) is a separate project,
not part of this loop.

## Human one-time setup

Before the loop can run, a repository admin sets up four things by hand:

- **Create the four `agent:*` labels:** `agent:implement`, `agent:review`,
  `agent:in-progress`, and `agent:blocked`. The workflows assume they already
  exist.
- **Add the agent credential secret:** `OPENROUTER_API_KEY`, the OpenRouter API
  key the opencode agent authenticates with inside the workshop.
- **Add `AGENT_PAT`:** a credential with a real identity, used to open the draft
  PR and apply the `agent:review` label (see the wiring note above — without it
  neither the PR's CI nor `agent-review` fires). Prefer a **fine-grained PAT
  scoped to this repository** with `Contents: read/write`, `Pull requests:
  read/write`, `Issues: read/write` (labels go through the issues API), and the
  baseline `Metadata: read` — or, better, a short-lived **GitHub App** token
  (`actions/create-github-app-token`). The identity must have write access to the
  repo. A classic `repo`-scoped PAT works too but is far broader than needed, and
  this credential is exposed in the `pull_request_target` review job, so the
  narrowest scope wins.
- **Confirm Actions permissions:** GitHub Actions must be allowed to push
  branches, open and label pull requests, and post reviews (workflow `contents`,
  `issues`, and `pull-requests` write permissions, plus repository settings that
  permit Actions to create and approve pull requests where required).

With those in place, labelling an issue `agent:implement` runs the whole loop end
to end.

## See also

- [The isolation model](isolation-model.md) — push-denied workshops and why the
  host owns integration.
- [Why the API is shaped this way](design.md)
- [CLI reference](../reference/cli.md) — `taboo run` and its flags.
