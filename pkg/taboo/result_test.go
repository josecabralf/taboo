package taboo

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// verdict implements Validator with a value receiver: Approved must be a known
// value, mirroring a JSON-Schema enum constraint done in Go.
type verdict struct {
	Approved string `json:"approved"`
}

func (v verdict) Validate() error {
	if v.Approved != "yes" && v.Approved != "no" {
		return fmt.Errorf("approved %q is not yes/no", v.Approved)
	}
	return nil
}

// review is the typed result shape used across the extractor tests; it stands in
// for whatever struct a caller would decode an agent's <result> block into.
type review struct {
	Summary string `json:"summary"`
	Score   int    `json:"score"`
}

func TestJSONResult_DecodesValidBlock(t *testing.T) {
	out := "chatter before\n<result>{\"summary\":\"looks good\",\"score\":7}</result>\nchatter after"

	got, err := JSONResult[review]().Extract(out)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	rv, ok := got.(review)
	if !ok {
		t.Fatalf("Extract returned %T, want taboo.review", got)
	}
	if rv.Summary != "looks good" || rv.Score != 7 {
		t.Errorf("Extract = %+v, want {Summary:looks good Score:7}", rv)
	}
}

func TestJSONResult_NoBlockIsErrNoResult(t *testing.T) {
	_, err := JSONResult[review]().Extract("the agent rambled but never emitted a block")
	if !errors.Is(err, ErrNoResult) {
		t.Errorf("Extract err = %v, want ErrNoResult", err)
	}
}

func TestJSONResult_UnclosedBlockIsErrNoResult(t *testing.T) {
	// An opening tag with no closing tag is not a complete block.
	_, err := JSONResult[review]().Extract("<result>{\"summary\":\"x\"}")
	if !errors.Is(err, ErrNoResult) {
		t.Errorf("Extract err = %v, want ErrNoResult for unclosed block", err)
	}
}

func TestJSONResult_MalformedJSONIsErrInvalidResult(t *testing.T) {
	// A block IS present, so this is invalid (not missing): the two failures must
	// stay distinguishable so a caller can tell "agent said nothing" from "agent
	// said something broken".
	_, err := JSONResult[review]().Extract("<result>{not json}</result>")
	if !errors.Is(err, ErrInvalidResult) {
		t.Errorf("Extract err = %v, want ErrInvalidResult", err)
	}
	if errors.Is(err, ErrNoResult) {
		t.Errorf("malformed block must not be reported as ErrNoResult: %v", err)
	}
}

func TestJSONResult_EmptyPayloadIsErrInvalidResult(t *testing.T) {
	// The block delimiters are present but enclose only whitespace: a block was
	// found (so not ErrNoResult) but there is nothing to decode.
	_, err := JSONResult[review]().Extract("<result>   \n  </result>")
	if !errors.Is(err, ErrInvalidResult) {
		t.Errorf("Extract err = %v, want ErrInvalidResult for empty payload", err)
	}
	if errors.Is(err, ErrNoResult) {
		t.Errorf("empty payload must not be reported as ErrNoResult: %v", err)
	}
	// The dedicated guard names the cause rather than surfacing the decoder's EOF.
	if err == nil || !strings.Contains(err.Error(), "empty payload") {
		t.Errorf("Extract err = %v, want it to name the empty payload", err)
	}
}

func TestJSONResult_LastBlockWins(t *testing.T) {
	// The agent echoed an example block, then emitted its real answer. The last
	// complete block is authoritative (robust to echoed examples / retry noise).
	out := "<result>{\"summary\":\"example\",\"score\":1}</result>\n" +
		"...thinking...\n" +
		"<result>{\"summary\":\"final\",\"score\":9}</result>"

	got, err := JSONResult[review]().Extract(out)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	rv := got.(review)
	if rv.Summary != "final" || rv.Score != 9 {
		t.Errorf("Extract = %+v, want the last block {final 9}", rv)
	}
}

func TestJSONResult_StrayCloseTagDoesNotResurrectEarlierBlock(t *testing.T) {
	// A stray </result> in prose sits between a complete earlier block and an
	// unclosed final block. Anchoring on the last </result> walks back to the
	// earlier block and returns it silently (json.Decoder stops at the first
	// value and ignores the trailing tag). Pairing from the open side instead
	// refuses the unclosed final block rather than serving a stale result.
	out := `before <result>{"summary":"first","score":1}</result> middle </result> <result>{"summary":"second","score":2}`

	_, err := JSONResult[review]().Extract(out)
	if !errors.Is(err, ErrNoResult) {
		t.Errorf("Extract err = %v, want ErrNoResult (unclosed final block; earlier block must not be resurrected)", err)
	}
}

func TestJSONResult_StrandedCloseTagDoesNotStrandFinalBlock(t *testing.T) {
	// A lone </result> in prose follows an earlier block and precedes the real,
	// still-open final block. Anchoring on the last </result> strands non-JSON
	// prose as the payload and hard-fails with ErrInvalidResult, discarding the
	// final block. Pairing from the open side reports the unclosed final block as
	// ErrNoResult, never the misleading ErrInvalidResult.
	out := `<result>reasoning</result> and then </result> <result>{"summary":"second","score":2}`

	_, err := JSONResult[review]().Extract(out)
	if !errors.Is(err, ErrNoResult) {
		t.Errorf("Extract err = %v, want ErrNoResult (final block unclosed, not invalid)", err)
	}
	if errors.Is(err, ErrInvalidResult) {
		t.Errorf("stranded prose must not be reported as ErrInvalidResult: %v", err)
	}
}

func TestJSONResult_IgnoresStrayCloseTagAroundClosedBlock(t *testing.T) {
	// A properly-closed final block is bracketed by stray </result> tags in
	// prose. The last opened, properly-closed block is authoritative and its
	// payload is exactly the JSON between its own tags, free of trailing prose.
	out := `<result>{"summary":"first","score":1}</result> noise </result> <result>{"summary":"final","score":9}</result> trailing </result>`

	got, err := JSONResult[review]().Extract(out)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rv := got.(review); rv.Summary != "final" || rv.Score != 9 {
		t.Errorf("Extract = %+v, want the last closed block {final 9}", rv)
	}
}

func TestJSONResult_CustomDelimitersDecodeCustomBlock(t *testing.T) {
	out := "noise ===BEGIN==={\"summary\":\"custom\",\"score\":3}===END=== noise"

	got, err := JSONResult[review](WithDelimiters("===BEGIN===", "===END===")).Extract(out)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	rv := got.(review)
	if rv.Summary != "custom" || rv.Score != 3 {
		t.Errorf("Extract = %+v, want {custom 3}", rv)
	}
}

func TestJSONResult_DefaultDelimitersMissCustomTags(t *testing.T) {
	// Output tagged with custom delimiters carries no default <result> block, so
	// the default extractor must report ErrNoResult rather than half-matching.
	out := "noise ===BEGIN==={\"summary\":\"custom\",\"score\":3}===END=== noise"

	if _, err := JSONResult[review]().Extract(out); !errors.Is(err, ErrNoResult) {
		t.Errorf("default delimiters err = %v, want ErrNoResult (custom tags shouldn't match)", err)
	}
}

func TestJSONResult_LenientIgnoresUnknownFields(t *testing.T) {
	// Default is lenient: agent stdout is chatty and a stray key on an otherwise
	// correct result should not fail the run.
	out := "<result>{\"summary\":\"ok\",\"score\":5,\"extra\":\"ignored\"}</result>"

	got, err := JSONResult[review]().Extract(out)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rv := got.(review); rv.Summary != "ok" || rv.Score != 5 {
		t.Errorf("Extract = %+v, want {ok 5} (extra field ignored)", rv)
	}
}

func TestJSONResult_StrictFieldsRejectsUnknown(t *testing.T) {
	out := "<result>{\"summary\":\"ok\",\"score\":5,\"extra\":\"nope\"}</result>"

	_, err := JSONResult[review](WithStrictFields()).Extract(out)
	if !errors.Is(err, ErrInvalidResult) {
		t.Errorf("Extract err = %v, want ErrInvalidResult for unknown field under strict mode", err)
	}
}

func TestJSONResult_ValidatorPasses(t *testing.T) {
	got, err := JSONResult[verdict]().Extract("<result>{\"approved\":\"yes\"}</result>")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rv := got.(verdict); rv.Approved != "yes" {
		t.Errorf("Extract = %+v, want approved=yes", rv)
	}
}

func TestJSONResult_ValidatorRejects(t *testing.T) {
	// Decodes fine as JSON but fails the semantic constraint, so it is invalid.
	_, err := JSONResult[verdict]().Extract("<result>{\"approved\":\"maybe\"}</result>")
	if !errors.Is(err, ErrInvalidResult) {
		t.Errorf("Extract err = %v, want ErrInvalidResult from Validate()", err)
	}
}
