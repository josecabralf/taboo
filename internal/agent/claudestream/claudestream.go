// Package claudestream decodes Claude Code's `--output-format stream-json`
// output. In headless `-p` mode (without --include-partial-messages) that flag
// makes the CLI emit JSONL — one envelope event per line: a `system` init event,
// `assistant` events whose message.content carries text and tool_use blocks,
// `user` events whose content carries tool_result blocks, and a terminal result
// event:
//
//	{"type":"result","result":"<clean plain text>", …}
//
// whose `result` field is the unescaped, final assistant text. (The fine-grained
// message_start/content_block_delta/message_stop events only appear under
// --include-partial-messages, which taboo does not pass.) claudestream is the
// single place that depends on this external event schema; ResultText reads the
// result line for the capture seam and NewRenderer reads the assistant/user
// events for the display seam, and the tests pin representative fixtures.
package claudestream

import (
	"encoding/json"
	"strings"
)

// ResultText extracts the clean final text from a Claude Code stream-json run.
// The rawJSONL argument is the agent's full captured stdout: one JSON event per
// line. ResultText returns the `result` field of the last line whose "type" is
// "result" — the unescaped plain text the orchestrator scans for the completion
// sentinel and the <result>{…}</result> block, exactly as `--output-format text`
// used to provide.
//
// If no result line is present (a crashed or truncated stream), ResultText
// returns rawJSONL unchanged, so a malformed stream degrades to "scan whatever
// we captured" rather than silently emptying Output. An is_error result still
// carries its `result` field, so it is returned verbatim and the orchestrator's
// existing extraction-failure handling takes over.
func ResultText(rawJSONL string) string {
	type event struct {
		Type   string `json:"type"`
		Result string `json:"result"`
	}
	text := rawJSONL
	for line := range strings.SplitSeq(rawJSONL, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			// Not a decodable event line (verbose noise, partial line): ignore it
			// and keep scanning. The result line is well-formed JSON.
			continue
		}
		if e.Type == "result" {
			// Last result line wins: a stream with multiple result events (none
			// expected, but cheap to tolerate) resolves to its final one.
			text = e.Result
		}
	}
	return text
}
