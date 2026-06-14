package taboo

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Validator is the opt-in semantic-validation hook: if a decoded result type
// implements it, the extractor calls Validate after decoding and treats a
// non-nil error as an invalid result. It is the dependency-free stand-in for
// JSON-Schema's required/enum/range constraints (ADR 0002).
type Validator interface {
	Validate() error
}

// ErrNoResult means no complete result block was found in the agent's output
// (no closing tag, or no opening tag before the last closing tag).
var ErrNoResult = errors.New("taboo: no result block found")

// ErrInvalidResult means a result block was found but its payload would not
// decode/validate: empty, malformed JSON, type-mismatched, unknown fields under
// strict mode, or rejected by a Validator. Wrapped with %w so the underlying
// detail survives in the message while errors.Is(err, ErrInvalidResult) holds.
var ErrInvalidResult = errors.New("taboo: result block invalid")

// ResultExtractor turns an agent's captured output into a typed, validated
// result. It is the structured-output seam: the orchestrator holds one as an
// interface so the surrounding API stays non-generic (ADR 0002). Implementations
// are pure functions over the output string.
type ResultExtractor interface {
	// Extract finds the result block in output and decodes its payload into a
	// typed value, returned as any for the caller to type-assert.
	Extract(output string) (any, error)
}

// extractOptions holds the tunable behavior of a JSON extractor, set via Option
// values passed to JSONResult.
type extractOptions struct {
	openTag, closeTag string
	strict            bool
}

// Option configures a JSONResult extractor.
type Option func(*extractOptions)

// WithDelimiters overrides the block delimiters (default "<result>" /
// "</result>").
func WithDelimiters(open, close string) Option {
	return func(o *extractOptions) {
		o.openTag, o.closeTag = open, close
	}
}

// WithStrictFields makes decoding reject a payload that carries fields absent
// from T (DisallowUnknownFields). Off by default because agent stdout is chatty.
func WithStrictFields() Option {
	return func(o *extractOptions) {
		o.strict = true
	}
}

// jsonExtractor decodes the JSON payload of a result block into T.
type jsonExtractor[T any] struct {
	extractOptions
}

// JSONResult builds a ResultExtractor that locates the last
// <result>...</result> block in the agent's output and decodes its JSON payload
// into T. The caller's struct is the schema (ADR 0002). Behavior is tuned via
// options such as WithDelimiters.
func JSONResult[T any](opts ...Option) ResultExtractor {
	o := extractOptions{openTag: "<result>", closeTag: "</result>"}
	for _, opt := range opts {
		opt(&o)
	}
	return jsonExtractor[T]{extractOptions: o}
}

// Extract implements ResultExtractor.
func (e jsonExtractor[T]) Extract(output string) (any, error) {
	end := strings.LastIndex(output, e.closeTag)
	if end < 0 {
		return nil, ErrNoResult
	}
	start := strings.LastIndex(output[:end], e.openTag)
	if start < 0 {
		return nil, ErrNoResult
	}
	payload := strings.TrimSpace(output[start+len(e.openTag) : end])

	dec := json.NewDecoder(strings.NewReader(payload))
	if e.strict {
		dec.DisallowUnknownFields()
	}
	var v T
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidResult, err)
	}

	// Checking &v covers both value- and pointer-receiver Validate methods, since
	// the pointer's method set includes both.
	if val, ok := any(&v).(Validator); ok {
		if err := val.Validate(); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidResult, err)
		}
	}
	return v, nil
}
