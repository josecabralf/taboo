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

    Two flags on `taboo run` build the variable map:

    | Flag | Value | Notes |
    |---|---|---|
    | `--vars-file <path>` | A JSON object of `{"VAR": "value"}` pairs. | Relative paths resolve against the `.taboo` config directory; absolute paths are used as given. |
    | `--var KEY=VALUE` | A single variable inline. | Repeatable. Overrides a matching `--vars-file` key. |

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
supplied, **every** `{{VAR}}` in the prompt must have a matching value — an
unresolved placeholder fails the run fast (the error names the missing
variable) rather than sending a half-filled prompt to the agent.

## Failure modes (CLI)

Each of the following aborts the run before anything executes:

- a `--vars-file` that does not exist, or that it cannot read;
- a `--vars-file` whose contents are not a JSON object of string values;
- a `--var` without an `=`, or with an empty key;
- a supplied set of variables that leaves any `{{VAR}}` in the prompt unresolved.

## Example

`.taboo/vars.json`:

```json
{
  "ISSUE_TITLE": "Parser drops trailing comments",
  "ISSUE_BODY": "Lines after a // comment are silently truncated."
}
```

A workflow prompt in `taboo.yaml`:

```yaml
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
  `(*ProjectConfig).Plan`, and `Substitute`.
