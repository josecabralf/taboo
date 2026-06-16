package main

// severity is a check outcome's seriousness. Only error affects the exit code.
type severity int

const (
	statusOK severity = iota
	statusWarn
	statusError
)

// token is the lowercase word printed (and emitted in JSON) for a severity.
func (s severity) token() string {
	switch s {
	case statusOK:
		return "ok"
	case statusWarn:
		return "warn"
	case statusError:
		return "error"
	default:
		return "unknown"
	}
}

// check is one host- or config-readiness result: a stable name, its severity,
// and a human-readable message explaining the outcome and any remedy.
type check struct {
	Name    string
	Status  severity
	Message string
}

// ok builds a passing check.
func ok(name, msg string) check { return check{Name: name, Status: statusOK, Message: msg} }

// warn builds an advisory check.
func warn(name, msg string) check { return check{Name: name, Status: statusWarn, Message: msg} }

// fail builds a failing (error) check.
func fail(name, msg string) check { return check{Name: name, Status: statusError, Message: msg} }

// anyError reports whether any check in the slice is an error, which is the sole
// signal that drives a non-zero exit.
func anyError(checks []check) bool {
	for _, c := range checks {
		if c.Status == statusError {
			return true
		}
	}
	return false
}
