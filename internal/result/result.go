package result

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Validator is the opt-in semantic-validation hook: if a decoded result type
// implements it, the extractor calls Validate and treats a non-nil error as an
// invalid result (ADR 0002).
type Validator interface {
	Validate() error
}

// ErrNoResult means no complete result block was found in the agent's output.
var ErrNoResult = errors.New("taboo: no result block found")

// ErrInvalidResult means a result block was found but its payload would not
// decode/validate. Wrapped with %w so errors.Is holds while the detail survives.
var ErrInvalidResult = errors.New("taboo: result block invalid")

// ResultExtractor turns an agent's captured output into a typed, validated result,
// the structured-output seam held as an interface so the API stays non-generic
// (ADR 0002).
type ResultExtractor interface {
	// Extract finds the result block in output and decodes its payload into a typed
	// value, returned as any for the caller to type-assert.
	Extract(output string) (any, error)
}

// extractOptions holds the tunable behavior of a JSON extractor.
type extractOptions struct {
	openTag, closeTag string
	strict            bool
}

// Option configures a JSONResult extractor.
type Option func(*extractOptions)

// WithDelimiters overrides the block delimiters (default `<result>` / `</result>`).
func WithDelimiters(open, close string) Option {
	return func(o *extractOptions) {
		o.openTag, o.closeTag = open, close
	}
}

// WithStrictFields makes decoding reject fields absent from T. Off by default
// because agent stdout is chatty.
func WithStrictFields() Option {
	return func(o *extractOptions) {
		o.strict = true
	}
}

// jsonExtractor decodes the JSON payload of a result block into T.
type jsonExtractor[T any] struct {
	extractOptions
}

// JSONResult builds a ResultExtractor that locates the last <result>...</result>
// block and decodes its JSON payload into T; the caller's struct is the schema
// (ADR 0002).
func JSONResult[T any](opts ...Option) ResultExtractor {
	o := extractOptions{openTag: "<result>", closeTag: "</result>"}
	for _, opt := range opts {
		opt(&o)
	}
	return jsonExtractor[T]{extractOptions: o}
}

// Extract implements ResultExtractor.
func (e jsonExtractor[T]) Extract(output string) (any, error) {
	// Pair from the open side: the last <result>, then the first </result> after
	// it. Anchoring on the last </result> would let a stray close tag in prose
	// mispair the block.
	start := strings.LastIndex(output, e.openTag)
	if start < 0 {
		return nil, ErrNoResult
	}
	rel := strings.Index(output[start+len(e.openTag):], e.closeTag)
	if rel < 0 {
		return nil, ErrNoResult // last block unclosed -> matches the existing contract
	}
	end := start + len(e.openTag) + rel
	payload := strings.TrimSpace(output[start+len(e.openTag) : end])
	if payload == "" {
		return nil, fmt.Errorf("%w: empty payload", ErrInvalidResult)
	}

	dec := json.NewDecoder(strings.NewReader(payload))
	if e.strict {
		dec.DisallowUnknownFields()
	}
	var v T
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidResult, err)
	}

	// &v covers both value- and pointer-receiver Validate methods.
	if val, ok := any(&v).(Validator); ok {
		if err := val.Validate(); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidResult, err)
		}
	}
	return v, nil
}
