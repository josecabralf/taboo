package exec

import (
	"context"
	"fmt"
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

// Commander runs host-side commands, the single side-effecting seam in taboo:
// production shells out, tests substitute a fake that records invocations.
type Commander interface {
	Run(ctx context.Context, c Cmd) error
}

// execCommander is the production Commander; it shells out via os/exec.
type execCommander struct{}

// NewExecCommander returns a Commander that runs commands as real host processes.
func NewExecCommander() Commander { return execCommander{} }

// Output runs cmd with a fresh stdout buffer and returns the untrimmed captured
// stdout and the run error. Any Stdout already set on cmd is overwritten. On
// failure the error carries the command's stderr, like os/exec's cmd.Output().
func Output(ctx context.Context, c Commander, cmd Cmd) (string, error) {
	var out strings.Builder
	cmd.Stdout = &out
	// Capture stderr only when the caller didn't wire its own.
	var stderr strings.Builder
	if cmd.Stderr == nil {
		cmd.Stderr = &stderr
	}
	err := c.Run(ctx, cmd)
	if err != nil && stderr.Len() > 0 {
		err = fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out.String(), err
}

func (execCommander) Run(ctx context.Context, c Cmd) error {
	// Commands come from trusted definition config, not end users.
	cmd := osexec.CommandContext(ctx, c.Name, c.Args...) // #nosec G204
	cmd.Dir = c.Dir
	cmd.Stdin = c.Stdin
	cmd.Stdout = c.Stdout
	cmd.Stderr = c.Stderr
	// Inherit the host environment so `workshop exec --env NAME` can resolve values
	// this process holds; append any extra entries.
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	return cmd.Run()
}
