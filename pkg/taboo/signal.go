package taboo

import (
	"bytes"
	"io"
)

// SignalScanner is an io.Writer that forwards every byte to an underlying
// writer while watching the stream for a fixed completion sentinel. Detection
// spans write-chunk boundaries: the sentinel is found even when split across
// successive Write calls. It is the seam through which the Orchestrator decides
// an agent has signaled completion mid-run.
type SignalScanner struct {
	signal []byte
	dst    io.Writer
	tail   []byte // trailing bytes of prior writes, to catch a boundary-split match
	found  bool
}

// NewSignalScanner returns a scanner that tees writes to dst (nil = discard)
// and reports whether signal has appeared in the stream. An empty signal is
// never detected.
func NewSignalScanner(signal string, dst io.Writer) *SignalScanner {
	if dst == nil {
		dst = io.Discard
	}
	return &SignalScanner{signal: []byte(signal), dst: dst}
}

// Write forwards p to the underlying writer and scans for the sentinel,
// prepending the retained tail so a match straddling the boundary is caught.
func (s *SignalScanner) Write(p []byte) (int, error) {
	if len(s.signal) > 0 && !s.found {
		window := make([]byte, 0, len(s.tail)+len(p))
		window = append(window, s.tail...)
		window = append(window, p...)
		if bytes.Contains(window, s.signal) {
			s.found = true
		}
		// Retain the last len(signal)-1 bytes: the longest suffix that could
		// form the prefix of a sentinel completing on the next write.
		if keep := len(s.signal) - 1; len(window) > keep {
			s.tail = window[len(window)-keep:]
		} else {
			s.tail = window
		}
	}
	return s.dst.Write(p)
}

// Found reports whether the sentinel has been seen in the stream so far.
func (s *SignalScanner) Found() bool { return s.found }
