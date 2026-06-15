# Agent registry: declarative roster keyed by Name(), hint as co-located metadata

## Status

accepted

## Context & decision

The taboo CLI (PRD #19) needs to resolve an agent `name` (plus a model) to an
`AgentProfile`, and to enumerate the canonical names it supports so it can
suggest a correction on a typo (story #24). It also needs an optional per-agent
**model-format hint** for `validate` to warn (not fail) on a suspicious model
string (story #25). This registry lives in `pkg/taboo` (#39); the fuzzy matching
itself lives in the CLI — the registry only supplies the candidate set.

We add a `registry.go` holding an **explicit, declarative slice** of
registrations, each pairing an agent's constructor with its model-format hint:

```go
var agents = []registration{
    {New: OpenCode,   Hint: openCodeHint},
    {New: ClaudeCode, Hint: claudeCodeHint},
}
```

The registry is **keyed by `New("").Name()`** — the key *is* the profile's own
`Name()`, so there is no second name literal to drift out of sync. This leans on
the existing invariant that an agent's `Name()` is one identity with the workshop
SDK qualifier (see CONTEXT.md, ADR 0001); `Name()` is model-independent, so
constructing with `""` purely to read the key is safe. Public surface:
`NewProfile(name, model string) (AgentProfile, error)` returning a wrapped
sentinel `ErrUnknownAgent` on a miss, and `AgentNames() []string` (sorted) for
the CLI's candidate set.

The **model-format hint is registry-table metadata, sourced from a value defined
in each `agent_<name>.go`** (`claudeCodeHint` lives beside `claudeCode`). This
keeps the hint cohesive with its agent *without* adding a method to the
hand-reviewed 4-method `AgentProfile` interface, and lets `validate` read the hint
from the agent name alone — it runs on config, before anything is constructed or run.

## Considered options

- **`init()` self-registration** (each `agent_*.go` registers itself into a
  package var). Gives cohesion and a no-central-edit roster, but is exactly the
  implicit "magic" this codebase avoids — registration order and membership stop
  being greppable. Rejected.
- **Behavioral `Registerer` interface** with a `Register(add ...)` method per
  agent (the pattern used for HTTP service endpoints elsewhere in the org). Earns
  its keep when one unit fans out to many registrations with per-item policy;
  here registration is 1-to-1 with no policy, so the method body collapses to a
  single `add(...)` call — an interface and a per-agent method as ceremony over
  data. Would be reconsidered only if an agent ever registers multiple
  names/aliases. Rejected for now.
- **Hint as a method on `AgentProfile`** (or an optional `ModelHinter` interface
  read via type assertion). Either bloats the deliberately-minimal interface
  (ADR 0001) or forces constructing a throwaway profile just to read a static,
  model-independent value before any run. Rejected.
- **Declarative slice keyed by `Name()`, hint as co-located metadata (chosen).**
  Enumeration stays explicit and greppable, the interface stays at 4 methods, the
  hint stays in the agent file, and nothing needs constructing to read a name or
  a hint. Cost: adding an agent is a one-line edit to `registry.go` (accepted —
  the explicitness is the point).

## Consequences

- A roster-invariant test asserts **registry ⊆ embedded SDKs**: every registered
  name has a `pkg/taboo/sdk/<Name()>/` dir whose `sdk.yaml` `name` equals
  `Name()`. This is the safety-critical direction — `runner.go` uses
  `Agent.Name()` directly as the SDK qualifier, so a registered agent with no
  matching SDK breaks at provisioning. The reverse direction — every embedded SDK
  has a profile — is not asserted yet: `codex`/`copilot`/`pi` are embedded but
  profile-less, so it is left to a TODO until the last profile lands, rather than a
  live skip-list that could rot into a false green.
- The hint's concrete *type* (regex vs. predicate vs. human "expected format"
  string) is deferred to the `validate` slice that consumes it; this ADR fixes
  only its *placement*, so the interface and registry shape do not churn later.
- `NewProfile` is name-resolution only — it does not validate the model — so its
  error space is exactly the one `ErrUnknownAgent` sentinel the CLI matches with
  `errors.Is` to branch into its fuzzy suggestion.
