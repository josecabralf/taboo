package claudestream

import "testing"

// A representative stream: message/content-block events, then the terminal
// result line. ResultText returns the result field, unescaped — the <result>
// block and the completion sentinel survive intact for the orchestrator.
func TestResultText_ExtractsResultLine(t *testing.T) {
	raw := `{"type":"message_start","message":{"id":"msg_1"}}
{"type":"content_block_start","content_block":{"type":"text"}}
{"type":"content_block_delta","delta":{"text":"working"}}
{"type":"content_block_start","content_block":{"type":"tool_use","name":"Bash"}}
{"type":"message_stop"}
{"type":"result","subtype":"success","is_error":false,"result":"DONE <result>{\"ok\":true}</result>"}`

	want := `DONE <result>{"ok":true}</result>`
	if got := ResultText(raw); got != want {
		t.Errorf("ResultText() = %q, want %q", got, want)
	}
}

// No result line (a crashed or truncated stream): ResultText returns the raw
// input unchanged so the completion-signal scan runs over whatever was captured
// rather than seeing an empty Output.
func TestResultText_NoResultLineReturnsRaw(t *testing.T) {
	raw := `{"type":"message_start"}
{"type":"content_block_delta","delta":{"text":"partial"}}`
	if got := ResultText(raw); got != raw {
		t.Errorf("ResultText() = %q, want raw input %q", got, raw)
	}
}

// Empty input has no result line, so it returns unchanged (empty).
func TestResultText_Empty(t *testing.T) {
	if got := ResultText(""); got != "" {
		t.Errorf("ResultText(\"\") = %q, want empty", got)
	}
}

// Multiple result lines: only the final one wins.
func TestResultText_LastResultWins(t *testing.T) {
	raw := `{"type":"result","result":"first"}
{"type":"result","result":"second"}`
	if got := ResultText(raw); got != "second" {
		t.Errorf("ResultText() = %q, want %q (last result wins)", got, "second")
	}
}

// An is_error result still carries its result field, returned verbatim; the
// orchestrator handles extraction failure downstream.
func TestResultText_ErrorResultReturnedVerbatim(t *testing.T) {
	raw := `{"type":"result","subtype":"error_max_turns","is_error":true,"result":"hit the turn limit"}`
	if got := ResultText(raw); got != "hit the turn limit" {
		t.Errorf("ResultText() = %q, want the error result text", got)
	}
}

// Non-JSON noise interleaved with events (verbose diagnostics, blank lines) is
// skipped; the result line is still found.
func TestResultText_SkipsUndecodableLines(t *testing.T) {
	raw := `not json at all

{"type":"result","result":"clean"}
trailing noise`
	if got := ResultText(raw); got != "clean" {
		t.Errorf("ResultText() = %q, want %q", got, "clean")
	}
}
