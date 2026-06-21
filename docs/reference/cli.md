# taboo CLI reference

The `taboo` binary wraps the common library paths. The thin `cli/main.go`
entrypoint delegates to the application package `cli/internal/app`. The root
command is `taboo` ("taboo orchestrates agent runs inside workshop
environments", `cli/internal/app/main.go`). It registers six subcommands:
`doctor`, `init`, `validate`, `run`, `list`, `clean`
(`cli/internal/app/main.go`, `newRootCmd`).

Every command exits `0` on success and `1` on failure. The process exits
non-zero whenever a command returns an error; `Execute` in
`cli/internal/app/main.go` maps any returned error to `os.Exit(1)`. The root
sets `SilenceErrors` and `SilenceUsage`, so cobra does not print usage on
failure.

!!! info "Where these facts come from"
    Every command, flag, and message below is read from the source named in
    each section (under `cli/internal/app/`). The flag tables mirror the
    `cobra` definitions verbatim — run `taboo <command> --help` to confirm.

For the `taboo.yaml` schema that `run`, `validate`, `list`, and `clean` read,
see [taboo-yaml.md](taboo-yaml.md). For agent names and credential keys, see
[agents.md](agents.md).

## init

Scaffold a `.taboo/` project into a repository (`cli/internal/app/init.go`,
`newInitCmd`). It writes `taboo.yaml`, `.gitignore`, and `.env.example`, seeds
the example workflow prompt files unless opted out, and optionally scaffolds a
Go `main.go` and `go.mod`. It never launches a workshop.

```
taboo init [flags]
```

Positional arguments: none (`cobra.NoArgs`).

| Flag | Default | Meaning |
|---|---|---|
| `--agent` | `""` | Agent to scaffold for (`opencode`, `claude-code`, `github-copilot`). Required non-interactively. |
| `--model` | `""` | Model passed to the chosen agent. Required non-interactively. |
| `--base` | `ubuntu@24.04` | Workshop base image (`defaultBase` in `cli/internal/app/init.go`). |
| `--repo` | current directory | Host repository path to scaffold into; resolved to an absolute path. |
| `--workshop` | derived from the repo directory name | Workshop name; slugified from the repo base name by `deriveWorkshopName`. |
| `--source-definition` | `""` | Named `.workshop/*.yaml` definition to derive from. Required (non-interactively) only when the repo has more than one. |
| `--workflows` | seeds `fix` and `refactor` | Pass `none` to skip seeding the example workflows and their prompt files. |
| `--template` | `none` | Go scaffold to write: `none`, `single`, or `fanout`. An unknown value is rejected before any side effect. |
| `--force` | `false` | Regenerate the scaffold files in an existing `.taboo` directory. |
| `--dry-run` | `false` | List the files `init` would write without writing them. |

When stdin is a real TTY and `--agent` or `--model` is missing, `init` runs an
interactive `huh` wizard (`cli/internal/app/init.go`, `runInitCmd`). When stdin is not
a TTY (a pipe, redirect, or `< /dev/null`), a missing required flag fails fast
naming every missing flag: `missing required flags: ... (pass them or run init
interactively)`. A fully flagged invocation skips the wizard even at a TTY.

`init` refuses to overwrite an existing `.taboo` directory unless `--force` is
set: `.taboo already exists at PATH; pass --force to regenerate its scaffold
files` (`ensureWritable`). An unknown agent is reported with the valid set:
`unknown agent "X"; valid agents: claude-code, github-copilot, opencode`
(`resolveProfile`, sorted from `taboo.AgentNames()`). A multi-definition repo
with no `--source-definition` selected is rejected non-interactively:
`multiple workshop definitions (...): pass --source-definition to pick one`.

Output routing: progress, the scaffold confirmation, and next steps go to
stdout (`printNextSteps`); errors go to stderr prefixed with `Error:`. A
`--dry-run` invocation prints `taboo init (dry run) — would write:` followed by
the absolute path of each planned file (`printDryRun`). There is no `--json`
flag.

Exit behaviour: non-zero on a missing required flag, an unknown agent or
template, a refused overwrite, or a write failure.

## run

Run a workflow (or an ad-hoc prompt) end-to-end on a fresh branch
(`cli/internal/app/run.go`, `newRunCmd`). It loads `taboo.yaml`, selects what to
run, resolves the run parameters into a `Plan` through the library's config→run
bridge, runs a host preflight, and drives the plan through the library's
`(*Plan).Run` looped run on a new per-run branch.

```
taboo run [workflow] [flags]
```

!!! warning "`run` takes a workflow name, not a path"
    The positional argument is a workflow **name** defined in `taboo.yaml`
    (e.g. `taboo run fix`), not a directory or file path. A bare `taboo run`
    uses the configured `default-workflow`; `--prompt` runs ad-hoc with no
    workflow.

Positional arguments: an optional `workflow` name (`cobra.MaximumNArgs(1)`). A
bare `run` with no positional argument runs the configured `default-workflow`,
or, when `--prompt`/`--prompt-file` is set, an ad-hoc run off the top-level
config. With neither a positional workflow, a prompt flag, nor a
`default-workflow`, `run` errors listing the available workflows
(`noSelectionError`).

| Flag | Default | Meaning |
|---|---|---|
| `--prompt` | `""` | Run instruction, overriding any configured prompt. |
| `--prompt-file` | `""` | File whose contents are the run instruction, resolved relative to the `.taboo` directory (absolute paths used verbatim). |
| `--vars-file` | `""` | JSON file of `{"VAR":"value"}` pairs substituted literally into `{{VAR}}` placeholders in the prompt (no shell expansion). Path resolved relative to the `.taboo` directory. |
| `--var` | none | Repeatable `KEY=VALUE` template variable substituted literally into `{{KEY}}`. Overrides a matching `--vars-file` key. |
| `--agent` | `""` | Override the resolved agent for this run. |
| `--model` | `""` | Override the resolved agent's model for this run. |
| `--timeout` | `0` | Override the per-exec timeout, e.g. `30m` (Go duration). Zero leaves it unset. |
| `--iterations` | `0` | Override the max iteration cap. Zero or less leaves it unset. |
| `--signal` | `""` | String that, when it appears in agent output, stops the iteration loop early. |
| `--branch` | auto-generated | Branch name for this run. The default is composed of the prefix, the workflow (or `adhoc`), and a timestamp. |
| `--from` | `""` | The workshop definition to derive the agent workshop from; overrides `taboo.yaml`'s source-definition for this run. |
| `--dry-run` | `false` | Resolve and print the plan without running anything. |
| `--yes` | `false` | Skip the interactive pre-run confirmation. |
| `--json` | `false` | Emit the run result as JSON. |

Parameter precedence: a CLI flag overrides the workflow block, which overrides
the top-level config and the `defaults:` block (`cli/internal/app/run.go`
packs the flags into `taboo.PlanOverrides`; the library bridge applies
flag-then-workflow-then-top-level to agent, model, and the rest). Template
variables are layered last: `--var KEY=VALUE` flags override matching
`--vars-file` keys, and the merged map is substituted into the resolved
prompt's `{{VAR}}` placeholders (`resolveVars`). A malformed `--var` (not
`KEY=VALUE`) or an unreadable/invalid `--vars-file` fails fast before the run.

Preflight (`runPreflight`): `workshop --version` must report at least
`minWorkshopVersion` (`0.9.1`, `cli/internal/app/version.go`), then the run-scoped
config-correctness checks run (`runConfigChecks`: the `validate` set with the
whole-config prompt-file and derive checks omitted, since `cfg.Plan` already
resolved the one prompt and workshop this run consumes). On any error check, the preflight report is written to stderr and
the run is refused with `errRunFailed`. At a TTY without `--yes`, `run` then
prints a one-line summary to stderr and reads a `y/N` answer from stdin; a blank
line, EOF, or anything but `y`/`yes` declines and prints `Aborted.`
(`confirmRun`, `promptYesNo`). A non-interactive caller or `--yes` proceeds
without prompting. A `--dry-run` invocation prints the resolved plan and never
reaches the preflight.

Output routing: live agent output (both the agent's stdout and stderr) and all
progress stream to stderr, so the machine result on stdout stays clean
(`planOverrides` points the plan's `Stdout` and `Stderr` at `env.Stderr`, and
`executeRun` drives the plan). The machine result goes to stdout. The plain
form is two lines (`writeRunResult`):

```
branch: taboo/fix-20260617-101500-000000001
commit: 1f3c9ab
```

The plain form omits the captured agent output (it already streamed to stderr).
With `--json`, stdout carries an indented JSON object (`jsonRunResult`):

```json
{
  "branch": "taboo/fix-20260617-101500-000000001",
  "commit": "1f3c9ab",
  "worktree": "/home/you/project/.taboo/worktrees/taboo-fix-20260617-101500-000000001",
  "output": "captured agent stdout",
  "iterations": 1,
  "stopReason": "max-iterations"
}
```

The `stopReason` field is the `StopReason` flattened to a string
(`max-iterations` or `signal`). A `--dry-run` plan prints to stdout under
`taboo run (dry run) — resolved plan:` with one aligned label per line
(`printPlan`).

Exit behaviour: non-zero on a config/selection error, a preflight failure
(`errRunFailed`), or a failure inside the run. A declined confirmation returns
zero (nothing ran).

## validate

Validate the project's `taboo.yaml` for correctness
(`cli/internal/app/validate.go`, `newValidateCmd`). It discovers and
strict-decodes the config, then runs the agent, model, prompt-file, repo, and
workshop-derive correctness checks. It does not probe host tooling; that is
`doctor`.

```
taboo validate [flags]
```

Positional arguments: none (`cobra.NoArgs`).

| Flag | Default | Meaning |
|---|---|---|
| `--json` | `false` | Emit the report as JSON. |

Checks (`validateChecks` -> `configCorrectnessChecks`):

- `config`: the `taboo.yaml` is discoverable, readable, and strict-decodes
  (unknown keys rejected, single document, non-empty). A failure here is
  terminal and no further checks run.
- `agent`/`agent/<name>`: every referenced agent resolves to a registered CLI.
  An unknown agent fails with a fuzzy suggestion when one is close:
  `unknown agent "X"; did you mean "Y"?` (`agentChecks`,
  `unknownAgentMessage`).
- `model/<agent>`: a missing model is a hard failure
  (`agent "X" has no model configured (model is required)`).
  `model/<agent>/<model>`: a model that does not match the agent's format hint
  is a `warn`, not an error (`modelChecks`, `MatchModelFormat`).
- `prompt-file/<path>`: every referenced prompt file exists, resolved relative
  to the config file's directory (`promptFileChecks`).
- `repo`/`repo-path`/`repo-git`: the repo must be set, on persistent storage
  (not under `/tmp` or `/run`), and a git work tree (`repoValidateChecks`).
- `source-definition`/`derive`: a `<repo>/workshop.yaml` source must exist, and
  the agent workshop must derive cleanly from it. The derivation is dry-run in
  memory (no launch, no FS writes); a malformed source is a hard `derive` error
  (`deriveChecks`).

Output routing: the report goes to stdout (`renderReport`). The human form
prints one line per check as `[STATUS] NAME MESSAGE` and a `result: OK` or
`result: FAIL` footer (`writeHuman`). With `--json`, stdout carries
`{"ok": bool, "checks": [{"name", "status", "message"}]}` (`writeJSON` in
`cli/internal/app/report.go`), where `status` is `ok`, `warn`, or `error`.

Exit behaviour: non-zero (`errValidateFailed`) when any check is an `error`. A
`warn` does not fail the command.

## doctor

Check host readiness for running taboo (`cli/internal/app/doctor.go`,
`newDoctorCmd`). It runs the always-on host checks and, inside a taboo project,
config-aware checks.

```
taboo doctor [flags]
```

Positional arguments: none (`cobra.NoArgs`).

| Flag | Default | Meaning |
|---|---|---|
| `--json` | `false` | Emit the report as JSON. |

Host checks (`hostChecks` in `cli/internal/app/host.go`), all `error` on failure:

- `workshop`: `workshop --version` is runnable and reports at least
  `minWorkshopVersion` (`0.9.1`). A failure or too-old version names the install
  or refresh command.
- `lxd`: `lxc version` succeeds (LXD installed).
- `lxd-reachable`: `lxc info` succeeds (daemon running, initialized). Skipped as
  a failure when LXD is not installed.
- `git`: `git --version` succeeds.
- `go`: `go version`. A missing Go toolchain is a `warn`, not an error (it is
  only needed to scaffold or run `main.go`).

Config-aware checks (`configChecks` in `cli/internal/app/config.go`), only when
a `taboo.yaml` is discoverable from the working directory:

- `config`: the config loads through `taboo.LoadConfig`.
- `credentials/<agent>`: a `warn` per referenced agent that has none of its
  credential env keys set (`credentialChecks`).
- `repo-path`/`repo-git`: the configured repo is on persistent storage and is a
  git work tree (`repoChecks`).
- `workshop-project`: the configured repo has a `<repo>/workshop.yaml`. This is a
  presence-only check (it does not derive the workshop — that is `validate`'s
  job) and is an `error` when the file is missing (`workshopProjectChecks`).

Output routing: the report goes to stdout, same human/`--json` shapes as
`validate`.

Exit behaviour: non-zero (`errChecksFailed`) when any check is an `error`.
Warnings (a missing Go toolchain, missing credentials) do not fail the command.

## list

List the project's workshops, worktrees, and branches
(`cli/internal/app/list.go`, `newListCmd`). Read-only: it loads the config,
probes the host through the command seam, and mutates nothing.

```
taboo list [flags]
```

Positional arguments: none (`cobra.NoArgs`).

| Flag | Default | Meaning |
|---|---|---|
| `--json` | `false` | Emit the listing as JSON. |

Sections (`runList`):

- workshops: one entry per distinct agent the config references, named
  `<workshop>-<agent>` (`projectWorkshops`, `workshopName`). The state
  comes from `workshop --project <projectDir> info <name>`; a probe error means
  `not provisioned`, an unparseable status means `unknown` (`workshopState`).
- worktrees: the worktrees under `<projectDir>/worktrees/`, from
  `git worktree list --porcelain` (`gatherWorktrees`).
- branches: the branches under the configured `branch-prefix`, from
  `git for-each-ref refs/heads/` (`gatherBranches`). An empty prefix returns
  every branch.

Output routing: the listing goes to stdout. The human form prints a header and
the three sections, each falling back to `(none)` when empty
(`renderListResult`). With `--json`, stdout carries
`{"workshops": [{"name","status"}], "worktrees": [{"branch","path"}],
"branches": []}` (`jsonListResult`); empty sections marshal as `[]`.

Exit behaviour: non-zero on a config-load error or a fatal git probe error. A
workshop-info probe error is not fatal (it reports `not provisioned`).

## clean

Tear down the project's taboo-managed worktrees, workshops, and branches
(`cli/internal/app/clean.go`, `newCleanCmd`). By default it removes only the
worktrees.

```
taboo clean [flags]
```

Positional arguments: none (`cobra.NoArgs`).

| Flag | Default | Meaning |
|---|---|---|
| `--workshops` | `false` | Tear down the project's workshops instead of its worktrees. |
| `--all` | `false` | Tear down both the worktrees and the workshops. |
| `--prune-branches` | `false` | Also delete the run branches under the configured `branch-prefix`. |
| `--force` | `false` | Delete branches even when not merged. |
| `--dry-run` | `false` | Print the plan without removing anything. |
| `--yes` | `false` | Skip the interactive confirmation. |

Scope (`buildCleanPlan`): worktrees are removed with `git worktree remove`;
`--workshops` switches to tearing down the derived workshops (`workshop remove`)
instead; `--all` does both. Tearing down workshops also removes any in-project
SDK quarantine symlinks under `<projectDir>/.workshop/` (only confirmed
symlinks, never their targets — `discoverSDKLinks`). `--prune-branches` deletes
the prefix branches with `git branch -D`. `--prune-branches` requires a
configured `branch-prefix`;
without one it refuses (`--prune-branches needs a configured branch-prefix;
without one every branch would match`). Unmerged branches are refused without
`--force`: `refusing to prune N unmerged branch(es) without --force: ...`.

At a TTY without `--yes`, `clean` prints a teardown summary to stderr and reads
a `y/N` answer before any destructive action; declining prints `Aborted.`
(`confirmClean`). A `--dry-run` invocation prints the plan to stdout under
`taboo clean (dry run) — would:` and never mutates anything (`printCleanPlan`).

Output routing: `--dry-run` and the `Nothing to clean.` message go to stdout;
per-artifact progress (`removed worktree ...`, `tore down workshop ...`,
`removed SDK link ...`, `deleted branch ...`) and warnings go to stderr
(`executeClean`). There is no `--json` flag.

Exit behaviour: teardown is best-effort. A failure on one artifact warns and
continues; every failure is joined into the returned error so the command still
exits non-zero (`executeClean`). A refused prune or an unset `branch-prefix`
fails before any mutation. A declined confirmation returns zero.
