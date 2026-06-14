# AgentProfile: argv + optional-stdin command contract

## Status

accepted

## Context & decision

`AgentProfile` is taboo's per-agent abstraction (PRD #1, slice 3). Its command
builder, `BuildCommand(CommandOptions) AgentCommand`, returns an
`AgentCommand{ Argv []string; Stdin string }` rather than a bare `[]string` of
argv. taboo execs the agent via `workshop exec -- <argv>`; when `Stdin` is
non-empty the runner pipes it to the agent's stdin (`Cmd.Stdin`) instead of the
prompt riding in argv.

We chose the two-field struct because the agents taboo intends to support split
on how they receive the prompt, and an argv-only return cannot represent the
stdin half of that roster; wiring up Claude Code or Codex would then force a
breaking change to this interface, which is reviewed by hand precisely because
fan-out and sessions build on it.

## Considered options

- **`BuildCommand(...) []string` (argv only).** Simplest, and enough for OpenCode
  alone. Rejected: three of the five embedded SDKs deliver the prompt on stdin,
  so the first non-argv agent breaks the interface.
- **`BuildCommand(...) AgentCommand{Argv, Stdin}` (chosen).** OpenCode/Copilot/
  Cursor populate `Argv` and leave `Stdin` empty; Claude/Codex/Pi put the agent
  invocation in `Argv` and the prompt in `Stdin`. One empty field on the argv
  agents buys the whole roster without an interface change, and sidesteps the
  ARG_MAX (~128 KB) limit on argv-delivered prompts.

Prompt delivery across the embedded `pkg/taboo/sdk/` roster (verified against the
sandcastle reference invocations):

| Agent       | Prompt delivery                 |
|-------------|---------------------------------|
| opencode    | positional argv                 |
| copilot     | `-p <prompt>` argv              |
| cursor      | positional argv                 |
| claude-code | stdin (`claude --print … -p -`) |
| codex       | stdin (`codex exec --json …`)   |
| pi          | stdin (`pi -p --mode json …`)   |

## Consequences

- The return type is named `AgentCommand`, not `Command`, to stay distinct from
  the host-process `Cmd` type in `commander.go`.
- `CommandOptions` (the builder input) is likewise a struct so the sessions slice
  can add `ResumeSession`/`ForkSession` non-breakingly; this slice ships only
  `Prompt`.
