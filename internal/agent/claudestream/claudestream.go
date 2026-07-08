// Package claudestream decodes Claude Code's `--output-format stream-json` output.
// In headless `-p` mode (no --include-partial-messages) the CLI emits JSONL: a
// `system` init event, `assistant` events with text and tool_use blocks, `user`
// events with tool_result blocks, and a terminal `result` event whose `result`
// field is the unescaped final assistant text. This is the single place that
// depends on that external schema; ResultText reads the result line for capture
// and NewRenderer reads assistant/user events for display.
package claudestream

import (
	"encoding/json"
	"strings"
)

// ResultText extracts the clean final text from a stream-json run: the `result`
// field of the last line whose "type" is "result". If no result line is present
// (crashed or truncated stream), it returns rawJSONL unchanged, so a malformed
// stream degrades to scanning what was captured rather than emptying Output.
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
			// Not a decodable event line (verbose noise, partial line); skip it.
			continue
		}
		if e.Type == "result" {
			// Last result line wins.
			text = e.Result
		}
	}
	return text
}
