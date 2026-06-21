package exec

import (
	"context"
	"io"
	"os"
	osexec "os/exec"
	"strings"
)

// Cmd is a single host-side process invocation (workshop or git).
type Cmd struct {
	Name   string    // executable, e.g. "workshop" or "git"
	Args   []string  // arguments
	Dir    string    // working directory on the host (empty = inherit)
	Env    []string  // extra environment ("NAME=value"), appended to the host env
	Stdin  io.Reader // optional
	Stdout io.Writer // optional; nil discards
	Stderr io.Writer // optional; nil discards
}

// Commander runs host-side commands. It is the single side-effecting seam in
// taboo: the real implementation shells out, while tests substitute a fake that
// records invocations.
type Commander interface {
	Run(ctx context.Context, c Cmd) error
}

// execCommander is the production Commander: it shells out via os/exec.
type execCommander struct{}

// NewExecCommander returns a Commander that runs commands as real host
// processes.
func NewExecCommander() Commander { return execCommander{} }

// Output runs cmd with a fresh stdout buffer and returns the raw captured
// stdout together with the run error. The string is untrimmed: callers that
// need trimming do it themselves. Any Stdout already set on cmd is overwritten.
func Output(ctx context.Context, c Commander, cmd Cmd) (string, error) {
	var buf strings.Builder
	cmd.Stdout = &buf
	err := c.Run(ctx, cmd)
	return buf.String(), err
}

func (execCommander) Run(ctx context.Context, c Cmd) error {
	// Running caller-supplied commands is this type's entire purpose; the
	// command and args originate from trusted definition config, not end users.
	cmd := osexec.CommandContext(ctx, c.Name, c.Args...) // #nosec G204
	cmd.Dir = c.Dir
	cmd.Stdin = c.Stdin
	cmd.Stdout = c.Stdout
	cmd.Stderr = c.Stderr
	// Inherit the host environment so `workshop exec --env NAME` can resolve
	// values held by this process; append any extra entries.
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	return cmd.Run()
}
