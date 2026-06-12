package taboo

import (
	"context"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// sdkFS holds the agent SDK(s) taboo ships. Each is seeded into a managed
// project's .workshop/ directory so the rendered definition can reference it
// as an in-project SDK (e.g. "project-opencode").
//
//go:embed sdk
var sdkFS embed.FS

// RunRequest describes a single agent run.
type RunRequest struct {
	// Branch is the new branch created for this run's worktree.
	Branch string
	// Prompt is the agent's instruction, delivered via Config.Agent's command
	// (in argv or on stdin, per the agent).
	Prompt string
	// Timeout bounds the agent exec (zero = no timeout).
	Timeout time.Duration
	// Stdout and Stderr receive the agent exec's live output (nil = discard).
	Stdout io.Writer
	Stderr io.Writer
	// Hooks are lifecycle commands run at defined points during the run.
	Hooks Hooks
}

// RunResult reports the outcome of a run.
type RunResult struct {
	Branch       string
	WorktreePath string
	Commit       string // HEAD of the branch after the agent ran (set in slice 6)
	Output       string // captured agent exec stdout (stderr is not retained)
}

// Runner orchestrates agent runs in a taboo-managed workshop.
type Runner struct {
	cfg Config
	cmd Commander
}

// New returns a Runner bound to cfg, driving workshop/git through cmd.
func New(cfg Config, cmd Commander) *Runner {
	return &Runner{cfg: cfg, cmd: cmd}
}

// definitionPath is where taboo writes the rendered workshop definition. This
// matches `workshop init`'s convention: <project>/.workshop/<name>.yaml.
func (r *Runner) definitionPath() string {
	return filepath.Join(r.cfg.ProjectDir, ".workshop", r.cfg.Workshop+".yaml")
}

// writeDefinition renders the workshop definition and writes it into the
// project's .workshop directory.
func (r *Runner) writeDefinition() error {
	out, err := renderDefinition(r.cfg)
	if err != nil {
		return err
	}
	path := r.definitionPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(out), 0o600)
}

// seedSDK writes taboo's embedded agent SDK(s) into the project's .workshop
// directory (e.g. .workshop/opencode/sdk.yaml + hooks/...), so the rendered
// definition's "project-<sdk>" reference resolves.
func (r *Runner) seedSDK() error {
	const root = "sdk"
	return fs.WalkDir(sdkFS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, root), "/")
		if rel == "" {
			return nil
		}
		dst := filepath.Join(r.cfg.ProjectDir, ".workshop", rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o750)
		}
		data, err := sdkFS.ReadFile(p)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if strings.Contains(rel, "/hooks/") {
			mode = 0o755 // hook scripts must be executable
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			return err
		}
		return os.WriteFile(dst, data, mode)
	})
}

func (r *Runner) workshop(ctx context.Context, args []string) error {
	return r.cmd.Run(ctx, Cmd{Name: "workshop", Args: args})
}

func (r *Runner) git(ctx context.Context, args []string) error {
	return r.cmd.Run(ctx, Cmd{Name: "git", Args: args})
}

// worktreePath is the host path for a run's worktree, derived from the branch.
func (r *Runner) worktreePath(branch string) string {
	safe := strings.ReplaceAll(branch, "/", "-")
	return filepath.Join(r.cfg.ProjectDir, "worktrees", safe)
}

// Run executes one agent run end-to-end: Setup the worktree, then Exec the
// agent once in it. It is the single-run primitive. The Orchestrator splits
// these steps to Setup once and Exec repeatedly into the same worktree.
func (r *Runner) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	res, err := r.Setup(ctx, req)
	if err != nil {
		return res, err
	}
	return r.Exec(ctx, req, res)
}

// Setup ensures the workshop exists, creates a fresh worktree on req.Branch, and
// swaps it (and the repo's .git) into the workshop via stop/remount/start. It
// runs once per worktree; the returned RunResult carries Branch + WorktreePath
// for the subsequent Exec call(s).
func (r *Runner) Setup(ctx context.Context, req RunRequest) (RunResult, error) {
	res := RunResult{Branch: req.Branch}

	if err := r.ensureWorkshop(ctx); err != nil {
		return res, fmt.Errorf("ensure workshop: %w", err)
	}

	wt := r.worktreePath(req.Branch)
	res.WorktreePath = wt
	if err := r.git(ctx, worktreeAddArgs(r.cfg.RepoPath, req.Branch, wt)); err != nil {
		return res, fmt.Errorf("add worktree: %w", err)
	}

	// Swap the worktree + the repo's .git into the workshop. A worktree is a
	// non-empty source, so remount is not atomic: stop -> remount -> start.
	proj, ws, sdk := r.cfg.ProjectDir, r.cfg.Workshop, r.cfg.Agent.Name()
	if err := r.workshop(ctx, stopArgs(proj, ws)); err != nil {
		return res, fmt.Errorf("stop: %w", err)
	}
	if err := r.workshop(ctx, remountArgs(proj, ws, sdk, "workspace", wt)); err != nil {
		return res, fmt.Errorf("remount workspace: %w", err)
	}
	if err := r.workshop(ctx, remountArgs(proj, ws, sdk, "gitcommon", gitCommonTarget(r.cfg.RepoPath))); err != nil {
		return res, fmt.Errorf("remount gitcommon: %w", err)
	}
	if err := r.workshop(ctx, startArgs(proj, ws)); err != nil {
		return res, fmt.Errorf("start: %w", err)
	}

	// The workshop is ready with the worktree mounted: run caller-supplied
	// setup hooks before handing control to the agent. This is the end of
	// Setup, so hooks run once per worktree, before any Exec.
	if err := r.runHooks(ctx, wt, req.Timeout, req.Stderr, req.Hooks.OnWorkshopReady); err != nil {
		return res, fmt.Errorf("on-workshop-ready hook: %w", err)
	}

	return res, nil
}

// Exec runs the agent once in the worktree Setup prepared (res.WorktreePath),
// then records the agent's stdout and the resulting branch HEAD on the returned
// result. Calling it more than once re-runs the agent in place; because the
// agent commits through the bind-mount, each Exec continues from the prior
// iteration's commit. The base argument supplies Branch + WorktreePath from Setup.
func (r *Runner) Exec(ctx context.Context, req RunRequest, base RunResult) (RunResult, error) {
	res := base
	proj, ws := r.cfg.ProjectDir, r.cfg.Workshop

	// Tee the agent's stdout into a runner-owned buffer so it is retained on
	// RunResult, while still forwarding live to the caller's writer if any.
	var captured strings.Builder
	stdout := io.Writer(&captured)
	if req.Stdout != nil {
		stdout = io.MultiWriter(&captured, req.Stdout)
	}

	ac := r.cfg.Agent.BuildCommand(CommandOptions{Prompt: req.Prompt})
	opts := execOptions{cwd: workspaceTarget, timeout: req.Timeout, envKeys: r.cfg.Agent.CredentialEnvKeys()}
	execCmd := Cmd{
		Name:   "workshop",
		Args:   execArgs(proj, ws, opts, ac.Argv),
		Stdout: stdout,
		Stderr: req.Stderr,
	}
	// Stdin-delivery agents (Claude/Codex/Pi) carry the prompt here instead of
	// in argv; OpenCode leaves it empty. See ADR 0001.
	if ac.Stdin != "" {
		execCmd.Stdin = strings.NewReader(ac.Stdin)
	}
	if err := r.cmd.Run(ctx, execCmd); err != nil {
		return res, fmt.Errorf("exec agent: %w", err)
	}
	res.Output = captured.String()

	// The agent committed in place through the bind-mount; capture the branch
	// HEAD from the host worktree.
	commit, err := r.gitCapture(ctx, revParseHeadArgs(res.WorktreePath))
	if err != nil {
		return res, fmt.Errorf("rev-parse HEAD: %w", err)
	}
	res.Commit = commit

	return res, nil
}

// gitCapture runs a git command and returns its trimmed stdout.
func (r *Runner) gitCapture(ctx context.Context, args []string) (string, error) {
	var buf strings.Builder
	err := r.cmd.Run(ctx, Cmd{Name: "git", Args: args, Stdout: &buf})
	return strings.TrimSpace(buf.String()), err
}

// ensureWorkshop launches the workshop if it does not already exist, otherwise
// reuses it. Existence is probed with `workshop info`: a non-error means the
// workshop is present and is reused (the launch is expensive (minutes), so it
// is amortized across runs).
func (r *Runner) ensureWorkshop(ctx context.Context) error {
	if err := r.workshop(ctx, infoArgs(r.cfg.ProjectDir, r.cfg.Workshop)); err == nil {
		return nil // present — reuse
	}
	if err := r.seedSDK(); err != nil {
		return fmt.Errorf("seed agent SDK: %w", err)
	}
	if err := r.writeDefinition(); err != nil {
		return fmt.Errorf("write definition: %w", err)
	}
	return r.workshop(ctx, launchArgs(r.cfg.ProjectDir, r.cfg.Workshop))
}
