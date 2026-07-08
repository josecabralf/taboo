package app

import (
	"encoding/json"
	"fmt"
	"io"
)

// jsonCheck is the machine-readable shape of one check in --json output.
type jsonCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// jsonReport is the top-level --json document; ok is true iff no check is an error.
type jsonReport struct {
	OK     bool        `json:"ok"`
	Checks []jsonCheck `json:"checks"`
}

// writeHuman prints a human-readable report under title, one line per check, with
// a verdict footer; doctor and validate render the same shape under different
// titles.
func writeHuman(w io.Writer, title string, checks []check) {
	_, _ = fmt.Fprintln(w, title)
	for _, c := range checks {
		_, _ = fmt.Fprintf(w, "  [%-5s] %-16s %s\n", c.Status.token(), c.Name, c.Message)
	}
	if anyError(checks) {
		_, _ = fmt.Fprintln(w, "result: FAIL (one or more errors above)")
		return
	}
	_, _ = fmt.Fprintln(w, "result: OK")
}

// writeJSON prints the machine-readable report, returning any encoding error.
func writeJSON(w io.Writer, checks []check) error {
	rep := jsonReport{OK: !anyError(checks), Checks: make([]jsonCheck, len(checks))}
	for i, c := range checks {
		rep.Checks[i] = jsonCheck{Name: c.Name, Status: c.Status.token(), Message: c.Message}
	}
	return writeIndentedJSON(w, rep)
}

// writeIndentedJSON encodes v to w as 2-space-indented JSON. Every --json emitter
// routes through it so the encoding convention has one home.
func writeIndentedJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// emptyIfNil normalizes a nil slice to an empty one so it marshals as [] rather
// than null, the invariant every --json document in the package shares.
func emptyIfNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
