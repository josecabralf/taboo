package claudestream

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// NewRenderer returns an io.Writer that turns Claude Code's stream-json stdout
// into a readable transcript and forwards it to out. It decodes `assistant` and
// `user` events, emitting one line per text block, tool call (`> Name(arg)`), and
// tool result; it skips the terminal `result` event (already shown by the
// preceding assistant event) and any unrecognized event, so schema drift degrades
// to a quieter transcript rather than a panic.
//
// It buffers across Write calls: stdout arrives in arbitrary chunks that may split
// a JSON line, so only complete newline-terminated lines are decoded. A trailing
// unterminated line is left unrendered (in a well-formed stream only the result
// event, skipped anyway), so no Close is needed.
func NewRenderer(out io.Writer) io.Writer {
	return &renderer{out: out}
}

type renderer struct {
	out io.Writer
	buf []byte // bytes of an incomplete trailing line, carried to the next Write.
}

// Write accumulates stdout and renders every complete line it now holds. It
// always reports len(p) consumed so it satisfies io.MultiWriter even when a chunk
// decodes to nothing; a write error is returned but the byte count is never short.
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

// streamEvent is the subset of a stream-json envelope the renderer reads; one
// struct covers both `assistant` and `user` events, each block populating only
// the fields its own type defines.
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
		// Verbose diagnostics, a partial line, or an unmodeled shape; skip rather
		// than fail the display. The result line is well-formed but not rendered here.
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
		// Assistant prose shown verbatim; only the trailing newline is dropped so
		// line() owns line termination.
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

// toolSummary renders a tool_use input as one readable argument, preferring the
// field that best identifies the call and falling back to the compact JSON of the
// whole input.
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

// resultText pulls a one-line summary from a tool_result's content. Content is
// usually a string but the Messages API also allows an array of blocks, so a
// string is handled first, then text blocks; ok=false when nothing is worth showing.
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

// oneLine collapses a value to its first non-empty line and caps its length, so a
// noisy argument or result contributes one bounded line. Truncation falls on a
// rune boundary so a multi-byte character is never split.
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
