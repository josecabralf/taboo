package taboo

import (
	"context"
	"fmt"
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
	// OnWorkshopReady run after the workshop is started with the run's worktree
	// mounted, but before the agent execs — the point to prepare the
	// environment the agent will see.
	OnWorkshopReady []Hook
}

// hookCmd builds the Cmd that runs h, either on the host or inside the
// workshop. In-workshop hooks inherit cwd /workspace and the agent's credential
// env keys so setup commands run with the same context as the agent. Pure.
func hookCmd(proj, ws string, envKeys []string, h Hook) Cmd {
	if h.InWorkshop {
		opts := execOptions{cwd: workspaceTarget, envKeys: envKeys}
		return Cmd{Name: "workshop", Args: execArgs(proj, ws, opts, h.Command)}
	}
	return Cmd{Name: h.Command[0], Args: h.Command[1:]}
}

// runHooks runs hooks in order through the Commander seam. A failure stops the
// sequence and is returned with context identifying the offending hook.
func (r *Runner) runHooks(ctx context.Context, hooks []Hook) error {
	for i, h := range hooks {
		if len(h.Command) == 0 {
			continue
		}
		cmd := hookCmd(r.cfg.ProjectDir, r.cfg.Workshop, r.cfg.EnvKeys, h)
		if err := r.cmd.Run(ctx, cmd); err != nil {
			return fmt.Errorf("hook %d %v: %w", i, h.Command, err)
		}
	}
	return nil
}
