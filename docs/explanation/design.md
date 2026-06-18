# Why the API is shaped this way

taboo's surface is a Go library first and a CLI second. The types it exposes, the
single side-effecting seam they run through, and the way agents and structured
output are modeled all follow from a small set of decisions, each recorded in an
ADR. This page explains those decisions and why they hold. For the catalogue of
types and signatures, see the
[library API reference](../reference/library-api.md). The architecture decisions
themselves live under [docs/adr/](../adr/).

## The library is the primary contract

CONTEXT.md states the stance directly: "The primary deliverable is a Go module
(`pkg/taboo`)." It continues: "A CLI may follow but is not the primary contract."

The audience is engineers building agent pipelines in Go who want to express
fan-out, review loops, and custom orchestration in code, not through flags. So the
library carries the full surface, and `cmd/taboo/` is a thin consumer of it. The
CLI does not own behaviour the library lacks; it wires flags and config onto the
same `Runner`, `Orchestrator`, and `Pool` a Go caller would construct. This is why
the reference docs point Go callers and CLI users at the same concepts, and why the
library is documented as the contract while the CLI is documented as a convenience
over it.

## The single side-effecting seam

Everything taboo does to the outside world, every `workshop` and `git` invocation,
goes through one interface. `Commander` in `pkg/taboo/commander.go` is
`Run(ctx context.Context, c Cmd) error`, and `Cmd` is a single host-side process
invocation. `NewExecCommander` returns the production implementation that shells
out via `os/exec`.

Concentrating side effects at one seam is what makes the rest of the library
testable without a real workshop or LXD. A test substitutes a fake `Commander` that
records the `Cmd` values it receives and returns canned results, and then asserts
on the exact `workshop` and `git` argv taboo would have run, with no container in
sight. The same seam is where a future capability slots in: ADR 0006 notes that
warm-clone fan-out, once an upstream verb exists, reverses by implementing behind
the `Commander` seam, changing no other type. The library is feature-complete and
its unit suite runs entirely against this fake.

## Agent-as-SDK and the command contract

An agent CLI has to exist inside the workshop before taboo can exec it, and the
per-run `stop` reprovisions the rootfs from the declared SDKs (see
[the isolation model](isolation-model.md)). So taboo packages each supported agent
as a workshop SDK and bakes it in, rather than installing it at runtime where the
next `stop` would wipe it.

The agent abstraction is `AgentProfile` in `pkg/taboo/agent.go`. One profile value
fully describes an agent: its name (which doubles as the SDK qualifier), how to
build its exec invocation, the credential env keys it needs, and its session
redirect. The command builder returns `AgentCommand{Argv []string; Stdin string}`
rather than a bare argv slice. ADR 0001
([the argv + stdin command contract](../adr/0001-agentprofile-argv-stdin-command-contract.md))
records why: the supported agents split on how they receive the prompt. OpenCode and
Copilot take it in argv; Claude Code takes it on stdin. An argv-only return could
not represent the stdin half without a later breaking change to an interface that
fan-out and sessions build on, so the two-field struct covers the whole roster up
front, and sidesteps the `ARG_MAX` limit on argv-delivered prompts.

Two further decisions extend the same command seam without reshaping it. ADR 0003
([session resume and fork](../adr/0003-session-resume-fork-command-contract.md))
adds `ResumeSession` and `Fork` to `CommandOptions` and mirrors them on
`RunRequest`, so each profile maps them into its own CLI dialect while the
orchestration code stays agent-neutral. ADR 0004
([multi-key credential env](../adr/0004-multi-key-credential-env.md)) lets
`CredentialEnvKeys()` return more than one key, so Claude Code can offer both
`ANTHROPIC_API_KEY` and `CLAUDE_CODE_OAUTH_TOKEN`; this is safe because
`workshop exec --env NAME` silently drops a key that is unset on the host, so a user
sets only the credential they hold.

## The declarative agent registry

The CLI resolves an agent name and model to an `AgentProfile`, and enumerates the
canonical names so it can suggest a correction on a typo. ADR 0005
([the declarative roster](../adr/0005-agent-registry-declarative-roster.md)) records
that `pkg/taboo/registry.go` holds an explicit slice of registrations rather than
self-registering agents through `init()`. The public surface is
`NewProfile(name, model string) (AgentProfile, error)`, which returns the wrapped
`ErrUnknownAgent` sentinel on a miss, and `AgentNames() []string`, sorted, for the
candidate set.

The registry is keyed by each profile's own `Name()`, so there is no second name
literal to drift out of sync. The choice is about visibility. An `init()`-based
roster hides which agents are registered and in what order behind import side
effects; the explicit slice keeps membership greppable, at the cost of a one-line
edit to add an agent, which is the point. The fuzzy "did you mean" match lives in
the CLI, not the registry; the registry only supplies the candidate set. The
model-format hint lives beside each agent rather than as a method on the four-method
`AgentProfile` interface, so `validate` can read it from a name alone, before
anything is constructed. ADR 0008 covers the hint and the fuzzy match.

## Structured output is the caller's struct

taboo extracts a typed result from an agent's stdout: a delimited
`<result>{...}</result>` block whose JSON payload becomes a typed Go value. The seam
is `ResultExtractor` in `pkg/taboo/result.go`, an interface built by the generic
constructor `JSONResult[T any](opts ...Option) ResultExtractor`.

ADR 0002
([structured output over generics](../adr/0002-structured-output-generics-encoding-json.md))
records the reasoning. sandcastle, the TypeScript product taboo is modeled on, uses
Zod, which fuses one artifact into both a runtime schema and a static type. Go has no
such fusion, so this is a genuine fork. taboo treats the caller's Go struct as the
schema: `JSONResult[T]` locates the result block and decodes its payload into `T`
with `encoding/json`, adding no third-party dependency. Validation is layered, in
the order it fires: well-formedness and type are always on; unknown fields are
opt-in via `WithStrictFields()`, off by default because agent stdout is chatty; and
semantic checks are opt-in via the `Validator` interface, the dependency-free
stand-in for a schema's `required` and `enum` constraints. Two `errors.Is`-matchable
sentinels keep the failure modes distinct: `ErrNoResult` for no block, and
`ErrInvalidResult` for a block that is present but malformed, type-mismatched, or
rejected by `Validate()`. The
[typed-results guide](../guides/typed-results.md) shows the mechanism in use.

Two properties follow from keeping the generic confined to the constructor. The
orchestration API stays non-generic: `OrchestratedRequest.ResultExtractor` holds the
interface and `OrchestratedResult.Result` is `any`, so the caller type-asserts
`res.Result.(T)`, and `Pool` inherits structured output without becoming generic
itself. And the design stays reversible toward declarative JSON-Schema validation,
because callers depend on the `ResultExtractor` interface and the sentinel errors,
not on the mechanism.

## Deferred warm fan-out

Fan-out runs one workshop per concurrency slot, each provisioned cold (see
[the isolation model](isolation-model.md)). Starting each slot from a warm clone of
an already-provisioned workshop would save the dominant per-slot cost, and it was
investigated. ADR 0006
([defer warm-clone fan-out](../adr/0006-defer-warm-fanout-single-repo-workshops.md))
records the decision to adopt neither warm-clone fan-out nor multi-repo workshop
reuse for now.

The reasoning is about staying inside the contract. The `workshop` CLI exposes no
clone, snapshot, or `launch --from` verb, and the investigation confirmed its Go
client wraps the same daemon API with the identical verb surface, so the warm-clone
win is unreachable without an upstream verb. Reaching below that line to LXD directly
was rejected as coupling taboo to the substrate that the shell-out contract exists to
avoid. Multi-repo reuse was rejected because the git-common mount pins a workshop to
one repository's `.git` path; the only in-contract alternative would force all
managed repositories under a single host parent, for a launch cost that is not yet
measured to justify it. Both decisions are pure "do not build", so neither changed a
type or a seam, and warm-clone reverses the day an upstream verb lands by
implementing behind the `Commander` seam.

## See also

- [The isolation model](isolation-model.md)
- [Library API reference](../reference/library-api.md)
- [Agents reference](../reference/agents.md)
- [Architecture decision records](../adr/)
