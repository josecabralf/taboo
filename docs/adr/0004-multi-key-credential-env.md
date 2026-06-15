# Multiple credential env keys per agent

## Status

accepted

## Context & decision

`AgentProfile.CredentialEnvKeys()` returns the host env-var names whose values
reach the agent via `workshop exec --env NAME` (ADR 0001; the value never enters
argv). OpenCode returns a single key (`OPENROUTER_API_KEY`). Claude Code is the
first agent that authenticates **two different ways** — an API key
(`ANTHROPIC_API_KEY`) for API users, or a long-lived OAuth token
(`CLAUDE_CODE_OAUTH_TOKEN`, from `claude setup-token`) for Pro/Max/Team/Enterprise
**subscription** users.

The decision is that `CredentialEnvKeys()` may return **more than one key**, and
the Claude Code profile returns **both**:

```go
func (claudeCode) CredentialEnvKeys() []string {
    return []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"}
}
```

This is safe and unambiguous because of two pre-existing behaviours taboo does
*not* have to implement:

1. **`workshop exec --env NAME` silently drops a key that is unset on the host.**
   `cmd/workshop/exec.go` resolves a name-only `--env` with `os.LookupEnv`; on a
   miss it `continue`s rather than injecting an empty value or erroring. So a user
   with only one of the two keys set has exactly that one forwarded; the other
   never reaches the workshop. One profile therefore serves both API-key and
   subscription users with zero configuration branching.

2. **When both are set, Claude Code's own documented precedence picks the API
   key** (`ANTHROPIC_API_KEY` ranks above `CLAUDE_CODE_OAUTH_TOKEN`). "Prefer the
   API key when both are available" thus needs no taboo code — the CLI does it.
   List order in `CredentialEnvKeys()` is cosmetic (both become `--env` flags);
   the API key is listed first only to mirror the precedence.

## Considered options

- **Both keys, rely on workshop's silent-drop + Claude's precedence (chosen).**
  Smallest surface; one profile, no constructor variants. The cost is that a user
  who sets *both* keys gets API-key billing even if they intended to use their
  subscription — invisible unless documented (it is, here and in the profile
  godoc).
- **One key, selectable at construction** (`ClaudeCode(model)` vs a token
  variant/option). Explicit, no silent precedence. Rejected: two code paths and a
  caller decision for no real gain, since the silent-drop already disambiguates.
- **API key only**, defer subscription. Rejected: fails the stated goal of serving
  subscription users now.

## Consequences

- The contract generalizes: future multi-auth agents (Codex, Copilot, …) may list
  every key they accept and let the host env + the agent's precedence resolve it.
- The safety of returning both keys is **load-bearing on `exec.go`'s drop-unset
  behaviour**. If a future workshop release made `--env NAME` error on an unset
  host var, listing a key the user hasn't set would break the run — a
  compatibility assumption worth re-checking when bumping the workshop dependency.
- Pairs with the session-capture decision: the Claude profile redirects
  `CLAUDE_CONFIG_DIR` (which holds credentials by design) onto the host sessions
  mount. That is safe *only* because auth is env-based here, so Claude writes no
  `.credentials.json` to disk. Env-based credentials and the session redirect are
  coupled — see the `Sessions()` godoc warning.
