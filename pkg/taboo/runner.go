package taboo

import (
	"context"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// sdkFS holds every agent SDK taboo ships. A run seeds only the configured
// agent's tree into the managed project's .workshop/ directory so the rendered
// definition can reference it as an in-project SDK (e.g. "project-opencode").
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
	// ResumeSession, if set, continues a prior agent session by its id instead of
	// starting fresh: the id is passed to Config.Agent's command builder, which
	// renders the agent's resume flag (e.g. OpenCode's --session). The session
	// store is bind-mounted independently of the worktree and is stable across a
	// workshop's runs (see sessionsDir), so a prior id resolves regardless of
	// which worktree this run uses. Empty = a fresh session.
	ResumeSession string
	// Fork, when set together with ResumeSession, forks that session into a new
	// one (the agent's fork flag, e.g. OpenCode's --fork) so the source
	// conversation is not mutated. Paired with a fresh Branch — Setup always
	// allocates a new worktree per branch — this isolates a divergent continuation
	// at both the session and filesystem levels. Fork without ResumeSession is
	// meaningless and ignored. Agents with no native fork degrade to worktree-only
	// isolation (see docs/adr/0003-session-resume-fork-command-contract.md).
	Fork bool
}

// RunResult reports the outcome of a run.
type RunResult struct {
	Branch       string
	WorktreePath string
	Commit       string // HEAD of the branch after the agent ran
	Output       string // captured agent exec stdout (stderr is not retained)
	// Err is this run's failure, populated by Pool when fanning out so that one
	// failed run does not abort the whole batch (see Pool.Run). The single-run
	// primitives (Runner.Run/Setup/Exec) return their error separately and leave
	// Err nil.
	Err error
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

// definitionPath is where taboo writes the derived workshop definition. Workshop
// resolves a launch from the project dir's root workshop.yaml, and taboo launches
// with --project <ProjectDir>, so the derived definition lives at
// <ProjectDir>/workshop.yaml.
func (r *Runner) definitionPath() string {
	return filepath.Join(r.cfg.ProjectDir, "workshop.yaml")
}

// sourceDefinitionPath is the project's own workshop definition that taboo
// derives the agent's workshop from. It lives at the repo root (RepoPath), the
// file the project's human developers already use — never under ProjectDir.
func (r *Runner) sourceDefinitionPath() string {
	return filepath.Join(r.cfg.RepoPath, "workshop.yaml")
}

// writeDefinition derives the agent's workshop definition from source (the
// project's own workshop.yaml bytes) and writes it to definitionPath.
func (r *Runner) writeDefinition(source []byte) error {
	out, err := deriveDefinition(r.cfg, source)
	if err != nil {
		return err
	}
	path := filepath.Clean(r.definitionPath())
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}

	// path is from config, cleaned by filepath.Join and filepath.Clean
	return os.WriteFile(path, []byte(out), 0o600) //nolint:gosec
}

// reconcileProjectSDKs makes <projectDir>/.workshop/<name> a symlink to
// <repoPath>/.workshop/<name> for each wanted name, and prunes stale symlinks
// (links whose name is no longer wanted). It keys pruning on the SYMLINK BIT, never
// membership, so it never deletes the seeded agent SDK (a real dir) or any other
// real file; it uses os.Remove (never os.RemoveAll) and os.Lstat (never follows
// the link), so it can never recurse into the project's real .workshop/<name>.
func reconcileProjectSDKs(projectDir, repoPath string, names []string) error {
	dir := filepath.Join(projectDir, ".workshop")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[name] = true
		if err := ensureSymlink(dir, repoPath, name); err != nil {
			return err
		}
	}
	return pruneStaleSymlinks(dir, wanted)
}

// ensureSymlink creates or updates a symlink for the given SDK name.
func ensureSymlink(dir, repoPath, name string) error {
	link := filepath.Join(dir, name)
	target := filepath.Join(repoPath, ".workshop", name)
	if fi, err := os.Lstat(link); err == nil {
		if fi.Mode()&os.ModeSymlink == 0 {
			return nil // a real entry already occupies this name; never clobber it
		}
		cur, err := os.Readlink(link)
		if err != nil {
			return err
		}
		if cur == target {
			return nil // already correct
		}
		if err := os.Remove(link); err != nil { // link-safe: removes the link only
			return err
		}
	}
	return os.Symlink(target, link)
}

// pruneStaleSymlinks removes symlinks that are no longer wanted.
func pruneStaleSymlinks(dir string, wanted map[string]bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if wanted[e.Name()] {
			continue
		}
		p := filepath.Join(dir, e.Name())
		fi, err := os.Lstat(p)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			continue // not a symlink (e.g. the seeded agent SDK dir) — leave it
		}
		if err := os.Remove(p); err != nil { // removes the stale link, never its target
			return err
		}
	}
	return nil
}

// materialize regenerates the .taboo artifacts a workshop launch depends on:
// the seeded agent SDK, the derived definition, and the project-SDK symlinks. It
// runs at the start of every Setup, before ensureWorkshop, so the artifacts exist
// before any launch and self-heal each run (single source of truth; see ADR 0009).
// The source is read ONCE here. The definition is written BEFORE reconcile so a
// malformed source fails without touching symlinks.
func (r *Runner) materialize() error {
	source, err := os.ReadFile(r.sourceDefinitionPath())
	if err != nil {
		return fmt.Errorf("read project definition %s: %w", r.sourceDefinitionPath(), err)
	}
	if err := r.seedSDK(); err != nil {
		return fmt.Errorf("seed agent SDK: %w", err)
	}
	if err := r.writeDefinition(source); err != nil {
		return fmt.Errorf("write definition: %w", err)
	}
	if err := reconcileProjectSDKs(r.cfg.ProjectDir, r.cfg.RepoPath, projectSDKNames(source)); err != nil {
		return fmt.Errorf("reconcile project SDKs: %w", err)
	}
	return nil
}

// seedSDK writes the configured agent's embedded SDK into the project's
// .workshop directory (e.g. .workshop/opencode/sdk.yaml + hooks/...), so the
// rendered definition's "project-<agent>" reference resolves.
func (r *Runner) seedSDK() error {
	const sdkRoot = "sdk"
	// Walk only the configured agent's subtree, stripping just the leading
	// "sdk/" so the agent-name segment survives. The destination layout stays
	// .workshop/<agent>/..., which is what "project-<agent>" resolves against.
	root := path.Join(sdkRoot, r.cfg.Agent.Name())
	return fs.WalkDir(sdkFS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Every walked entry is rooted at sdk/<agent>, so trimming the literal
		// "sdk/" keeps the <agent>/... layout; the root entry itself becomes
		// "<agent>", a real dir that MkdirAll handles.
		rel := strings.TrimPrefix(p, sdkRoot+"/")
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

// sessionsDir is the host directory taboo binds into the workshop for a
// session-capable agent's session files. It is stable across a workshop's runs
// (not per-branch) so a session can be resumed regardless of which worktree a
// later run uses.
//
// Because it is shared by every run in a ProjectDir, it is safe only for
// sequential runs against one workshop: concurrent runs sharing a ProjectDir
// would share OpenCode's single SQLite session DB and could corrupt it. Pool
// keeps this invariant by giving each concurrency slot its own ProjectDir (see
// Pool.slotConfig), so parallel fan-out runs never share a session store.
func (r *Runner) sessionsDir() string {
	return filepath.Join(r.cfg.ProjectDir, "sessions")
}

// sessionEnv returns the explicit `--env NAME=VALUE` assignment that redirects a
// session-capable agent's session-dir env var at the sessions mount target, or
// nil for a sessionless agent. Both the agent exec and any in-workshop setup
// hook apply it: hooks run after start with no swap before the exec, so a hook
// that prepares session state must resolve the store to the same bound path the
// agent later reads, or their views of it would silently diverge.
func (r *Runner) sessionEnv() []envAssignment {
	if spec, ok := r.cfg.Agent.Sessions(); ok {
		return []envAssignment{{Name: spec.DirEnv, Value: sessionsTarget}}
	}
	return nil
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

	if err := r.materialize(); err != nil {
		return res, fmt.Errorf("materialize: %w", err)
	}
	if err := r.ensureWorkshop(ctx); err != nil {
		return res, fmt.Errorf("ensure workshop: %w", err)
	}

	wt := r.worktreePath(req.Branch)
	res.WorktreePath = wt
	// A fresh linked worktree on req.Branch.
	if err := r.git(ctx, []string{"-C", r.cfg.RepoPath, "worktree", "add", "-b", req.Branch, wt}); err != nil {
		return res, fmt.Errorf("add worktree: %w", err)
	}

	// Swap the worktree + the repo's .git into the workshop. A worktree is a
	// non-empty source, so remount is not atomic: stop -> remount -> start.
	proj, ws, sdk := r.cfg.ProjectDir, r.cfg.Workshop, r.cfg.Agent.Name()
	if err := r.workshop(ctx, verbArgs(proj, "stop", ws)); err != nil {
		return res, fmt.Errorf("stop: %w", err)
	}
	if err := r.workshop(ctx, remountArgs(proj, ws, sdk, "workspace", wt)); err != nil {
		return res, fmt.Errorf("remount workspace: %w", err)
	}
	if err := r.workshop(ctx, remountArgs(proj, ws, sdk, "gitcommon", gitCommonTarget(r.cfg.RepoPath))); err != nil {
		return res, fmt.Errorf("remount gitcommon: %w", err)
	}
	// A session-capable agent gets a host sessions dir bound in alongside the
	// worktree, so its session files write through to the host and survive this
	// stop/remount/start swap (which wipes the rootfs).
	if _, ok := r.cfg.Agent.Sessions(); ok {
		host := r.sessionsDir()
		if err := os.MkdirAll(host, 0o750); err != nil {
			return res, fmt.Errorf("create sessions dir: %w", err)
		}
		if err := r.workshop(ctx, remountArgs(proj, ws, sdk, "sessions", host)); err != nil {
			return res, fmt.Errorf("remount sessions: %w", err)
		}
	}
	if err := r.workshop(ctx, verbArgs(proj, "start", ws)); err != nil {
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

	ac := r.cfg.Agent.BuildCommand(CommandOptions{
		Prompt:        req.Prompt,
		ResumeSession: req.ResumeSession,
		Fork:          req.Fork,
	})
	opts := execOptions{cwd: workspaceTarget, timeout: req.Timeout, envKeys: r.cfg.Agent.CredentialEnvKeys()}
	// Point the agent's session-dir env var at the sessions mount target so its
	// session files land in the bound host directory (the same dir Setup mounted
	// and that survives the swap).
	opts.env = r.sessionEnv()
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
	commit, err := r.gitCapture(ctx, []string{"-C", res.WorktreePath, "rev-parse", "HEAD"})
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
	if err := r.workshop(ctx, verbArgs(r.cfg.ProjectDir, "info", r.cfg.Workshop)); err == nil {
		return nil // present — reuse
	}
	return r.workshop(ctx, verbArgs(r.cfg.ProjectDir, "launch", r.cfg.Workshop))
}
