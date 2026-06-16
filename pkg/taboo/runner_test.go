package taboo

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeCommander records every invocation and can be programmed to fail
// specific commands via errFn. It is safe for concurrent use (the Pool fans runs
// out across goroutines), so mu guards calls and worktrees and every accessor
// takes it.
type fakeCommander struct {
	mu        sync.Mutex
	calls     []Cmd
	errFn     func(c Cmd) error
	stdoutFn  func(c Cmd) string  // programmed stdout for a matched call
	worktrees map[string]struct{} // branches already added, to model git's statefulness

	// Optional concurrency gate for Pool tests. A call whose verb == gateVerb
	// updates the inflight/peak meter, signals entered, then blocks until it
	// receives a token from gate — all WITHOUT holding mu, so concurrently gated
	// calls genuinely overlap and peak reflects true simultaneity. Leaving gate
	// nil disables it (the default for non-concurrency tests).
	gateVerb string
	gate     chan struct{}
	entered  chan struct{}
	inflight atomic.Int32
	peak     atomic.Int32
}

func (f *fakeCommander) Run(_ context.Context, c Cmd) error {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	// Model the one piece of git statefulness the orchestrator depends on: a
	// second `worktree add -b <branch>` for a branch already added fails, as
	// real git does. Without this the fake would silently accept a re-add and
	// hide a loop that re-creates the worktree every iteration.
	if branch, ok := worktreeAddBranch(c); ok {
		if _, exists := f.worktrees[branch]; exists {
			f.mu.Unlock()
			return fmt.Errorf("fatal: a branch named %q already exists", branch)
		}
		if f.worktrees == nil {
			f.worktrees = map[string]struct{}{}
		}
		f.worktrees[branch] = struct{}{}
	}
	stdoutFn, errFn := f.stdoutFn, f.errFn
	gateVerb, gate, entered := f.gateVerb, f.gate, f.entered
	f.mu.Unlock()

	// Gate matching calls without holding mu so they truly overlap. Count this
	// call as in flight, push the peak up to the new high-water mark, announce
	// arrival, then park until the test releases one token.
	if gate != nil && verbOf(c) == gateVerb {
		n := f.inflight.Add(1)
		for {
			p := f.peak.Load()
			if n <= p || f.peak.CompareAndSwap(p, n) {
				break
			}
		}
		if entered != nil {
			entered <- struct{}{}
		}
		<-gate
		f.inflight.Add(-1)
	}

	if stdoutFn != nil && c.Stdout != nil {
		if s := stdoutFn(c); s != "" {
			_, _ = io.WriteString(c.Stdout, s)
		}
	}
	if errFn != nil {
		return errFn(c)
	}
	return nil
}

// snapshot returns a copy of the recorded calls, taken under the lock, so tests
// can inspect the sequence after concurrent runs without racing the recorder.
func (f *fakeCommander) snapshot() []Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// verbs returns the workshop/git subcommand verb of each recorded call, in
// order, for sequence assertions. For workshop calls the verb is the token
// after "--project <dir>"; for git it is "worktree".
func (f *fakeCommander) verbs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var vs []string
	for _, c := range f.calls {
		vs = append(vs, verbOf(c))
	}
	return vs
}

func verbOf(c Cmd) string {
	if c.Name == "git" {
		if len(c.Args) >= 3 {
			return c.Args[2] // -C <repo> <verb>
		}
		return c.Name
	}
	// workshop --project <dir> <verb> ...
	for i, a := range c.Args {
		if a == "--project" {
			if i+2 < len(c.Args) {
				return c.Args[i+2]
			}
		}
	}
	if len(c.Args) > 0 {
		return c.Args[0]
	}
	return c.Name
}

// worktreeAddBranch reports the branch of a `git -C <repo> worktree add -b
// <branch> <path>` invocation, matching the worktree-add Runner.Setup issues.
// The ok result is false for any other call.
func worktreeAddBranch(c Cmd) (string, bool) {
	if c.Name != "git" {
		return "", false
	}
	for i := 0; i+1 < len(c.Args); i++ {
		if c.Args[i] == "-b" {
			return c.Args[i+1], true
		}
	}
	return "", false
}

func failOnVerb(verb string) func(Cmd) error {
	return func(c Cmd) error {
		if verbOf(c) == verb {
			return fmt.Errorf("simulated failure for %q", verb)
		}
		return nil
	}
}

func testConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		Workshop:   "taboo-run",
		Base:       "ubuntu@24.04",
		Agent:      OpenCode(openCodeModel),
		RepoPath:   "/home/dev/repos/myproject",
		ProjectDir: t.TempDir(),
	}
}

func TestEnsureWorkshop_AbsentLaunches(t *testing.T) {
	// `info` fails -> workshop is absent -> taboo writes the definition and launches.
	fc := &fakeCommander{errFn: failOnVerb("info")}
	cfg := testConfig(t)
	r := New(cfg, fc)

	if err := r.ensureWorkshop(context.Background()); err != nil {
		t.Fatalf("ensureWorkshop: %v", err)
	}

	if got := fc.verbs(); !slices.Equal(got, []string{"info", "launch"}) {
		t.Errorf("calls = %v, want [info launch]", got)
	}

	// The rendered definition must have been written into the project's
	// .workshop dir, matching `workshop init`'s convention.
	defPath := filepath.Join(cfg.ProjectDir, ".workshop", cfg.Workshop+".yaml")
	data, err := os.ReadFile(defPath)
	if err != nil {
		t.Fatalf("definition not written: %v", err)
	}
	if len(data) == 0 {
		t.Error("definition file is empty")
	}

	// The agent SDK must be seeded so the "project-opencode" reference resolves.
	sdkYAML := filepath.Join(cfg.ProjectDir, ".workshop", cfg.Agent.Name(), "sdk.yaml")
	if _, err := os.Stat(sdkYAML); err != nil {
		t.Errorf("agent SDK not seeded: %v", err)
	}
	hook := filepath.Join(cfg.ProjectDir, ".workshop", cfg.Agent.Name(), "hooks", "setup-base")
	if info, err := os.Stat(hook); err != nil {
		t.Errorf("setup-base hook not seeded: %v", err)
	} else if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("setup-base hook is not executable: %v", info.Mode())
	}
}

func TestEnsureWorkshop_PresentReuses(t *testing.T) {
	// `info` succeeds -> workshop exists -> no launch.
	fc := &fakeCommander{} // all calls succeed
	r := New(testConfig(t), fc)

	if err := r.ensureWorkshop(context.Background()); err != nil {
		t.Fatalf("ensureWorkshop: %v", err)
	}

	if got := fc.verbs(); !slices.Equal(got, []string{"info"}) {
		t.Errorf("calls = %v, want [info] (reuse, no launch)", got)
	}
}

// findCallN returns the nth (0-based) recorded Cmd whose verb matches, or fails.
func (f *fakeCommander) findCallN(t *testing.T, verb string, n int) Cmd {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := 0
	for _, c := range f.calls {
		if verbOf(c) == verb {
			if seen == n {
				return c
			}
			seen++
		}
	}
	t.Fatalf("no call #%d with verb %q in %v", n, verb, f.verbs())
	return Cmd{}
}

func TestRun_PerRunSequence(t *testing.T) {
	fc := &fakeCommander{errFn: failOnVerb("info")} // workshop absent -> launches
	cfg := testConfig(t)
	r := New(cfg, fc)

	res, err := r.Run(context.Background(), RunRequest{
		Branch: "agent/skeleton",
		Prompt: "scaffold a go module",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The verified recipe order: ensure (info+launch) -> worktree add ->
	// stop -> remount workspace -> remount gitcommon -> remount sessions ->
	// start -> exec. The sessions remount is present because OpenCode is a
	// session-capable agent.
	wantSeq := []string{"info", "launch", "worktree", "stop", "remount", "remount", "remount", "start", "exec", "rev-parse"}
	if got := fc.verbs(); !slices.Equal(got, wantSeq) {
		t.Fatalf("sequence =\n  %v\nwant\n  %v", got, wantSeq)
	}

	// Two-mount rule: workspace -> the worktree; gitcommon -> the repo's .git.
	// Assert the whole remount argv against the builder the production path uses
	// (its flag layout is pinned by TestWorkshopArgs), so this test stays about
	// wiring the right plug+source rather than positional argument shape.
	wantWs := remountArgs(cfg.ProjectDir, cfg.Workshop, cfg.Agent.Name(), "workspace", res.WorktreePath)
	if got := fc.findCallN(t, "remount", 0).Args; !slices.Equal(got, wantWs) {
		t.Errorf("workspace remount args =\n  %v\nwant\n  %v", got, wantWs)
	}
	// The gitcommon source is the host .git absolute path, hard-coded here as
	// the load-bearing half of the two-mount rule.
	wantGc := remountArgs(cfg.ProjectDir, cfg.Workshop, cfg.Agent.Name(), "gitcommon", "/home/dev/repos/myproject/.git")
	if got := fc.findCallN(t, "remount", 1).Args; !slices.Equal(got, wantGc) {
		t.Errorf("gitcommon remount args =\n  %v\nwant\n  %v", got, wantGc)
	}
	// Sessions mount: a host sessions dir under the project is bound so the
	// agent's session files survive the swap. It is the third remount.
	wantSess := remountArgs(cfg.ProjectDir, cfg.Workshop, cfg.Agent.Name(), "sessions", filepath.Join(cfg.ProjectDir, "sessions"))
	if got := fc.findCallN(t, "remount", 2).Args; !slices.Equal(got, wantSess) {
		t.Errorf("sessions remount args =\n  %v\nwant\n  %v", got, wantSess)
	}
	// The host sessions dir must exist for the remount source to resolve.
	if _, err := os.Stat(filepath.Join(cfg.ProjectDir, "sessions")); err != nil {
		t.Errorf("host sessions dir not created: %v", err)
	}

	// The worktree is created on the requested branch.
	wtAdd := fc.findCallN(t, "worktree", 0)
	if !slices.Contains(wtAdd.Args, "agent/skeleton") {
		t.Errorf("worktree add missing branch: %v", wtAdd.Args)
	}
	if !slices.Contains(wtAdd.Args, res.WorktreePath) {
		t.Errorf("worktree add missing worktree path %q: %v", res.WorktreePath, wtAdd.Args)
	}

	// exec carries the agent command + prompt, env keys, and /workspace cwd.
	exec := fc.findCallN(t, "exec", 0)
	if !slices.Contains(exec.Args, "scaffold a go module") {
		t.Errorf("exec missing prompt: %v", exec.Args)
	}
	if !slices.Contains(exec.Args, "OPENROUTER_API_KEY") {
		t.Errorf("exec missing env key: %v", exec.Args)
	}
	if !slices.Contains(exec.Args, "/workspace") {
		t.Errorf("exec missing /workspace cwd: %v", exec.Args)
	}
}

// A session-capable agent has its session-dir env var redirected to the sessions
// mount target at exec time, so the agent writes session files into the bound
// host directory rather than the ephemeral rootfs. The value is set explicitly
// (NAME=VALUE), unlike inherited credential keys.
func TestRun_RedirectsSessionDirEnv(t *testing.T) {
	fc := &fakeCommander{}
	cfg := testConfig(t)
	r := New(cfg, fc)

	if _, err := r.Run(context.Background(), RunRequest{Branch: "agent/x", Prompt: "go"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	spec, ok := cfg.Agent.Sessions()
	if !ok {
		t.Fatal("test config agent is not session-capable")
	}
	// The redirect value must be the sessions mount target itself (the host dir
	// Setup bound there), so the agent writes session files into the bind-mount.
	want := spec.DirEnv + "=" + sessionsTarget // e.g. XDG_DATA_HOME=/sessions
	exec := fc.findCallN(t, "exec", 0)
	if !slices.Contains(exec.Args, want) {
		t.Errorf("exec missing session-dir redirect %q in argv: %v", want, exec.Args)
	}
}

// A run that carries a resume-session id threads it through the AgentProfile's
// command builder into the agent exec, so the agent continues the prior session
// rather than starting fresh. This is the end-to-end resume path: RunRequest ->
// CommandOptions -> BuildCommand -> exec argv.
func TestRun_ResumeSessionReachesExec(t *testing.T) {
	fc := &fakeCommander{}
	cfg := testConfig(t)
	r := New(cfg, fc)

	const sessionID = "ses_abc123"
	if _, err := r.Run(context.Background(), RunRequest{
		Branch: "agent/x", Prompt: "go", ResumeSession: sessionID,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	exec := fc.findCallN(t, "exec", 0)
	if !slices.Contains(exec.Args, sessionID) {
		t.Errorf("exec missing resume session id %q in argv: %v", sessionID, exec.Args)
	}
}

// A plain resume (no Fork) threads the session id to exec but must NOT carry the
// agent's fork flag: resume continues the source session in place, whereas a
// stray --fork would divert the work into a new forked session and leave the
// resumed conversation untouched. This pins the negative at the Runner layer;
// TestRun_ResumeSessionReachesExec only asserts the id is present.
func TestRun_ResumeWithoutForkOmitsForkFlag(t *testing.T) {
	fc := &fakeCommander{}
	cfg := testConfig(t)
	r := New(cfg, fc)

	if _, err := r.Run(context.Background(), RunRequest{
		Branch: "agent/x", Prompt: "go", ResumeSession: "ses_abc123",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	exec := fc.findCallN(t, "exec", 0)
	if slices.Contains(exec.Args, "--fork") {
		t.Errorf("plain resume exec carries --fork; it must not: %v", exec.Args)
	}
}

// A fork run resumes a prior session AND branches it: the session id and the
// agent's fork flag both reach the exec, while Setup allocates a fresh worktree
// on the fork's branch. Together these isolate a divergent continuation at the
// session level (the source conversation is not mutated) and the filesystem
// level (a new worktree) — the two halves of taboo's fork.
func TestRun_ForkReachesExecAndAllocatesNewWorktree(t *testing.T) {
	fc := &fakeCommander{}
	cfg := testConfig(t)
	r := New(cfg, fc)

	const sessionID = "ses_src"
	res, err := r.Run(context.Background(), RunRequest{
		Branch: "fork/divergent", Prompt: "go", ResumeSession: sessionID, Fork: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Session-level isolation: the resume id and the agent's fork flag both reach
	// exec, so the agent forks the source session rather than appending to it.
	exec := fc.findCallN(t, "exec", 0)
	if !slices.Contains(exec.Args, sessionID) {
		t.Errorf("fork exec missing resume session id %q: %v", sessionID, exec.Args)
	}
	if !slices.Contains(exec.Args, "--fork") {
		t.Errorf("fork exec missing --fork flag: %v", exec.Args)
	}

	// Filesystem-level isolation: a fresh worktree is allocated on the fork branch.
	wtAdd := fc.findCallN(t, "worktree", 0)
	if !slices.Contains(wtAdd.Args, "fork/divergent") {
		t.Errorf("fork did not allocate a worktree on the fork branch: %v", wtAdd.Args)
	}
	if !slices.Contains(wtAdd.Args, res.WorktreePath) {
		t.Errorf("fork worktree path %q missing from worktree add: %v", res.WorktreePath, wtAdd.Args)
	}
}

// An agent with no session store gets none of the sessions wiring: no sessions
// remount in the swap, no session-dir env on exec, and no host sessions dir is
// created. This pins the negative branch of the Sessions() guard.
func TestRun_SessionlessAgent_NoSessionsWiring(t *testing.T) {
	fc := &fakeCommander{}
	cfg := testConfig(t)
	cfg.Agent = stdinProfile{} // Sessions() ok == false
	r := New(cfg, fc)

	if _, err := r.Run(context.Background(), RunRequest{Branch: "agent/x", Prompt: "go"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Only the two core remounts (workspace, gitcommon); no sessions remount.
	remounts := 0
	for _, v := range fc.verbs() {
		if v == "remount" {
			remounts++
		}
	}
	if remounts != 2 {
		t.Errorf("got %d remounts, want 2 (workspace+gitcommon, no sessions); verbs: %v", remounts, fc.verbs())
	}

	// No session-dir redirect leaks into the agent exec.
	exec := fc.findCallN(t, "exec", 0)
	for _, a := range exec.Args {
		if strings.Contains(a, "/sessions") {
			t.Errorf("sessionless agent exec carries a session redirect: %v", exec.Args)
		}
	}

	// No host sessions dir is created when there is nothing to persist.
	if _, err := os.Stat(filepath.Join(cfg.ProjectDir, "sessions")); !os.IsNotExist(err) {
		t.Errorf("host sessions dir created for a sessionless agent (err=%v)", err)
	}
}

func TestRun_StreamsExecOutputToRequestWriters(t *testing.T) {
	var out, errBuf strings.Builder
	fc := &fakeCommander{
		stdoutFn: func(c Cmd) string {
			if verbOf(c) == "exec" {
				return "agent says hi\n"
			}
			return ""
		},
	}
	cfg := testConfig(t)
	r := New(cfg, fc)

	_, err := r.Run(context.Background(), RunRequest{
		Branch: "agent/x", Prompt: "go",
		Stdout: &out, Stderr: &errBuf,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	exec := fc.findCallN(t, "exec", 0)
	if exec.Stdout == nil {
		t.Fatal("exec Cmd.Stdout not wired to request writer")
	}
	if out.String() != "agent says hi\n" {
		t.Errorf("streamed stdout = %q, want %q", out.String(), "agent says hi\n")
	}
}

func TestRun_CapturesExecStdout(t *testing.T) {
	// The runner retains the agent's exec stdout on RunResult.Output. When the
	// caller also supplies a Stdout writer, it tees: both the caller's writer
	// and RunResult.Output receive the output.
	for _, tc := range []struct {
		name   string
		stream bool
	}{
		{"no caller writer", false},
		{"tees to caller writer", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			fc := &fakeCommander{
				stdoutFn: func(c Cmd) string {
					if verbOf(c) == "exec" {
						return "agent did stuff\n"
					}
					return ""
				},
			}
			cfg := testConfig(t)

			req := RunRequest{Branch: "agent/x", Prompt: "go"}
			if tc.stream {
				req.Stdout = &out
			}
			res, err := New(cfg, fc).Run(context.Background(), req)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			if res.Output != "agent did stuff\n" {
				t.Errorf("Output = %q, want %q", res.Output, "agent did stuff\n")
			}
			if tc.stream && out.String() != "agent did stuff\n" {
				t.Errorf("streamed stdout = %q, want %q", out.String(), "agent did stuff\n")
			}
		})
	}
}

func TestRun_CapturesCommit(t *testing.T) {
	fc := &fakeCommander{
		stdoutFn: func(c Cmd) string {
			if verbOf(c) == "rev-parse" {
				return "deadbeefcafe\n"
			}
			return ""
		},
	}
	cfg := testConfig(t)
	r := New(cfg, fc)

	res, err := r.Run(context.Background(), RunRequest{Branch: "agent/x", Prompt: "go"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Commit != "deadbeefcafe" {
		t.Errorf("Commit = %q, want deadbeefcafe", res.Commit)
	}

	// rev-parse runs against the worktree, after exec.
	rp := fc.findCallN(t, "rev-parse", 0)
	if !slices.Contains(rp.Args, res.WorktreePath) {
		t.Errorf("rev-parse not run against worktree %q: %v", res.WorktreePath, rp.Args)
	}
	verbs := fc.verbs()
	if verbs[len(verbs)-1] != "rev-parse" {
		t.Errorf("rev-parse should be last; sequence = %v", verbs)
	}
}

// stdinProfile is a minimal AgentProfile that delivers the prompt on stdin (as
// the Claude/Codex/Pi agents do), used to exercise the Exec stdin path that
// OpenCode — which delivers the prompt in argv — leaves untaken.
type stdinProfile struct{}

func (stdinProfile) Name() string { return "opencode" }
func (stdinProfile) BuildCommand(o CommandOptions) AgentCommand {
	return AgentCommand{Argv: []string{"claude", "--print", "-p", "-"}, Stdin: o.Prompt}
}
func (stdinProfile) CredentialEnvKeys() []string   { return nil }
func (stdinProfile) Sessions() (SessionSpec, bool) { return SessionSpec{}, false }

// A stdin-delivery agent has its prompt piped to the exec's stdin, and the
// prompt must not also appear in argv. This pins the ac.Stdin wiring in Exec,
// the whole reason AgentCommand carries a Stdin field (see ADR 0001).
func TestExec_StdinDeliveryAgentPipesPromptToStdin(t *testing.T) {
	fc := &fakeCommander{}
	cfg := testConfig(t)
	cfg.Agent = stdinProfile{}
	r := New(cfg, fc)

	const prompt = "do the task"
	if _, err := r.Run(context.Background(), RunRequest{Branch: "agent/x", Prompt: prompt}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	exec := fc.findCallN(t, "exec", 0)
	if exec.Stdin == nil {
		t.Fatal("exec Cmd.Stdin not wired for a stdin-delivery agent")
	}
	got, err := io.ReadAll(exec.Stdin)
	if err != nil {
		t.Fatalf("read exec stdin: %v", err)
	}
	if string(got) != prompt {
		t.Errorf("exec stdin = %q, want %q", got, prompt)
	}
	// The prompt rides stdin, so it must not leak into argv.
	if slices.Contains(exec.Args, prompt) {
		t.Errorf("prompt leaked into argv for a stdin-delivery agent: %v", exec.Args)
	}
}
