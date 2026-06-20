// Package diffmap parses a unified diff into the set of addressable new-side
// positions a PR review comment may target: the added ('+') and context (' ')
// lines, keyed by "path:line". A GitHub review rejects an inline comment whose
// position is not on the diff's RIGHT side, so callers filter their comments
// through a parsed Set and silently drop the rest.
package diffmap

import (
	"strconv"
	"strings"
)

// Set is the addressable new-side positions of a unified diff.
type Set struct {
	m map[string]struct{}
}

// Has reports whether path:line is an addressable new-side position in the diff.
func (s Set) Has(path string, line int) bool {
	_, ok := s.m[key(path, line)]
	return ok
}

func key(path string, line int) string {
	return path + ":" + strconv.Itoa(line)
}

// Parse walks a unified diff and collects every addressable new-side position.
// It tracks the current file from each '+++ b/<path>' header and the new-side
// line counter from each hunk header '@@ -a,b +c,d @@'; added and context lines
// record a position and advance the counter, while '-' and '\ No newline' lines
// do not.
func Parse(diff string) Set {
	s := Set{m: make(map[string]struct{})}
	var file string
	var line int
	inHunk := false
	for _, l := range strings.Split(diff, "\n") {
		l = strings.TrimSuffix(l, "\r") // tolerate CRLF-terminated diffs
		switch {
		case strings.HasPrefix(l, "diff --git "):
			// The next file's preamble begins; leave the previous hunk body.
			file, inHunk = "", false
		case strings.HasPrefix(l, "@@ "):
			// A hunk header resets the new-side counter and opens a hunk body.
			line = hunkNewStart(l)
			inHunk = true
		case !inHunk:
			// Between hunks, only the new-side file header matters: '+++ b/<path>'
			// names the file, while any other '+++ ' (e.g. /dev/null for a pure
			// deletion) clears it. Every other header line ('---', index, rename
			// metadata) is ignored.
			if strings.HasPrefix(l, "+++ b/") {
				file = strings.TrimPrefix(l, "+++ b/")
			} else if strings.HasPrefix(l, "+++ ") {
				file = ""
			}
		case file == "":
			// Inside a hunk that addresses nothing (the deleted side of the diff).
		case strings.HasPrefix(l, "+"):
			// Added line: present only on the new side. Inside a hunk the leading
			// byte alone classifies the line, so body content that resembles a header
			// (an added line reading '+++ b/x') is recorded here, never misread as a
			// file header and never desynced from the counter.
			s.m[key(file, line)] = struct{}{}
			line++
		case strings.HasPrefix(l, " "):
			// Context line: present on both sides.
			s.m[key(file, line)] = struct{}{}
			line++
		}
		// '-' (old-side), '\ No newline' metadata, and the trailing split artifact
		// fall through: they record no position and do not advance the counter.
	}
	return s
}

// hunkNewStart extracts the new-side start line from a hunk header
// '@@ -a,b +c,d @@': it returns c from the '+c,d' (or '+c') field.
func hunkNewStart(header string) int {
	for _, field := range strings.Fields(header) {
		if !strings.HasPrefix(field, "+") {
			continue
		}
		field = strings.TrimPrefix(field, "+")
		if i := strings.IndexByte(field, ','); i >= 0 {
			field = field[:i]
		}
		n, err := strconv.Atoi(field)
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}
