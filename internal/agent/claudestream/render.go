package claudestream

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// NewRenderer returns an io.Writer that turns Claude Code's raw stream-json
// stdout into a human-readable transcript and forwards it to out. It is the
// display-side counterpart to ResultText: the runner tees the agent's stdout
// into both a capture buffer (which ResultText later reduces to res.Output) and
// this renderer (which writes the transcript to the workflow log), so display
// and scan stay separate concerns.
//
// The renderer decodes the envelope events that --output-format stream-json
// emits without --include-partial-messages: an `assistant` event whose
// message.content carries text and tool_use blocks, and a `user` event whose
// content carries tool_result blocks. It emits one transcript line per assistant
// text block, per tool call (`> Name(arg)`), and per tool result. It ignores the
// `system` init event, the terminal `result` event (its text is already shown by
// the preceding assistant event, so rendering it would duplicate the final
// answer), and any event type or content block it does not recognize — so
// event-schema drift degrades to a quieter transcript rather than a panic.
//
// Render buffers across Write calls: stdout arrives in arbitrary chunks that may
// split a JSON line mid-event, so bytes are accumulated and only complete,
// newline-terminated lines are decoded. A trailing line with no final newline is
// left unrendered — in a well-formed stream that is only the result event, which
// the renderer skips anyway — so no Close is needed.
func NewRenderer(out io.Writer) io.Writer {
	return &renderer{out: out}
}

type renderer struct {
	out io.Writer
	buf []byte // bytes of an incomplete trailing line, carried to the next Write.
}

// Write accumulates stdout and renders every complete line it now holds. It
// always reports len(p) bytes consumed so it satisfies io.MultiWriter even when
// a chunk decodes to nothing; a write error from out is returned but the byte
// count is never short.
func (r *renderer) Write(p []byte) (int, error) {
	r.buf = append(r.buf, p...)
	for {
		i := bytes.IndexByte(r.buf, '\n')
		if i < 0 {
			break
		}
		line := r.buf[:i]
		r.buf = r.buf[i+1:]
		if err := r.renderLine(line); err != nil {
			return len(p), err
		}
	}
	return len(p), nil
}

// streamEvent is the subset of a stream-json envelope the renderer reads. One
// struct covers both `assistant` and `user` events; each content block populates
// only the fields its own type defines (Text for text blocks, Name/Input for
// tool_use, ToolResult/IsError for tool_result) and json leaves the rest zero.
type streamEvent struct {
	Type    string    `json:"type"`
	Message streamMsg `json:"message"`
}

type streamMsg struct {
	Content []streamBlock `json:"content"`
}

type streamBlock struct {
	Type       string          `json:"type"`
	Text       string          `json:"text"`
	Name       string          `json:"name"`
	Input      json.RawMessage `json:"input"`
	ToolResult json.RawMessage `json:"content"`
	IsError    bool            `json:"is_error"`
}

func (r *renderer) renderLine(line []byte) error {
	if len(bytes.TrimSpace(line)) == 0 {
		return nil
	}
	var e streamEvent
	if json.Unmarshal(line, &e) != nil {
		// Verbose diagnostics, a partial line, or a shape we don't model: skip it
		// rather than fail the display. The result line is well-formed JSON but is
		// intentionally not rendered here.
		return nil
	}
	switch e.Type {
	case "assistant", "user":
		for _, b := range e.Message.Content {
			if err := r.emitBlock(b); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *renderer) emitBlock(b streamBlock) error {
	switch b.Type {
	case "text":
		// Assistant prose is shown verbatim (multi-line preserved); only the
		// trailing newline is dropped so line() owns line termination.
		if s := strings.TrimRight(b.Text, "\n"); strings.TrimSpace(s) != "" {
			return r.line(s)
		}
	case "tool_use":
		if b.Name != "" {
			return r.line("> " + b.Name + "(" + toolSummary(b.Input) + ")")
		}
	case "tool_result":
		if s, ok := resultText(b.ToolResult); ok {
			if b.IsError {
				return r.line("  [error] " + s)
			}
			return r.line("  " + s)
		}
	}
	return nil
}

func (r *renderer) line(s string) error {
	_, err := io.WriteString(r.out, s+"\n")
	return err
}

// toolSummary renders a tool_use input as a single readable argument. It prefers
// the one field that best identifies the call (a shell command, a file path, a
// search pattern) and falls back to the compact JSON of the whole input for
// tools it has no salient key for, so an unfamiliar tool still shows something.
func toolSummary(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(input, &m) != nil {
		return ""
	}
	for _, k := range []string{"command", "file_path", "path", "pattern", "query", "url"} {
		if raw, ok := m[k]; ok {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				return oneLine(s)
			}
		}
	}
	return oneLine(string(input))
}

// resultText pulls a one-line summary out of a tool_result's content. The
// content is usually a plain string, but the Messages API also allows an array
// of content blocks, so a string is handled first and then text blocks. It
// reports ok=false when there is nothing worth showing.
func resultText(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		s = oneLine(s)
		return s, s != ""
	}
	var blocks []streamBlock
	if json.Unmarshal(raw, &blocks) == nil {
		for _, b := range blocks {
			if b.Type == "text" {
				if s := oneLine(b.Text); s != "" {
					return s, true
				}
			}
		}
	}
	return "", false
}

// oneLine collapses a value to its first non-empty line and caps its length, so
// a noisy tool argument or result (a multi-line file, a long command output)
// contributes a single bounded transcript line. Truncation falls on a rune
// boundary so a multi-byte character is never split.
func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	const max = 200
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "..."
	}
	return s
}
