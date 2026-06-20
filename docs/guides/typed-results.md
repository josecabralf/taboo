# Get a typed result out of a run

Decode a typed, validated Go value from an agent's output instead of parsing its
stdout by hand.

Agents print free-form text. To make a decision in code (did the review pass? how
many issues?), wrap the structured part in a delimited block and let
`JSONResult[T]` decode it into your struct.

## The result block convention

The agent prints a `<result>...</result>` block somewhere in its output, with a
JSON payload inside:

```text
I checked the code and fixed two issues.
<result>{"passed": true, "issues": 2}</result>
```

`JSONResult[T]` (`pkg/taboo/result.go`) finds that block, decodes the JSON into
`T`, and returns it. Your struct is the schema: the JSON keys map to fields by
their `json` tags.

## Decode a block

`JSONResult[T]` returns a `ResultExtractor`. Its `Extract` method takes the agent
output and returns `any`, which you type-assert to `T`.

```go
package main

import (
	"errors"
	"fmt"

	taboo "github.com/josecabralf/taboo/pkg"
)

type review struct {
	Passed bool `json:"passed"`
	Issues int  `json:"issues"`
}

func main() {
	output := `done.
<result>{"passed": true, "issues": 2}</result>`

	ext := taboo.JSONResult[review]()
	v, err := ext.Extract(output)
	if err != nil {
		if errors.Is(err, taboo.ErrNoResult) {
			fmt.Println("no result block in output")
		}
		return
	}
	r := v.(review)
	fmt.Printf("passed=%v issues=%d\n", r.Passed, r.Issues)
}
```

This program runs without a workshop host. `Extract` is a pure function over the
output string.

## Attach the extractor to an orchestrated run

Set the extractor on an `OrchestratedRequest` and the orchestrator runs it over
the final iteration's output, exposing the value on `OrchestratedResult.Result`:

```go
res, err := orch.Run(ctx, taboo.OrchestratedRequest{
	RunRequest:      taboo.RunRequest{Branch: "taboo/review", Prompt: prompt},
	ResultExtractor: taboo.JSONResult[review](),
})
if err != nil {
	return err
}
r := res.Result.(review)
```

See [Iterate until the agent signals done](iterate-until-done.md) for the loop
that drives this.

## The last complete block wins

When the output holds several `<result>` blocks (an echoed example, a retry), the
extractor pairs the last `<result>` opening tag with the first `</result>` that
follows it. The last complete block wins. This tolerates the agent quoting an
example block earlier in its output and emitting the real one at the end.

An opening tag with no following close tag yields `ErrNoResult`, the same as no
block at all.

## Reject unknown fields with WithStrictFields

By default, decoding ignores JSON keys absent from `T`, because agent stdout is
chatty and a stray key should not fail an otherwise-correct result. Pass
`WithStrictFields()` to reject a payload that carries unknown fields
(`DisallowUnknownFields`):

```go
ext := taboo.JSONResult[review](taboo.WithStrictFields())
```

With strict fields on, a payload like `{"passed": true, "issues": 2, "extra": 1}`
returns `ErrInvalidResult` because `extra` is not a field of `review`.

## Change the delimiters with WithDelimiters

The default delimiters are `<result>` and `</result>`. Override them with
`WithDelimiters(open, close)` when those tags collide with the agent's content:

```go
ext := taboo.JSONResult[review](taboo.WithDelimiters("<<json>>", "<</json>>"))
```

## Validate the decoded value

For checks beyond well-formed JSON (a required field, an enum, a range),
implement the `Validator` interface on `T`:

```go
type Validator interface {
	Validate() error
}
```

If `T` implements `Validate`, the extractor calls it after decoding and treats a
non-nil error as an invalid result:

```go
type review struct {
	Passed bool `json:"passed"`
	Issues int  `json:"issues"`
}

func (r review) Validate() error {
	if r.Issues < 0 {
		return fmt.Errorf("issues cannot be negative: %d", r.Issues)
	}
	return nil
}
```

A non-nil `Validate` error returns `ErrInvalidResult`, with the underlying detail
preserved in the message. A value or pointer receiver works.

## Tell the two errors apart

`Extract` returns one of two sentinel errors, both matchable with `errors.Is`:

- `ErrNoResult` means no complete block was found: no opening tag, or no closing
  tag after the last opening tag.
- `ErrInvalidResult` means a block was found but its payload would not
  decode or validate: empty, malformed JSON, type-mismatched, an unknown field
  under strict mode, or rejected by `Validate`.

```go
v, err := ext.Extract(output)
switch {
case errors.Is(err, taboo.ErrNoResult):
	// agent emitted no result block
case errors.Is(err, taboo.ErrInvalidResult):
	// block found but payload rejected
case err != nil:
	// other error
default:
	r := v.(review)
	_ = r
}
```

## See also

- [Iterate until the agent signals done](iterate-until-done.md) to attach an
  extractor to a loop.
- [Structured output: generics and encoding/json](../adr/0002-structured-output-generics-encoding-json.md)
  for why the struct is the schema.
- [Library API reference](../reference/library-api.md) for the signatures of
  `JSONResult`, `Option`, `WithStrictFields`, `WithDelimiters`, and `Validator`.
