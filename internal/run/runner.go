package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/josecabralf/taboo/internal/agent"
	"github.com/josecabralf/taboo/internal/exec"
	"github.com/josecabralf/taboo/internal/workshop"
)

// RunRequest describes a single agent run.
type RunRequest struct {
	// Branch is the new branch created for this run's worktree.
	Branch string
	// BaseRef, when set, makes Setup fetch origin and start the run's worktree
	// branch from this ref (e.g. "origin/feature-x") instead of the host repo's
	// HEAD. The fetch updates the ref (and origin/main, which the agent may merge)
	// before the worktree is added. Empty = the default: a fresh branch off HEAD,
	// no fetch.
	BaseRef string
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

// runResultHandle is a RunResult's private capability to read and tear down a
// run without callers needing to know the worktree's on-disk layout. Exec's
// rev-parse capture and Artifact read worktreePath through the handle; dispose
// also uses repoPath and cmd to shell out to `git -C <repoPath> worktree remove`.
type runResultHandle struct {
	repoPath     string
	worktreePath string
	cmd          exec.Commander
}

// RunResult reports the outcome of a run.
type RunResult struct {
	Branch string
	Commit string // HEAD of the branch after the agent ran
	Output string // captured agent exec stdout (stderr is not retained)
	// Err is this run's failure, populated by Pool when fanning out so that one
	// failed run does not abort the whole batch (see Pool.Run). The single-run
	// primitives (Runner.Run/Setup/Exec) return their error separately and leave
	// Err nil.
	Err error
	// handle is the run's private capability to read its artifacts; nil until
	// Setup populates it.
	handle *runResultHandle
}

// NewResultWithWorktree returns a RunResult whose Artifact reads files from an
// existing worktree directory. Runner.Setup is the normal source of a result's
// worktree handle; this lets a caller that already has a worktree on disk (or a
// consumer test) attach one to a hand-built result so the Artifact API works
// without a full run.
func NewResultWithWorktree(worktree string) RunResult {
	return RunResult{handle: &runResultHandle{worktreePath: worktree}}
}

// NewResultWithWorktreeCmd is NewResultWithWorktree plus a Commander, so a
// consumer test can exercise Dispose (which shells out to git) against a
// hand-built result without a full run. repoPath is left empty; Dispose passes
// it to `git -C`, where the test's fake Commander records the call instead of
// running it.
func NewResultWithWorktreeCmd(worktree string, cmd exec.Commander) RunResult {
	return RunResult{handle: &runResultHandle{worktreePath: worktree, cmd: cmd}}
}

// Artifact reads the file at relpath within the run's worktree and returns its
// contents.
func (r RunResult) Artifact(relpath string) (string, error) {
	if r.handle == nil {
		return "", errors.New("artifact: result has no worktree handle")
	}
	// Artifact is public API. Today's only caller passes a constant, but a future
	// caller could pass untrusted input, so confine reads to the worktree: reject
	// absolute paths and any ".." escape. (Lexical only — a symlink inside the
	// worktree could still point out; tighten to os.Root if that becomes a risk.)
	if !filepath.IsLocal(relpath) {
		return "", fmt.Errorf("artifact %q: path escapes worktree", relpath)
	}
	b, err := os.ReadFile(filepath.Join(r.handle.worktreePath, relpath)) // #nosec G304
	if err != nil {
		return "", fmt.Errorf("read artifact %q: %w", relpath, err)
	}
	return string(b), nil
}

// Dispose removes the run's worktree with a non-force `git worktree remove`,
// matching taboo clean's teardown. It is explicit, never automatic. A worktree
// already gone is success, not an error. The branch ref and the workshop are
// left intact (persisting is the default) so a later push or run can reuse them.
// It returns an error, rather than panicking, when the result has no worktree
// handle.
func (r RunResult) Dispose() error {
	if r.handle == nil {
		return errors.New("dispose: result has no worktree handle")
	}
	return r.handle.dispose(context.Background())
}

// dispose performs the worktree removal for Dispose. Idempotency lives here: a
// worktree already gone (a prior Dispose, or a manual `git worktree remove`)
// short-circuits to success before shelling out, so git's "not a working tree"
// failure never surfaces.
func (h *runResultHandle) dispose(ctx context.Context) error {
	if _, err := os.Stat(h.worktreePath); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if h.cmd == nil {
		return errors.New("dispose: result handle has no commander")
	}
	return h.cmd.Run(ctx, exec.Cmd{
		Name: "git",
		Args: []string{"-C", h.repoPath, "worktree", "remove", h.worktreePath},
	})
}

// Runner orchestrates agent runs in a taboo-managed workshop.
type Runner struct {
	cfg workshop.Config
	cmd exec.Commander
}

// New returns a Runner bound to cfg, driving workshop/git through cmd.
func New(cfg workshop.Config, cmd exec.Commander) *Runner {
	return &Runner{cfg: cfg, cmd: cmd}
}

// fingerprintPath is where taboo records the digest of the derived def the live
// workshop was last provisioned (launched/refreshed) with. It sits beside the
// derived definition under ProjectDir.
func (r *Runner) fingerprintPath() string {
	return filepath.Join(r.cfg.ProjectDir, "workshop.fingerprint")
}

// readFingerprint returns the persisted provisioning fingerprint, or "" if none
// is recorded — "" never matches a real digest, so an absent record forces a
// reconcile, which is the safe default. A missing sidecar is the expected absent
// case (fs.ErrNotExist -> "", nil); any OTHER read error is surfaced rather than
// silently masquerading as absent and forcing a spurious refresh.
func (r *Runner) readFingerprint() (string, error) {
	b, err := os.ReadFile(r.fingerprintPath())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// writeFingerprint records fingerprint as what the live workshop was just
// provisioned with, so the next run can take the reuse fast path.
func (r *Runner) writeFingerprint(fingerprint string) error {
	// path is derived from config, not user input
	return os.WriteFile(r.fingerprintPath(), []byte(fingerprint), 0o600) //nolint:gosec
}

// materialize regenerates the .taboo artifacts a workshop launch depends on
// (the seeded agent SDK, the derived definition, the project-SDK symlinks) and
// returns the provisioning fingerprint. The pure provisioning logic lives in
// internal/workshop; this is the thin Runner-side call into it.
func (r *Runner) materialize() (fingerprint string, err error) {
	return workshop.Materialize(r.cfg)
}

func (r *Runner) workshop(ctx context.Context, args []string) error {
	return r.cmd.Run(ctx, exec.Cmd{Name: "workshop", Args: args})
}

func (r *Runner) git(ctx context.Context, args []string) error {
	return r.cmd.Run(ctx, exec.Cmd{Name: "git", Args: args})
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
func (r *Runner) sessionEnv() []workshop.EnvAssignment {
	if spec, ok := r.cfg.Agent.Sessions(); ok {
		return []workshop.EnvAssignment{{Name: spec.DirEnv, Value: workshop.SessionsTarget}}
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
// runs once per worktree; the returned RunResult carries Branch + a worktree
// handle for the subsequent Exec call(s).
func (r *Runner) Setup(ctx context.Context, req RunRequest) (RunResult, error) {
	res := RunResult{Branch: req.Branch}

	fingerprint, err := r.materialize()
	if err != nil {
		return res, fmt.Errorf("materialize: %w", err)
	}
	if err := r.ensureWorkshop(ctx, fingerprint); err != nil {
		return res, fmt.Errorf("ensure workshop: %w", err)
	}

	wt := r.worktreePath(req.Branch)
	res.handle = &runResultHandle{repoPath: r.cfg.RepoPath, worktreePath: wt, cmd: r.cmd}
	if req.BaseRef != "" {
		// Update remote-tracking refs so BaseRef (and origin/main, which the agent
		// may merge offline) are current, then start the worktree branch FROM
		// BaseRef's tip rather than the host repo's HEAD.
		if err := r.git(ctx, []string{"-C", r.cfg.RepoPath, "fetch", "origin"}); err != nil {
			return res, fmt.Errorf("fetch origin: %w", err)
		}
		if err := r.git(ctx, []string{"-C", r.cfg.RepoPath, "worktree", "add", "-b", req.Branch, wt, req.BaseRef}); err != nil {
			return res, fmt.Errorf("add worktree from %s: %w", req.BaseRef, err)
		}
	} else {
		// A fresh linked worktree on req.Branch, off the repo's current HEAD.
		if err := r.git(ctx, []string{"-C", r.cfg.RepoPath, "worktree", "add", "-b", req.Branch, wt}); err != nil {
			return res, fmt.Errorf("add worktree: %w", err)
		}
	}

	// Swap the worktree + the repo's .git into the workshop. A worktree is a
	// non-empty source, so remount is not atomic: stop -> remount -> start.
	proj, ws, sdk := r.cfg.ProjectDir, r.cfg.Workshop, string(r.cfg.Agent.Name())
	if err := r.workshop(ctx, workshop.VerbArgs(proj, "stop", ws)); err != nil {
		return res, fmt.Errorf("stop: %w", err)
	}
	if err := r.workshop(ctx, workshop.RemountArgs(proj, ws, sdk, "workspace", wt)); err != nil {
		return res, fmt.Errorf("remount workspace: %w", err)
	}
	if err := r.workshop(ctx, workshop.RemountArgs(proj, ws, sdk, "gitcommon", workshop.GitCommonTarget(r.cfg.RepoPath))); err != nil {
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
		if err := r.workshop(ctx, workshop.RemountArgs(proj, ws, sdk, "sessions", host)); err != nil {
			return res, fmt.Errorf("remount sessions: %w", err)
		}
	}
	if err := r.workshop(ctx, workshop.VerbArgs(proj, "start", ws)); err != nil {
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

// Exec runs the agent once in the worktree Setup prepared (read from the result's
// handle), then records the agent's stdout and the resulting branch HEAD on the
// returned result. Calling it more than once re-runs the agent in place; because the
// agent commits through the bind-mount, each Exec continues from the prior
// iteration's commit. The base argument supplies Branch + the worktree handle from
// Setup.
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

	ac := r.cfg.Agent.BuildCommand(agent.CommandOptions{
		Prompt:        req.Prompt,
		ResumeSession: req.ResumeSession,
		Fork:          req.Fork,
	})
	opts := workshop.ExecOptions{Cwd: workshop.WorkspaceTarget, Timeout: req.Timeout, EnvKeys: r.cfg.Agent.CredentialEnvKeys()}
	// Point the agent's session-dir env var at the sessions mount target so its
	// session files land in the bound host directory (the same dir Setup mounted
	// and that survives the swap).
	opts.Env = r.sessionEnv()
	execCmd := exec.Cmd{
		Name:   "workshop",
		Args:   workshop.ExecArgs(proj, ws, opts, ac.Argv),
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
	// HEAD from the host worktree. The path comes from the handle, which Setup
	// always populates; guard hand-built results that lack one.
	if res.handle == nil {
		return res, fmt.Errorf("exec: result has no worktree handle")
	}
	commit, err := r.gitCapture(ctx, []string{"-C", res.handle.worktreePath, "rev-parse", "HEAD"})
	if err != nil {
		return res, fmt.Errorf("rev-parse HEAD: %w", err)
	}
	res.Commit = commit

	return res, nil
}

// gitCapture runs a git command and returns its trimmed stdout.
func (r *Runner) gitCapture(ctx context.Context, args []string) (string, error) {
	out, err := exec.Output(ctx, r.cmd, exec.Cmd{Name: "git", Args: args})
	return strings.TrimSpace(out), err
}

// ensureWorkshop reconciles the long-lived workshop with the just-derived
// definition, identified by fingerprint (the digest of the def materialize
// wrote this run). Absent: launch fresh and record the fingerprint. Present and
// unchanged: reuse as-is — the amortization fast path (the expensive launch is
// minutes; this is the common case). Present but changed (the project's
// workshop.yaml drifted, e.g. an added SDK): refresh the workshop to the new
// def, then record the new fingerprint.
func (r *Runner) ensureWorkshop(ctx context.Context, fingerprint string) error {
	proj, ws := r.cfg.ProjectDir, r.cfg.Workshop
	if err := r.workshop(ctx, workshop.VerbArgs(proj, "info", ws)); err != nil {
		if err := r.workshop(ctx, workshop.VerbArgs(proj, "launch", ws)); err != nil {
			return err
		}
		return r.writeFingerprint(fingerprint)
	}
	recorded, err := r.readFingerprint()
	if err != nil {
		return err
	}
	if recorded == fingerprint {
		return nil // unchanged — reuse the existing workshop as-is
	}
	// `workshop refresh` reconciles the live workshop to the new
	// definition (base image, SDKs, and plugs), which covers the #70 drift case
	// (e.g. an added SDK). A remove+launch fallback is only worth adding if a real
	// refresh-failure case appears.
	if err := r.workshop(ctx, workshop.VerbArgs(proj, "refresh", ws)); err != nil {
		return err
	}
	return r.writeFingerprint(fingerprint)
}
