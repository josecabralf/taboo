package app

// severity is a check outcome's seriousness. Only statusError affects the exit code.
type severity int

const (
	statusOK severity = iota
	statusWarn
	statusError
)

// token is the lowercase word printed and emitted in JSON for a severity.
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

// check is one host- or config-readiness result.
type check struct {
	Name    string
	Status  severity
	Message string
}

func ok(name, msg string) check { return check{Name: name, Status: statusOK, Message: msg} }

func warn(name, msg string) check { return check{Name: name, Status: statusWarn, Message: msg} }

func fail(name, msg string) check { return check{Name: name, Status: statusError, Message: msg} }

// anyError reports whether any check is an error, the sole signal that drives a
// non-zero exit.
func anyError(checks []check) bool {
	for _, c := range checks {
		if c.Status == statusError {
			return true
		}
	}
	return false
}
