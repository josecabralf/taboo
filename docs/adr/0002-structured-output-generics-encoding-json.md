# Structured output: generics + encoding/json, the struct is the schema

## Status

accepted

## Context & decision

taboo extracts a typed result from an agent's stdout: a delimited
`<result>{...}</result>` block whose JSON payload becomes a typed Go value (PRD
#1, slice 3 — "the no-Zod-in-Go fork"). sandcastle uses Zod, which fuses one
artifact into both a runtime *schema* (validated, and shown to the LLM) and a
static *type*. Go has no such fusion, so the mechanism is a genuine design fork.

We chose **generics over `encoding/json`, treating the caller's Go struct as the
schema**. The seam is an interface built by a generic constructor:

```go
type ResultExtractor interface { Extract(output string) (any, error) }
func JSONResult[T any](opts ...Option) ResultExtractor
```

`JSONResult[T]` locates the result block, `json.Unmarshal`s its payload into `T`,
and returns it as `any`. No third-party dependency is added; `gopkg.in/yaml.v3`
remains taboo's only require.

Validation has three layers, in order of when they fire:

1. **Well-formedness + type** — always on: malformed JSON or a payload that does
   not decode into `T` is invalid.
2. **Unknown fields** — opt-in via `WithStrictFields()` (`DisallowUnknownFields`);
   **off by default** because agent stdout is chatty and a stray model-added key
   should not fail an otherwise-correct result.
3. **Semantic validation** — opt-in via a `Validator` interface
   (`interface{ Validate() error }`): if `T` (value or pointer receiver)
   implements it, the extractor calls it after decode and a returned error is
   invalid. This is the dependency-free stand-in for JSON-Schema's `required` /
   `enum` / range constraints.

## Considered options

- **Generics + `encoding/json` (chosen).** The struct *is* the schema and the
  type — the honest Go analog to Zod's unify-schema-and-type. Zero new
  dependencies (matches the rest of taboo), compile-time typed, a pure function
  trivially testable like the other pure builders. Concedes declarative
  constraint validation and a data-shaped schema reusable for prompt injection —
  the former recovered via the opt-in `Validator` interface, the latter out of
  scope here (the extractor is scoped to "a pure function over captured output").

- **JSON-Schema validation (`santhosh-tekuri/jsonschema` or similar).** Real
  declarative validation (required/enum/min-max/pattern) and a schema that is
  *data* — reusable later to tell the agent the exact shape to emit. Rejected for
  this slice: adds a dependency, breaking the one-require posture, and surfaces an
  untyped value (`json.RawMessage`/`map`) unless the caller *also* maintains a
  struct — reintroducing exactly the schema/type duplication Zod exists to avoid.

- **Hybrid (JSON-Schema validates, struct-tag decoding is the typed accessor).**
  CONTEXT.md's stated starting default. Strongest guarantees, but the most
  ceremony: two artifacts to keep in sync plus the dependency. Rejected as
  premature.

## Consequences

- The orchestration API stays non-generic. The generic lives only in the
  `JSONResult[T]` constructor; `OrchestratedRequest.ResultExtractor` holds the
  interface and `OrchestratedResult.Result` is `any` (caller type-asserts
  `res.Result.(T)`). Fan-out / `Pool` inherit this without becoming generic.
- Two distinct, `errors.Is`-matchable sentinels: `ErrNoResult` (no block found,
  including an unclosed opening tag) and `ErrInvalidResult` (block found but
  empty, malformed, type-mismatched, unknown-field-under-strict, or
  `Validate()`-rejected).
- When the final output holds several result blocks, **the last** complete block
  wins — robust to echoed examples and the qwen retry noise CONTEXT documents.
- Delimiters default to `<result>`/`</result>` and are overridable via an option.
- Extraction is fatal at the orchestrator: `o.Run` returns the wrapped sentinel
  error, but `res` is still populated (`Branch`/`Commit`/`Output`/`Iterations`/
  `StopReason`), so a failed extraction never discards the agent's commit.
- Reversible toward JSON-Schema: callers depend on the `ResultExtractor`
  interface and the sentinel errors, not on the mechanism, so a future
  `JSONSchemaResult(schema)` can be added alongside `JSONResult[T]` without a
  breaking change if declarative schemas or prompt-injectable schemas are needed.
</content>
</invoke>
