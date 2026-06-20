# Agents reference

taboo supports three agents: `opencode`, `claude-code`, and `copilot`. Each is
one `AgentProfile` implementation (`pkg/internal/agent/agent.go`); construct one
through the public `NewProfile(name, model)`. The profiles live in
`pkg/internal/agent/agent_opencode.go`, `agent_claudecode.go`, and
`agent_copilot.go`; the declarative roster that ties names to constructors is in
`pkg/internal/agent/registry.go`.

The generated godoc is the rendered source of truth:
<https://pkg.go.dev/github.com/josecabralf/taboo/pkg>.

## Summary

| Agent | `Name()` | Construct with | Credential env keys | Prompt delivery | Sessions `DirEnv` / `Subdir` | Model-hint `expected` | Fork |
|---|---|---|---|---|---|---|---|
| OpenCode | `opencode` | `NewProfile("opencode", model)` | `OPENROUTER_API_KEY` | argv | `XDG_DATA_HOME` / `opencode` | `<provider>/<model>, e.g. openrouter/qwen/qwen3-coder-plus` | native (`--fork`) |
| Claude Code | `claude-code` | `NewProfile("claude-code", model)` | `ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN` | stdin | `CLAUDE_CONFIG_DIR` / `projects` | `a Claude model id or family alias, e.g. claude-sonnet-4-6 or sonnet` | native (`--fork-session`) |
| Copilot | `copilot` | `NewProfile("copilot", model)` | `COPILOT_GITHUB_TOKEN`, `GH_TOKEN`, `GITHUB_TOKEN` | argv (value of `-p`) | `COPILOT_HOME` / `session-state` | none (never warns) | ignored |

All three deny `git push` from inside the workshop. Credential env keys reach the
agent via `workshop exec --env NAME`, which silently drops any key that is unset
on the host, so a user forwards only the credential they hold.

## OpenCode

Source: `pkg/internal/agent/agent_opencode.go`.

`Name()` returns `opencode`. Construct it with `NewProfile("opencode", model)`.

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

Push deny: yes. OpenCode runs its tools freely inside the isolated workshop, and
the workshop is the security boundary.

## Claude Code

Source: `pkg/internal/agent/agent_claudecode.go`.

`Name()` returns `claude-code`. Construct it with `NewProfile("claude-code", model)`.

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
`git push`, `git push origin main`, and `--force` in any position are blocked.

## Copilot

Source: `pkg/internal/agent/agent_copilot.go`.

`Name()` returns `copilot`. Construct it with `NewProfile("copilot", model)`.

Credential env keys (`CredentialEnvKeys()`): `COPILOT_GITHUB_TOKEN`, `GH_TOKEN`,
and `GITHUB_TOKEN`, in Copilot's own documented precedence order. `workshop exec
--env NAME` drops whichever are unset; when several are set, Copilot's precedence
picks the first present.

Prompt delivery: argv, as the value of `-p`. `BuildCommand` renders
`copilot --model <model> --allow-all --deny-tool=shell(git push) --output-format
text -s` and then appends `-p <prompt>`; `AgentCommand.Stdin` is empty. `-p` is
always emitted, even when the prompt is empty. A resume id appends
`--resume=<id>` before the `-p` prompt.

Sessions (`Sessions()`): `DirEnv` is `COPILOT_HOME`, `Subdir` is `session-state`,
and the second return value is `true`. `COPILOT_HOME` is the only env var Copilot
exposes to relocate its home directory; it captures the entire home. Session
transcripts land under `session-state/`.

Model hint: none. `copilotHint` is the no-opinion hint (nil pattern), because
Copilot proxies models from many providers. `taboo validate` never warns on a
Copilot model, and `MatchModelFormat("copilot", ...)` returns `expected` as `""`.

Fork: ignored. Copilot has no native headless fork, so `CommandOptions.Fork` is
not consulted in `BuildCommand`. A run that sets `Fork` degrades to worktree-only
isolation.

Push deny: yes, via `--deny-tool=shell(git push)`. Denial rules take precedence
over `--allow-all`, and Copilot approves shell commands on a first-level
subcommand basis, so this one pattern blocks every `git push` form.

## Why push is denied

A linked worktree shares the host repo's object store and refs, so a push from
inside the workshop could mutate host branches. taboo's contract is
commit-in-place: the agent commits through the bind-mount and the host owns
integration. The agent never needs to push. See
[the isolation model](../explanation/isolation-model.md).

## Agent resolution

Source: `pkg/internal/agent/registry.go`.

```go
func NewProfile(name, model string) (AgentProfile, error)
func AgentNames() []string
func MatchModelFormat(agent, model string) (ok bool, expected string)
```

The roster in `registry.go` is a slice of registrations, one line per supported
agent, keyed by each profile's own `Name()`:

```go
var agents = []registration{
    {New: OpenCode, Hint: openCodeHint},
    {New: ClaudeCode, Hint: claudeCodeHint},
    {New: Copilot, Hint: copilotHint},
}
```

`NewProfile(name, model)` scans the roster for a registration whose `Name()`
equals `name` and returns `New(model)`. An unmatched name returns a wrapped
`ErrUnknownAgent` (`taboo: unknown agent`); match it with `errors.Is`.
`NewProfile` validates the name only, not the model.

`AgentNames()` returns the registered names sorted: `claude-code`, `copilot`,
`opencode`.

`MatchModelFormat(agent, model)` reads the registration's `Hint` and reports
whether `model` looks well-formed, plus the `expected` format string. It is
advisory: `taboo validate` turns a non-match into a warning, never a failure.

`LoadConfig` (`pkg/internal/config/config.go`) resolves the top-level `agent` and `model`, and each
workflow's, to an `AgentProfile` through `NewProfile`, storing them on
`ProjectConfig.Profile` and `Workflow.Profile`.

## Unsupported agents

`codex` and `pi` are not supported. They exist only as SDK stub directories under
`pkg/internal/workshop/sdk/`. They have no Go profile in `agent_*.go` and no registration in
`registry.go`, so `NewProfile("codex", ...)` and `NewProfile("pi", ...)` return
`ErrUnknownAgent`, and `AgentNames()` does not list them.

## See also

- [Library API reference](library-api.md) for `AgentProfile` and the registry
  functions.
- [taboo.yaml reference](../reference/taboo-yaml.md) for setting `agent` and
  `model`.
