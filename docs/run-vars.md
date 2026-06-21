# Injecting template variables into a run prompt

`taboo run` can fill `{{VAR}}` placeholders in the resolved prompt with
caller-supplied values, so one workflow prompt serves many concrete runs
(a GitHub issue, a PR diff, a ticket id) without editing `taboo.yaml`.

Two flags supply the values:

- `--vars-file <path>` — a JSON object of `{"VAR": "value"}` pairs.
- `--var KEY=VALUE` — a single variable inline; repeatable.

Both substitute their values into the prompt's `{{VAR}}` placeholders: a
`{{ISSUE_TITLE}}` in the prompt is replaced by the `ISSUE_TITLE` value.

## Precedence

Variable injection happens *after* the prompt itself is resolved. The normal
prompt precedence chain runs first — `--prompt`/`--prompt-file` flag > 
workflow > defaults — and whichever prompt wins is the text the placeholders
are filled into.

Among the variables themselves, `--var` overrides `--vars-file`: a
`--var NAME=x` wins over a `"NAME"` key in the file, so a file of defaults can be
overridden one key at a time on the command line.

## Values are literal, never expanded

Injected values are inserted **verbatim**. Substitution is a single pass, so a
value that itself contains `$(...)`, backticks, `$VAR`, or another `{{X}}` is
placed into the prompt as-is and is **never** evaluated, shell-expanded, or
re-substituted. `taboo run` fills the values with a single literal pass
(`taboo.Substitute`) and hands the prompt to the agent through argv/stdin (see
`docs/adr/0001-agentprofile-argv-stdin-command-contract.md`) — not through a
shell. So there is no shell to expand the injected values.

This is what makes injection safe for untrusted text — a GitHub issue body, a PR
diff, an arbitrary comment — that may contain shell metacharacters or look like a
placeholder. It lands in the prompt as plain text.

## Paths and resolution

A relative `--vars-file` path resolves against the `.taboo` config directory
(the same base as `--prompt-file`), so the same invocation works from any
subdirectory of the project. An absolute path is used as given. A missing file,
malformed JSON, or a `--var` without an `=` aborts the run before anything
executes.

## Once a var is supplied, every placeholder must resolve

With no vars given the prompt is passed through untouched, so a prompt that
legitimately contains `{{...}}` is left alone. But as soon as *any* var is
supplied, **every** `{{VAR}}` in the prompt must have a matching value — an
unresolved placeholder fails the run fast rather than sending a half-filled
prompt to the agent.

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

The agent receives the prompt with both placeholders filled, the title taken
from the `--var` override and the body from the file.
