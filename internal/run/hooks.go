package run

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/josecabralf/taboo/internal/exec"
	"github.com/josecabralf/taboo/internal/workshop"
)

// Hook is a single setup command run at a lifecycle point.
type Hook struct {
	// Command is the executable and its arguments.
	Command []string
	// InWorkshop runs the command inside the workshop instead of on the host.
	InWorkshop bool
}

// Hooks groups the lifecycle hook points a run can supply.
type Hooks struct {
	// OnWorkshopReady hooks run after the workshop is started with the run's
	// worktree mounted, but before the agent execs. They run on every run, not
	// once per workshop, so keep them idempotent and cheap.
	OnWorkshopReady []Hook
}

// hookCmd builds the Cmd that runs h. In-workshop hooks inherit the agent's cwd,
// timeout, credential env keys, and session-dir redirect (sessionEnv) so setup
// commands run with the same context as the agent. Host hooks run in the run's
// worktree with neither env set (the redirect is a workshop path).
func hookCmd(proj, ws, worktree string, envKeys []string, sessionEnv []workshop.EnvAssignment, timeout time.Duration, h Hook) exec.Cmd {
	if h.InWorkshop {
		opts := workshop.ExecOptions{Cwd: workshop.WorkspaceTarget, Timeout: timeout, EnvKeys: envKeys, Env: sessionEnv}
		return exec.Cmd{Name: "workshop", Args: workshop.ExecArgs(proj, ws, opts, h.Command)}
	}
	return exec.Cmd{Name: h.Command[0], Args: h.Command[1:], Dir: worktree}
}

// runHooks runs hooks in order, each bounded by timeout, output to out. A
// failure stops the sequence and is returned identifying the offending hook.
func (r *Runner) runHooks(ctx context.Context, worktree string, timeout time.Duration, out io.Writer, hooks []Hook) error {
	for i, h := range hooks {
		if len(h.Command) == 0 {
			continue
		}
		cmd := hookCmd(r.cfg.ProjectDir, r.cfg.Workshop, worktree, r.cfg.Agent.CredentialEnvKeys(), r.sessionEnv(), timeout, h)
		cmd.Stdout, cmd.Stderr = out, out
		if err := r.runHook(ctx, timeout, cmd); err != nil {
			return fmt.Errorf("hook %d %v: %w", i, h.Command, err)
		}
	}
	return nil
}

// runHook runs a single hook command, bounding it by timeout when set so a
// hanging hook cannot stall the run.
func (r *Runner) runHook(ctx context.Context, timeout time.Duration, cmd exec.Cmd) error {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return r.cmd.Run(ctx, cmd)
}
