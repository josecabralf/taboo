# Agents reference

taboo supports four agents: `opencode`, `claude-code`, `github-copilot`, and
`codex`. Each is one `AgentProfile` implementation (`internal/agent/agent.go`);
construct one through the public `NewProfile(name, model)`. The profiles live in
`internal/agent/agent_opencode.go`, `agent_claudecode.go`,
`agent_githubcopilot.go`, and `agent_codex.go`; the declarative roster that ties
names to constructors is in `internal/agent/registry.go`.

The generated godoc is the rendered source of truth:
<https://pkg.go.dev/github.com/josecabralf/taboo>.

## Summary

| Agent | `Name()` | Construct with | Credential env keys | Prompt delivery | Sessions `DirEnv` / `Subdir` | Model-hint `expected` | Fork |
|---|---|---|---|---|---|---|---|
| OpenCode | `opencode` | `NewProfile(taboo.OpenCode, model)` | `OPENROUTER_API_KEY` | argv | `XDG_DATA_HOME` / `opencode` | `<provider>/<model>, e.g. openrouter/qwen/qwen3-coder-plus` | native (`--fork`) |
| Claude Code | `claude-code` | `NewProfile(taboo.ClaudeCode, model)` | `ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN` | stdin | `CLAUDE_CONFIG_DIR` / `projects` | `a Claude model id or family alias, e.g. claude-sonnet-4-6 or sonnet` | native (`--fork-session`) |
| GitHub Copilot | `github-copilot` | `NewProfile(taboo.GitHubCopilot, model)` | `COPILOT_GITHUB_TOKEN`, `GH_TOKEN`, `GITHUB_TOKEN` | argv (value of `-p`) | `COPILOT_HOME` / `session-state` | none (never warns) | ignored |
| Codex | `codex` | `NewProfile(taboo.Codex, model)` | `OPENAI_API_KEY` | stdin | `CODEX_HOME` / `sessions` | `an OpenAI model id, e.g. gpt-5-codex or o4-mini` | ignored |

No agent can push from inside the workshop: `claude-code` and `github-copilot`
deny `git push` at the command level, while `opencode` and `codex` carry no
command-level deny and rely on the workshop container as their only boundary.
Credential env keys reach the agent via `workshop exec --env NAME`, which silently
drops any key that is unset on the host, so a user forwards only the credential
they hold.

!!! warning "Session capture assumes env-based auth"
    `Sessions()` relocates an agent's whole config/home directory onto the host
    sessions mount. That is safe only because taboo authenticates each agent
    through the credential env keys above. Do not pair it with an interactive
    login (`claude /login`, `copilot login`): that would persist credentials into
    the host-bound captured directory.

## OpenCode

Source: `internal/agent/agent_opencode.go`.

`Name()` returns `opencode`. Construct it with `NewProfile(taboo.OpenCode, model)`.

Credential env keys (`CredentialEnvKeys()`): `OPENROUTER_API_KEY`. OpenCode
authenticates from this key in the environment.

Prompt delivery: argv. `BuildCommand` renders
`opencode run --log-level ERROR -m <model>` and appends the prompt positionally;
`AgentCommand.Stdin` is empty. A resume id appends `--session <id>`, and a fork
adds `--fork` on top of it.

Sessions (`Sessions()`): `DirEnv` is `XDG_DATA_HOME`, `Subdir` is `opencode`,
and the second return value is `true`. Pointing `XDG_DATA_HOME` at the sessions
mount captures OpenCode's whole data directory, including its SQLite session DB.

Model-hint `expected`: `<provider>/<model>, e.g. openrouter/qwen/qwen3-coder-plus`.
The hint pattern is `^[^/]+/.+$`, so a value with no leading provider segment
(for example a bare `gpt-4`) does not match and `taboo validate` warns.

Fork: native. `--fork` applies only when continuing a session.

Push deny: no command-level deny. OpenCode runs its tools freely inside the
isolated workshop; its argv carries no `git push` deny, so the workshop container
is the only boundary.

## Claude Code

Source: `internal/agent/agent_claudecode.go`.

`Name()` returns `claude-code`. Construct it with `NewProfile(taboo.ClaudeCode, model)`.

Credential env keys (`CredentialEnvKeys()`): `ANTHROPIC_API_KEY` and
`CLAUDE_CODE_OAUTH_TOKEN`, in that order. `ANTHROPIC_API_KEY` is for API users;
`CLAUDE_CODE_OAUTH_TOKEN` (from `claude setup-token`) is for subscription users.
Returning both needs no configuration branching: `workshop exec --env NAME` drops
whichever is unset, and when both are set Claude Code prefers the API key. The
API key is listed first to mirror that precedence.

Prompt delivery: stdin. `BuildCommand` renders
`claude -p --output-format text --model <model> --permission-mode auto
--disallowedTools "Bash(git push *)"` and sets `AgentCommand.Stdin` to the
prompt; the prompt never rides in argv. A resume id appends `--resume <id>`, and
a fork adds `--fork-session` on top of it.

Sessions (`Sessions()`): `DirEnv` is `CLAUDE_CONFIG_DIR`, `Subdir` is `projects`,
and the second return value is `true`. `CLAUDE_CONFIG_DIR` is the only relocation
env var Claude exposes; it captures the whole config directory. Transcripts land
under `projects/<project>/<session>.jsonl`.

Model-hint `expected`: `a Claude model id or family alias, e.g. claude-sonnet-4-6
or sonnet`. The hint pattern is `(?i)claude|^(sonnet|opus|haiku)`, so any value
containing `claude` or starting with a bare family alias matches; a foreign id
warns.

Fork: native (`--fork-session`).

Push deny: yes, via `--disallowedTools "Bash(git push *)"`. A deny outranks
`--permission-mode auto`. The single `*` spans all arguments, so bare
`git push`, `git push origin main`, and `--force`/`-f` in any position are
blocked.

## GitHub Copilot

Source: `internal/agent/agent_githubcopilot.go`.

`Name()` returns `github-copilot`. Construct it with `NewProfile(taboo.GitHubCopilot, model)`.

Credential env keys (`CredentialEnvKeys()`): `COPILOT_GITHUB_TOKEN`, `GH_TOKEN`,
and `GITHUB_TOKEN`, in Copilot's own documented precedence order. `workshop exec
--env NAME` drops whichever are unset; when several are set, Copilot's precedence
picks the first present.

Prompt delivery: argv, as the value of `-p`. `BuildCommand` renders
`copilot --model <model> --allow-all --deny-tool=shell(git push) --output-format
text -s` and then appends `-p <prompt>`; `AgentCommand.Stdin` is empty. `-p` is
always emitted, even when the prompt is empty. A resume id appends
`--resume=<id>` before the `-p` prompt. Copilot itself rejects an empty `-p`
value, exiting 1 with `No prompt provided`, so a resume run must still supply a
prompt.

Sessions (`Sessions()`): `DirEnv` is `COPILOT_HOME`, `Subdir` is `session-state`,
and the second return value is `true`. `COPILOT_HOME` is the only env var Copilot
exposes to relocate its home directory; it captures the entire home. Session
transcripts land under `session-state/`.

Model hint: none. `copilotHint` is the no-opinion hint (nil pattern), because
Copilot proxies models from many providers. `taboo validate` never warns on a
Copilot model, and `MatchModelFormat(taboo.GitHubCopilot, ...)` returns `expected` as `""`.

Fork: ignored. Copilot has no native headless fork, so `CommandOptions.Fork` is
not consulted in `BuildCommand`. Setting `Fork` has no effect; the run proceeds
as a non-forked resume (or a fresh run).

Push deny: yes, via `--deny-tool=shell(git push)`. Denial rules take precedence
over `--allow-all`, and Copilot approves shell commands on a first-level
subcommand basis, so this one pattern blocks every `git push` form.

## Codex

Source: `internal/agent/agent_codex.go`.

`Name()` returns `codex`. Construct it with `NewProfile(taboo.Codex, model)`.

Credential env keys (`CredentialEnvKeys()`): `OPENAI_API_KEY`. Codex authenticates
from this key in the environment. ChatGPT-subscription auth is the interactive
`codex login` flow (which writes `auth.json` under `CODEX_HOME`) and is unusable
headless, so it is excluded.

Prompt delivery: stdin. `BuildCommand` renders
`codex exec --dangerously-bypass-approvals-and-sandbox --model <model> -` and sets
`AgentCommand.Stdin` to the prompt; the trailing `-` is Codex's stdin sentinel, so
the prompt never rides in argv. The `-` is kept even for an empty prompt so a
resume never drops into the interactive TUI. `codex exec` streams progress to
stderr and prints only the final agent message to stdout as plain text, so the
`<result>` block reaches stdout literally with no `--json` and no output parser. A
resume id maps to the `exec resume <id>` subcommand; `--model` and the bypass flag
are global args, so they stay valid after the subcommand.

`--dangerously-bypass-approvals-and-sandbox` runs fully autonomous: there is no
human approver headless, and Codex's `workspace-write` sandbox would block the
commit, whose objects land in the shared store in the parent-repo mount, outside
the agent's cwd. The ephemeral workshop is the security boundary.

Sessions (`Sessions()`): `DirEnv` is `CODEX_HOME`, `Subdir` is `sessions`, and the
second return value is `true`. `CODEX_HOME` (default `~/.codex`) is the env var
Codex exposes to relocate its home; it captures the whole home. Session JSONL
lands under `sessions/YYYY/MM/DD/`.

Model-hint `expected`: `an OpenAI model id, e.g. gpt-5-codex or o4-mini`. The hint
pattern is `(?i)gpt|codex|^o[0-9]`, so a gpt-*/codex-* id or an o-series id
matches; a foreign id (a Claude id or an OpenCode provider slug) warns.

Fork: ignored. Codex has no native headless fork (its fork is TUI-only), so
`CommandOptions.Fork` is not consulted in `BuildCommand`. Setting `Fork` has no
effect; isolation degrades to the fresh branch/worktree taboo already allocates.

Push deny: no command-level deny. Codex exposes no per-command deny flag, so —
unlike `claude-code` and `github-copilot` — its `git push` deny degrades to the
workshop container as its only boundary. taboo commits in place and never pushes.

## Why push is denied

A linked worktree shares the host repo's object store and refs, so a push from
inside the workshop could mutate host branches. taboo's contract is
commit-in-place: the agent commits through the bind-mount and the host owns
integration. The agent never needs to push. See
[the isolation model](../explanation/isolation-model.md).

## Agent resolution

Source: `internal/agent/registry.go`.

```go
func NewProfile(name AgentName, model string) (AgentProfile, error)
func AgentNames() []string
func MatchModelFormat(agentName AgentName, model string) (ok bool, expected string)
```

The roster in `registry.go` is a slice of registrations, one line per supported
agent (internal constructor names `NewOpenCode`, `NewClaudeCode`,
`NewGitHubCopilot`, `NewCodex`). The public constants live in `internal/agent` and
are re-exported from the facade as `taboo.OpenCode`, `taboo.ClaudeCode`,
`taboo.GitHubCopilot`, and `taboo.Codex`:

```go
var agents = []registration{
    {New: NewOpenCode, Hint: openCodeHint},
    {New: NewClaudeCode, Hint: claudeCodeHint},
    {New: NewGitHubCopilot, Hint: copilotHint},
    {New: NewCodex, Hint: codexHint},
}
```

`NewProfile(name AgentName, model)` scans the roster for a registration whose `Name()`
equals `name` and returns `New(model)`. An unmatched name returns a wrapped
`ErrUnknownAgent` (`taboo: unknown agent`); match it with `errors.Is`.
`NewProfile` validates the name only, not the model.

`AgentNames()` returns the registered names as a sorted `[]string`: `claude-code`,
`codex`, `github-copilot`, `opencode`. It feeds the CLI's fuzzy-suggestion path on
an unknown name; the matching itself lives in the CLI, not here. To name an agent
in code, use the `taboo.OpenCode`, `taboo.ClaudeCode`, `taboo.GitHubCopilot`, and
`taboo.Codex` constants (`AgentName`) with `NewProfile` or `Workflow.Agent`.

`MatchModelFormat(agentName AgentName, model)` reads the registration's `Hint` and reports
whether `model` looks well-formed, plus the `expected` format string. It is
advisory: `taboo validate` turns a non-match into a warning, never a failure.

`LoadConfig` (`internal/config/config.go`) resolves the top-level `agent` and `model`, and each
workflow's, to an `AgentProfile` through `NewProfile`, storing them on
`ProjectConfig.Profile` and `Workflow.Profile`.

## Unsupported agents

`pi` is not supported. It exists only as an SDK stub directory under
`internal/workshop/sdk/`. It has no Go profile in `agent_*.go` and no registration
in `registry.go`, so `NewProfile(taboo.AgentName("pi"), ...)` returns
`ErrUnknownAgent`, and `AgentNames()` does not list it.

## See also

- [Library API reference](library-api.md) for `AgentProfile` and the registry
  functions.
- [taboo.yaml reference](taboo-yaml.md) for setting `agent` and
  `model`.
