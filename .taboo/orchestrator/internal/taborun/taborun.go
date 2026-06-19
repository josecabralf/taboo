// Package taborun resolves a named workflow from a .taboo/taboo.yaml into a
// taboo run and drives it through taboo.Orchestrator. It is the orchestrator's
// single seam onto the host taboo library: callers hand it a workflow name plus
// prompt variables and receive the run's branch, worktree, commit, and output.
package taborun

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/josecabralf/taboo/pkg/taboo"
)

// Options describes one orchestrated taboo run: which workflow to run from which
// config, the branch it creates, the prompt variables to substitute, the host
// paths taboo operates on, and the exec/output seams.
type Options struct {
	ConfigPath string            // path to .taboo/taboo.yaml
	Workflow   string            // workflow name, e.g. "implement"
	Branch     string            // branch the run creates
	Vars       map[string]string // prompt variables substituted into the workflow prompt
	RepoPath   string            // absolute path to the host git repository
	ProjectDir string            // host directory taboo owns (the .taboo dir)
	Commander  taboo.Commander   // exec seam; nil uses taboo.NewExecCommander()
	Stdout     io.Writer         // agent stdout sink; nil discards
	Stderr     io.Writer         // agent stderr sink; nil discards
}

// Result reports the outcome of an orchestrated run: the final iteration's
// branch, worktree, commit, and captured agent output.
type Result struct {
	Branch       string
	WorktreePath string
	Commit       string
	Output       string
}

// plan is the resolved, side-effect-free description of a run: the taboo.Config
// that names the workshop and agent, and the OrchestratedRequest that carries
// the substituted prompt and loop knobs. Splitting it out keeps buildPlan
// unit-testable without provisioning a real workshop.
type plan struct {
	cfg taboo.Config
	req taboo.OrchestratedRequest
}

// buildPlan resolves the named workflow into a taboo.Config and
// OrchestratedRequest: it reads and substitutes the workflow prompt (resolved
// relative to the config file's directory), derives the per-agent workshop name,
// and resolves the iteration, timeout, and completion-signal settings (workflow
// overrides, else defaults). It performs no workshop or git side effects.
func buildPlan(pc *taboo.ProjectConfig, opts Options) (plan, error) {
	wf, ok := pc.Workflows[opts.Workflow]
	if !ok {
		return plan{}, fmt.Errorf("taborun: unknown workflow %q", opts.Workflow)
	}

	promptPath := filepath.Join(filepath.Dir(opts.ConfigPath), wf.PromptFile)
	// The prompt path is derived from the trusted config file's directory and
	// the workflow's prompt-file, not from end-user input.
	tmpl, err := os.ReadFile(promptPath) // #nosec G304
	if err != nil {
		return plan{}, fmt.Errorf("taborun: read prompt %s: %w", promptPath, err)
	}
	prompt, err := taboo.Substitute(string(tmpl), opts.Vars)
	if err != nil {
		return plan{}, fmt.Errorf("taborun: workflow %q: %w", opts.Workflow, err)
	}

	cfg := taboo.Config{
		Workshop:         taboo.WorkshopName(pc.Workshop, wf.Profile.Name()),
		Base:             pc.Base,
		Agent:            wf.Profile,
		RepoPath:         opts.RepoPath,
		ProjectDir:       opts.ProjectDir,
		SourceDefinition: pc.SourceDefinition,
	}

	maxIterations := taboo.ResolveMaxIterations(wf, pc.Defaults)
	timeout := taboo.ResolveTimeout(wf, pc.Defaults)
	completionSignal := taboo.ResolveCompletionSignal(pc.Defaults)

	req := taboo.OrchestratedRequest{
		RunRequest: taboo.RunRequest{
			Branch:  opts.Branch,
			Prompt:  prompt,
			Timeout: timeout,
			Stdout:  opts.Stdout,
			Stderr:  opts.Stderr,
		},
		MaxIterations:    maxIterations,
		CompletionSignal: completionSignal,
	}

	return plan{cfg: cfg, req: req}, nil
}

// Run loads the config, resolves the named workflow into a plan, and drives it
// through taboo.Orchestrator, mapping the orchestrated result onto Result. The
// orchestrator shells out to provision a workshop, so Run is integration-level
// glue; the resolution logic lives in buildPlan, which is unit-tested.
func Run(ctx context.Context, opts Options) (Result, error) {
	pc, err := taboo.LoadConfig(opts.ConfigPath)
	if err != nil {
		return Result{}, err
	}

	p, err := buildPlan(pc, opts)
	if err != nil {
		return Result{}, err
	}

	cmd := opts.Commander
	if cmd == nil {
		cmd = taboo.NewExecCommander()
	}

	orch := taboo.NewOrchestrator(taboo.New(p.cfg, cmd))
	res, err := orch.Run(ctx, p.req)
	if err != nil {
		return Result{}, err
	}

	return Result{
		Branch:       res.Branch,
		WorktreePath: res.WorktreePath,
		Commit:       res.Commit,
		Output:       res.Output,
	}, nil
}
