package taboo

import (
	"context"
	"fmt"
	"io"
	"time"
)

// Hook is a single setup command run at a lifecycle point. By default it runs
// on the host through the Commander seam; set InWorkshop to run it inside the
// workshop via `workshop exec` (cwd /workspace), where it sees the same mounts
// and credential env keys as the agent.
type Hook struct {
	// Command is the executable and its arguments, e.g. {"go","mod","download"}.
	Command []string
	// InWorkshop runs the command inside the workshop instead of on the host.
	InWorkshop bool
}

// Hooks groups the lifecycle hook points a run can supply.
type Hooks struct {
	// OnWorkshopReady hooks run after the workshop is started with the run's
	// worktree mounted, but before the agent execs. Use them to prepare the
	// environment the agent will see. They run on every run (the worktree is
	// swapped in per run), not once per workshop, so keep them idempotent and
	// cheap rather than modeling expensive one-time provisioning here.
	OnWorkshopReady []Hook
}

// hookCmd builds the Cmd that runs h, either on the host or inside the
// workshop. In-workshop hooks inherit cwd /workspace, the run's timeout, the
// agent's credential env keys, and its session-dir redirect (sessionEnv) so
// setup commands run with the same context as the agent. Host hooks run in the
// run's worktree and get neither env set (the redirect is a workshop path).
// Pure.
func hookCmd(proj, ws, worktree string, envKeys []string, sessionEnv []envAssignment, timeout time.Duration, h Hook) Cmd {
	if h.InWorkshop {
		opts := execOptions{cwd: workspaceTarget, timeout: timeout, envKeys: envKeys, env: sessionEnv}
		return Cmd{Name: "workshop", Args: execArgs(proj, ws, opts, h.Command)}
	}
	return Cmd{Name: h.Command[0], Args: h.Command[1:], Dir: worktree}
}

// runHooks runs hooks in order through the Commander seam, host hooks in the
// run's worktree and each hook bounded by the run timeout. Hook output goes to
// out (typically the run's stderr) so setup failures are diagnosable. A failure
// stops the sequence and is returned with context identifying the offending hook.
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
// hanging hook cannot stall the run before the agent execs.
func (r *Runner) runHook(ctx context.Context, timeout time.Duration, cmd Cmd) error {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return r.cmd.Run(ctx, cmd)
}
