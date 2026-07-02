# Run variables: `{{VAR}}` substitution

A resolved prompt may contain `{{VAR}}` placeholders. Caller-supplied values
fill those placeholders before the prompt reaches the agent, so one workflow
prompt serves many concrete runs (a GitHub issue, a PR diff, a ticket id)
without editing `taboo.yaml`.

Substitution is performed by `taboo.Substitute` (the `internal/prompt.Substitute`
function, re-exported on the public facade). Both the CLI and the library route
through it.

## The placeholder grammar

A placeholder is `{{` `VAR` `}}`, where `VAR` is an identifier: a leading letter
or underscore, then letters, digits, or underscores (the regexp
`[A-Za-z_][A-Za-z0-9_]*`). Text that does not match — `{{ VAR }}` with inner
spaces, `{{1ST}}`, `{{a-b}}` — is not a placeholder and is left untouched.

There are **no built-in variables**. Every value is caller-supplied; nothing is
auto-populated from the environment, the run, or the config. A placeholder with
no supplied value is an error (see [Every supplied placeholder must
resolve](#every-supplied-placeholder-must-resolve)).

## Supplying values

=== "Library"

    Pass a `map[string]string` of `{VAR: value}` to the call that resolves the
    prompt — `RunWorkflow`, `RunWorkflowAs[T]`, or `(*ProjectConfig).Plan`. Each
    key fills the matching `{{key}}` placeholder.

    ```go
    vars := map[string]string{
        "ISSUE_TITLE": "Parser drops trailing comments",
        "ISSUE_BODY":  "Lines after a // comment are silently truncated.",
    }
    res, err := taboo.RunWorkflow(
        ctx, ".", "triage", vars, taboo.PlanOverrides{}, taboo.NewExecCommander(),
    )
    ```

    `Substitute` is also exported directly for callers that hold the template
    themselves:

    ```go
    filled, err := taboo.Substitute("Title: {{ISSUE_TITLE}}", vars)
    ```

=== "CLI"

    Two flags on `taboo run` build the variable map
    (`cli/internal/app/run.go`):

    | Flag | Value | Notes |
    |---|---|---|
    | `--vars-file <path>` | A JSON object of `{"VAR": "value"}` pairs. Default `""` (unset). | A relative path resolves against the config file's directory (the directory holding `taboo.yaml`: the repo root for a bare `taboo.yaml`, or `.taboo/` when nested); an absolute path is used as given. |
    | `--var KEY=VALUE` | A single variable inline. Repeatable, default none. | Overrides a matching `--vars-file` key. |

    The library path takes no flags: a Go caller passes one pre-merged
    `map[string]string`, so the `--var`-over-`--vars-file` layering below is the
    CLI's `resolveVars` job, not something taboo does internally.

    ```sh
    taboo run triage --vars-file vars.json --var ISSUE_TITLE="Parser drops comments"
    ```

## Precedence

Substitution happens *after* the prompt itself is resolved. The prompt
precedence chain runs first — inline `--prompt`/`--prompt-file` over the
workflow over the `defaults:` block — and the winning text is what the
placeholders are filled into.

Among the variables themselves (CLI only), `--var` overrides `--vars-file`: a
`--var NAME=x` wins over a `"NAME"` key in the file, so a file of defaults can be
overridden one key at a time on the command line.

## Values are literal, never expanded

Supplied values are inserted **verbatim** in a single pass. A value that itself
contains `$(...)`, backticks, `$VAR`, or another `{{X}}` is placed into the
prompt as-is — it is never evaluated, shell-expanded, or re-substituted. taboo
hands the prompt to the agent through argv or stdin
([ADR 0001 — the argv + stdin command contract](https://github.com/josecabralf/taboo/blob/main/docs/adr/0001-agentprofile-argv-stdin-command-contract.md)),
not through a shell, so there is no shell to expand the injected values.

!!! info "Why a single literal pass matters"
    This is what makes substitution safe for untrusted text — a GitHub issue
    body, a PR diff, an arbitrary comment — that may contain shell
    metacharacters or look like a placeholder. It lands in the prompt as plain
    text.

## Every supplied placeholder must resolve

With no variables supplied, the prompt is passed through untouched, so a prompt
that legitimately contains `{{...}}` is left alone. As soon as *any* variable is
supplied, **every** `{{VAR}}` in the prompt must have a matching value. An
unresolved placeholder fails the run fast with
`prompt template: undefined variable(s): <names>` (`internal/prompt/prompt.go`),
naming the missing variables rather than sending a half-filled prompt to the
agent.

Substitution applies to the resolved prompt regardless of where it came from, so
`{{VAR}}` placeholders in a `prompt-file`'s contents are filled the same as an
inline `prompt`.

## Discovering a workflow's variables

Nothing has to be run to learn which variables a workflow's prompt takes.

=== "Library"

    `taboo.Placeholders` returns the distinct `{{VAR}}` names a template
    references, sorted ascending — the same grammar `Substitute` fills, so text
    that is not a placeholder is ignored by both:

    ```go
    names := taboo.Placeholders("Title: {{ISSUE_TITLE}}\n\n{{ISSUE_BODY}}")
    // []string{"ISSUE_BODY", "ISSUE_TITLE"}
    ```

    A resolved `taboo.Plan` also carries the placeholder set of its
    pre-substitution prompt in `Plan.Placeholders` (the post-substitution
    `Request.Prompt` no longer shows them).

=== "CLI"

    Three surfaces report a workflow's variables:

    - **`taboo run <workflow> --dry-run`** prints a `vars:` line in the plan.
      It shows `(none)` for a placeholder-free prompt; the placeholder names
      with a `(supplied)` marker when variables were passed (plus any
      supplied-but-unused keys, e.g. `unused: TYPO`); or the names marked
      `(unfilled — will pass through literally; supply --var/--vars-file)` when
      none were passed.
    - **A real `taboo run`** warns on stderr — the run itself is unchanged —
      when a supplied key matches no placeholder in the prompt, or when no
      variables were supplied and the prompt carries placeholders (which then
      reach the agent literally, per the no-vars rule below). The warning
      appears before the interactive confirmation, so a run started with the
      wrong keys can be aborted.
    - **`taboo validate`** emits an OK-level `vars/<workflow>` check naming the
      placeholders each workflow's effective prompt references (inline
      `prompt` or the contents of an existing `prompt-file`, falling back to
      the `defaults:` block). Placeholder-free workflows emit nothing.

## Failure modes (CLI)

These checks run during plan resolution, before anything executes, so they also
fail a `--dry-run` preview, not just a real run. Each error is quoted verbatim:

- a `--vars-file` that does not exist or cannot be read: `read vars-file: <err>`;
- a `--vars-file` whose contents are not a JSON object of string values:
  `parse vars-file <path>: <err>`;
- a `--var` without an `=`, or with an empty key:
  `invalid --var "<kv>": want KEY=VALUE`;
- a supplied set of variables that leaves any `{{VAR}}` in the prompt unresolved:
  `prompt template: undefined variable(s): <names>`.

## Example

`.taboo/vars.json`:

```json title=".taboo/vars.json"
{
  "ISSUE_TITLE": "Parser drops trailing comments",
  "ISSUE_BODY": "Lines after a // comment are silently truncated."
}
```

A workflow prompt in `taboo.yaml`:

```yaml title="taboo.yaml"
workflows:
  triage:
    prompt: |
      Title: {{ISSUE_TITLE}}

      {{ISSUE_BODY}}
```

Run it, overriding one value inline:

```sh
taboo run triage --vars-file vars.json --var ISSUE_TITLE="Parser drops comments"
```

The agent receives the prompt with both placeholders filled: the title taken
from the `--var` override and the body from the file.

## See also

- [`taboo.yaml` reference](reference/taboo-yaml.md) — where workflow prompts and
  the `defaults:` prompt are configured.
- [CLI reference](reference/cli.md) — the full `run` flag set and resolution
  order.
- [Library API reference](reference/library-api.md) — `RunWorkflow`,
  `(*ProjectConfig).Plan`, `Substitute`, and `Placeholders`.
