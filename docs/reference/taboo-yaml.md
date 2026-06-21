# taboo.yaml reference

`taboo.yaml` is the parsed project config read by both the CLI and Go callers
that drive runs through `pkg`. The schema is defined by `ProjectConfig`
in `internal/config/config.go`. `taboo.LoadConfig(path)` reads it, decodes it
strictly, and resolves the agent and model of the top level and of every
workflow into an `AgentProfile`.

The CLI discovers the file by ascending from the working directory: either the
directory holds `taboo.yaml`, or an ancestor holds `.taboo/taboo.yaml`
(`cmd/taboo/config.go`, `findConfig`). `taboo init` writes it under `.taboo/`.

For the commands that read this file, see [cli.md](cli.md). For agent names and
the model each expects, see [agents.md](agents.md).

## Top-level keys

`ProjectConfig` (`internal/config/config.go`). The YAML key is the struct tag in each
row.

| Key | Type | Default | Meaning |
|---|---|---|---|
| `workshop` | string | none | Workshop name taboo provisions runs in. |
| `base` | string | none | Workshop base image, e.g. `ubuntu@24.04`. |
| `repo` | string | none | Host git repository path whose worktrees the agent operates on. |
| `agent` | string | none | Default agent name, resolved against the registry. |
| `model` | string | none | Default model passed to the resolved agent. |
| `strategy` | string | `branch` | Branch-strategy seam; accepts any value for forward compatibility. |
| `defaults` | mapping (`RunDefaults`) | omitted (nil) | Scalar run settings applied when a workflow or flag does not override them. |
| `workflows` | mapping (`Workflow`) | omitted | Named, reusable task types keyed by workflow name. |
| `default-workflow` | string | `""` | Workflow run when the CLI selects none. |

`strategy` defaults to `branch` in `LoadConfig` when omitted (`defaultStrategy`,
`internal/config/config.go`). `agent` and `model` are resolved to a profile only where
an agent is set; an empty agent leaves the resolved `Profile` nil without error.
Enforcing a required agent is the `validate` command's job, not the loader's.

`Profile` (the resolved top-level profile) is not serialized (`yaml:"-"`). It is
populated by `LoadConfig`, not read from the file.

## defaults

The `defaults:` block is `RunDefaults` (`internal/config/config.go`): scalar-only run
settings applied when a workflow or flag does not override them. The whole block
is optional; when omitted, `Defaults` is nil and every default is its zero
value.

| Key | Type | Default | Meaning |
|---|---|---|---|
| `branch-prefix` | string | `""` | Prefix for branches taboo creates for a run. Also gates `taboo clean --prune-branches`. |
| `prompt` | string | `""` | Inline default instruction for a run. |
| `prompt-file` | string | `""` | Path to a file whose contents are the run instruction. |
| `timeout` | duration string | `0` | Bounds a single agent invocation, e.g. `30m`. |
| `max-iterations` | int | `0` | Caps how many times the agent is re-run for one task. |
| `completion-signal` | string | `""` | String whose appearance in agent output ends the run early. |

Both `prompt` (inline) and `prompt-file` exist here and at the workflow level to
mirror the CLI's `--prompt` and `--prompt-file` flags; the `run` command
resolves their precedence.

`timeout` is a `Duration`, a config-friendly `time.Duration` that parses Go
duration strings such as `30m` or `1h30m` through `time.ParseDuration`; an empty
value yields zero (`internal/config/config.go`, `Duration.UnmarshalYAML`). It marshals
back to a Go duration string (`Duration.MarshalYAML`).

## workflows

The `workflows:` block is a map from workflow name to `Workflow`
(`internal/config/config.go`). A workflow overrides the top-level run parameters for a
named task type.

| Key | Type | Default | Meaning |
|---|---|---|---|
| `prompt` | string | `""` | Inline instruction for this workflow. |
| `prompt-file` | string | `""` | Path to a file whose contents are the instruction. |
| `model` | string | `""` | Overrides the top-level model for this workflow. |
| `agent` | string | `""` | Overrides the top-level agent for this workflow. |
| `max-iterations` | int | `0` | Overrides the default iteration cap for this workflow. |
| `timeout` | duration string | `0` | Overrides the default per-invocation timeout, e.g. `30m`. |

A workflow has no `branch-prefix` and no `completion-signal` field: those live
only in `defaults` and as CLI flags. A workflow with no agent set anywhere
(neither its own `agent` nor a top-level `agent`) leaves its resolved `Profile`
nil (`resolveProfiles`). Like the top level, `Profile` is not serialized
(`yaml:"-"`).

## default-workflow

`default-workflow` (string) names the workflow `taboo run` selects when given no
positional workflow and no prompt flag (`cmd/taboo/run.go`, `selectRun`). When
it names a workflow that is not defined, `run` errors. When it is empty and no
workflow is named, a bare `run` errors listing the available workflows.

## Precedence chain

For a run, each scalar parameter resolves through three layers, highest first
(`cmd/taboo/run.go`, the `resolve*` helpers):

1. The CLI flag (`--prompt`, `--model`, `--agent`, `--timeout`, `--iterations`,
   `--signal`, `--branch`).
2. The selected workflow block.
3. The top-level config and the `defaults:` block.

Two parameters resolve through fewer layers because they have no workflow field:

- `completion-signal` resolves flag-then-defaults only (`resolveSignal`).
- `branch-prefix` lives only in `defaults` and feeds the auto-generated branch
  name (`resolveBranch`); `--branch` overrides the whole name verbatim.

The prompt resolves through six layers, first non-empty wins: flag inline, flag
file, workflow inline, workflow file, defaults inline, defaults file
(`resolvePrompt`). A prompt-file path is read relative to the config file's
directory unless it is absolute.

Agent and model also apply this chain when resolving the run's profile: flag,
then workflow, then top level (`effectiveAgent`, `resolveModel`, via `orElse`).

## Strict decode

`LoadConfig` decodes the document strictly (`decodeStrict`,
`internal/config/config.go`):

- Unknown keys are rejected. The decoder sets `KnownFields(true)`, so any key
  not in the schema fails with `taboo: invalid config` (`ErrConfigParse`).
- Only a single YAML document is supported. A trailing document (anything after
  a `---` separator) is rejected:
  `multiple YAML documents not supported`.
- A read failure (for example a missing path) wraps `taboo: cannot read config`
  (`ErrConfigRead`).

An empty document decodes to the zero config without error through
`LoadConfig`. The `validate` command decodes the same struct itself and instead
treats an empty document as an error (`config is empty`, `decodeValidate` in
`cmd/taboo/validate.go`).

## Seeded example

`taboo init` writes this `taboo.yaml` when seeding the example workflows
(`cmd/taboo/scaffold.go`, `renderTabooYAML`). The `workshop`, `repo`, and
`model` values are filled from the flags or wizard answers.

```yaml
# taboo.yaml — generated by `taboo init`.
# This is the single source of truth for this taboo project.
# Edit it directly; run `taboo doctor` to validate your changes.

workshop: my-project
base: ubuntu@24.04
repo: /home/you/my-project
agent: opencode
model: openrouter/qwen/qwen3-coder-plus
strategy: branch
workflows:
    fix:
        prompt-file: prompts/fix.md
    refactor:
        prompt-file: prompts/refactor.md
default-workflow: fix
```

`taboo init` marshals this through `yaml.v3`, which indents nested mappings by
four spaces. The seeded `.taboo/.gitignore` lists `worktrees/`, `.workshop/`, `.env`, and
`logs/` (`renderGitignore`). The `.env.example` header names the chosen agent
and lists one `KEY=` line per credential env key the agent reads
(`renderEnvExample`).

## Minimal example

The smallest config the loader and `validate` accept names an agent, a model, a
repo, and one workflow.

```yaml
workshop: my-project
base: ubuntu@24.04
repo: /home/you/my-project
agent: opencode
model: openrouter/qwen/qwen3-coder-plus
workflows:
  fix:
    prompt: "Investigate the failing tests and fix the bug."
default-workflow: fix
```

`strategy` is omitted, so `LoadConfig` defaults it to `branch`. The `defaults:`
block is omitted, so `branch-prefix`, `timeout`, `max-iterations`, and
`completion-signal` take their zero values until a workflow or a CLI flag sets
them.
