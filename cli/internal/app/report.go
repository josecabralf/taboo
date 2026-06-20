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

// jsonReport is the top-level --json document: ok is true iff no check is an
// error.
type jsonReport struct {
	OK     bool        `json:"ok"`
	Checks []jsonCheck `json:"checks"`
}

// writeHuman prints a human-readable report under title, one line per check,
// with a footer summarizing the overall verdict. The title is supplied by the
// caller (doctor and validate render the same shape under different headers).
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

// writeJSON prints the machine-readable report. It returns any encoding error;
// the caller still returns the failure sentinel separately when a check errored.
func writeJSON(w io.Writer, checks []check) error {
	rep := jsonReport{OK: !anyError(checks), Checks: make([]jsonCheck, len(checks))}
	for i, c := range checks {
		rep.Checks[i] = jsonCheck{Name: c.Name, Status: c.Status.token(), Message: c.Message}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}
