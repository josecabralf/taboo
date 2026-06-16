# Model-format hint: advisory per-agent regex, copilot no-opinion; fuzzy agent match in the CLI

## Status

accepted

## Context & decision

ADR 0005 fixed the agent registry's shape and the *placement* of the per-agent
**model-format hint** (registry-table metadata, co-located as `<name>Hint` in each
`agent_<name>.go`), but deliberately deferred the hint's concrete *type* to "the
validate slice that consumes it". `taboo validate` (#33) is that slice. It needs
two judgement calls the loader never made (`LoadConfig` validates the agent name
only): does a configured model *look* right for its agent (story #25), and what
is the closest registered name to a misspelled one (story #24)? This ADR settles
both.

**The hint is an advisory regex.** `modelHint` becomes a `{pattern *regexp.Regexp;
expected string}` pair. `pattern` is the shape a well-formed model matches;
`expected` is the human-readable format the warning quotes. A pure library
function exposes it:

```go
func MatchModelFormat(agent, model string) (ok bool, expected string)
```

It is **warn-not-fail by construction**: an unknown agent, a no-opinion hint (nil
`pattern`), or a pattern match all return `ok=true`. Only a *recognized* agent
whose pattern *rejects* the model returns `ok=false`, which validate renders as a
WARN — never an error, never a non-zero exit. Surrounding whitespace is trimmed so
a stray space in YAML cannot trip a false warning.

Per-agent patterns:

- **opencode** → `^[^/]+/.+$` (a `<provider>/<model>` slug; opencode routes
  through OpenRouter and friends, so a bare `gpt-4` is almost certainly wrong).
- **claude-code** → `(?i)claude|^(sonnet|opus|haiku)` (any id containing
  "claude", covering `claude-*` and Bedrock/Vertex `anthropic.claude-*`, or a bare
  family alias).
- **copilot** → **no opinion** (nil pattern). Copilot is a proxy that runs
  models from many providers (gpt-\*, claude-\*, gemini-\*, the o-series), so there
  is no single well-formed shape to check; any heuristic would warn on valid
  configs. `copilotHint = modelHint{}` makes the absence of an opinion explicit.

**Fuzzy agent matching lives in the CLI, not the registry.** ADR 0005 split the
work — the registry supplies the candidate set (`AgentNames()`), the CLI decides
what is "close". `cmd/taboo/fuzzy.go`'s `suggestAgent(name, candidates)` lowercases
and trims, then ranks candidates so a *prefix relationship* (an abbreviation like
`claude` for `claude-code`) always outranks a plain edit-distance match; within a
class the smaller Levenshtein distance wins. A suggestion is surfaced when the
best candidate is a prefix match or its distance is within a length-scaled budget
(`len/2 + 1`) — close enough to be a likely typo, not a coincidence. The
Levenshtein helper is hand-rolled so the CLI takes on no new dependency.

**validate decodes the raw config itself** rather than calling `LoadConfig`.
`LoadConfig` resolves profiles and aborts on the first unknown agent
(`ErrUnknownAgent`); validate must instead report *every* problem in one pass, so
an unknown agent is a per-agent check, not a fatal stop. It therefore strict-decodes
`ProjectConfig` directly (`KnownFields(true)`, single-document, like the library's
unexported `decodeStrict`) and computes effective agent/model bindings with a
CLI-local `orElse` (the library's is unexported). One behavioral difference is
deliberate: `decodeStrict` treats an empty document as success (the zero config,
because the loader defers required-field enforcement to validate), whereas
`decodeValidate` reports it as `config is empty` — that enforcement is validate's
job.

## Considered options

- **Hint as an advisory regex (chosen).** Declarative, greppable, and cheap to
  read from the agent name alone before any run. The regex is intentionally loose
  — it catches the *category* of mistake (wrong vendor, missing provider slug), not
  a closed set of model ids.
- **Hint as a predicate function** (`func(string) bool` per agent). Equivalent in
  power but buries the rule in code rather than a visible pattern, and tempts each
  agent into bespoke logic. Rejected for the regex's at-a-glance legibility.
- **A curated catalog of valid model ids.** Highest precision, but models churn
  weekly; a catalog would rot and would hard-fail brand-new valid models the day
  they ship. Rejected — exactly the brittleness the advisory warning avoids.
- **Hard-fail on a format mismatch.** Rejected: the registry cannot know every
  model an agent will ever accept, so a fail would block legitimate configs. The
  warning names how to silence it ("set it intentionally"), keeping the user in
  control.
- **Fuzzy matching in the registry.** Rejected by ADR 0005 already; recorded here
  for completeness. Keeping the policy in the CLI lets the suggestion heuristic
  evolve (prefix boost, budget) without touching the library contract.
- **A Levenshtein library dependency.** Rejected; a dozen-line two-row helper is
  enough and keeps `pkg`/`cmd` dependency-free beyond cobra + yaml.

## Consequences

- Adding an agent now also sets its hint: a `modelHint{pattern, expected}` to opt
  into format warnings, or `modelHint{}` to stay silent (the copilot posture). The
  hint stays co-located in `agent_<name>.go` (ADR 0005) and off the `AgentProfile`
  interface (ADR 0001) — `MatchModelFormat` reads it from the name alone.
- `MatchModelFormat` returns `ok=true` for an unknown agent on purpose: the
  unknown-agent *failure* is the agent check's job, and a model warning layered on
  top would be redundant noise.
- validate carries small, documented duplicates of `pkg/taboo` internals (the
  strict single-document decode and `orElse`) because they are unexported there.
  If a third consumer needs them, promote them to exported library helpers rather
  than copying a third time — but the empty-document policy must be parameterized
  so validate keeps rejecting an empty config while the loader keeps tolerating it.
- The heuristic will occasionally warn on a valid-but-unusual model (a false
  positive is acceptable for an advisory check); it will never fail one. New agents
  whose model format is genuinely open-ended should use the no-opinion hint.
