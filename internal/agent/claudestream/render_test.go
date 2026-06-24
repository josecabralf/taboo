package claudestream

import (
	"io"
	"strings"
	"testing"
)

// A representative run — system init, assistant text, a tool call, its result,
// the final assistant text, and the terminal result line — renders to a clean
// transcript: assistant prose verbatim, tool calls as `> Name(arg)`, results
// indented. The system and result events contribute nothing (the final answer is
// already shown by the preceding assistant event).
func TestRenderer_GoldenTranscript(t *testing.T) {
	raw := strings.Join([]string{
		`{"type":"system","subtype":"init","model":"claude-opus-4-8"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"I'll check the hostname."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"cat /etc/hostname","description":"read hostname"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"thinkpad-t14\n","is_error":false}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"The host is thinkpad-t14."}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"The host is thinkpad-t14."}`,
		``,
	}, "\n")

	var buf strings.Builder
	if _, err := io.WriteString(NewRenderer(&buf), raw); err != nil {
		t.Fatalf("Write: %v", err)
	}

	want := strings.Join([]string{
		"I'll check the hostname.",
		"> Bash(cat /etc/hostname)",
		"  thinkpad-t14",
		"The host is thinkpad-t14.",
		"",
	}, "\n")
	if got := buf.String(); got != want {
		t.Errorf("transcript =\n%q\nwant\n%q", got, want)
	}
}

// stdout arrives in arbitrary chunks that can split a JSON line mid-event. The
// renderer must buffer the partial line and emit the event exactly once, only
// after the line completes.
func TestRenderer_BuffersPartialLines(t *testing.T) {
	var buf strings.Builder
	w := NewRenderer(&buf)

	// First chunk cuts the event in half: nothing is decodable yet.
	if _, err := io.WriteString(w, `{"type":"assistant","message":{"content":[{"type":"text","te`); err != nil {
		t.Fatalf("Write 1: %v", err)
	}
	if buf.String() != "" {
		t.Errorf("after partial write, transcript = %q, want empty", buf.String())
	}

	// Second chunk completes the line: the event now renders, exactly once.
	if _, err := io.WriteString(w, `xt":"hello"}]}}`+"\n"); err != nil {
		t.Fatalf("Write 2: %v", err)
	}
	if got := buf.String(); got != "hello\n" {
		t.Errorf("after completing write, transcript = %q, want %q", got, "hello\n")
	}
}

// Undecodable lines (verbose noise) and event types or content blocks the
// renderer does not model are skipped without panicking; a known block on the
// same line still renders — proof of graceful schema-drift handling.
func TestRenderer_IgnoresUnknownAndUndecodable(t *testing.T) {
	var buf strings.Builder
	raw := strings.Join([]string{
		`not json at all`,
		`{"type":"some_future_event","payload":{"whatever":1}}`,
		`{"type":"assistant","message":{"content":[{"type":"future_block","data":1},{"type":"text","text":"still here"}]}}`,
		``,
	}, "\n")
	if _, err := io.WriteString(NewRenderer(&buf), raw); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := buf.String(); got != "still here\n" {
		t.Errorf("transcript = %q, want %q (unknown events skipped, no panic)", got, "still here\n")
	}
}

// A tool with no salient input key falls back to its compact JSON input, and an
// error result is flagged with an [error] prefix.
func TestRenderer_ToolFallbackAndErrorResult(t *testing.T) {
	var buf strings.Builder
	raw := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Mystery","input":{"foo":"bar"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"boom","is_error":true}]}}`,
		``,
	}, "\n")
	if _, err := io.WriteString(NewRenderer(&buf), raw); err != nil {
		t.Fatalf("Write: %v", err)
	}
	want := "> Mystery({\"foo\":\"bar\"})\n  [error] boom\n"
	if got := buf.String(); got != want {
		t.Errorf("transcript = %q, want %q", got, want)
	}
}

// A noisy tool result — multi-line, or longer than the cap — is reduced to a
// single bounded line so one tool call never floods the log.
func TestRenderer_TruncatesNoisyResults(t *testing.T) {
	var buf strings.Builder
	long := strings.Repeat("x", 500)
	raw := `{"type":"user","message":{"content":[{"type":"tool_result","content":"first line\nsecond line"}]}}` + "\n" +
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"` + long + `"}]}}` + "\n"
	if _, err := io.WriteString(NewRenderer(&buf), raw); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "  first line\n") || strings.Contains(got, "second line") {
		t.Errorf("multi-line result not reduced to its first line: %q", got)
	}
	if !strings.Contains(got, "...") {
		t.Errorf("long result not truncated: %q", got)
	}
}

// A chunk that decodes to nothing (a partial line, no newline) must still report
// every byte consumed, or io.MultiWriter would treat it as a short write and
// abort the agent's stdout pipe.
func TestRenderer_WriteReportsFullLength(t *testing.T) {
	var buf strings.Builder
	p := []byte(`{"type":"assistant","message":{"content":[{"type":"text","te`)
	n, err := NewRenderer(&buf).Write(p)
	if err != nil || n != len(p) {
		t.Errorf("Write = (%d, %v), want (%d, nil)", n, err, len(p))
	}
}
