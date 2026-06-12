package taboo

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeCommander records every invocation and can be programmed to fail
// specific commands via errFn.
type fakeCommander struct {
	calls    []Cmd
	errFn    func(c Cmd) error
	stdoutFn func(c Cmd) string // programmed stdout for a matched call
}

func (f *fakeCommander) Run(_ context.Context, c Cmd) error {
	f.calls = append(f.calls, c)
	if f.stdoutFn != nil && c.Stdout != nil {
		if s := f.stdoutFn(c); s != "" {
			_, _ = io.WriteString(c.Stdout, s)
		}
	}
	if f.errFn != nil {
		return f.errFn(c)
	}
	return nil
}

// verbs returns the workshop/git subcommand verb of each recorded call, in
// order, for sequence assertions. For workshop calls the verb is the token
// after "--project <dir>"; for git it is "worktree".
func (f *fakeCommander) verbs() []string {
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
		SDK:        "opencode",
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
	sdkYAML := filepath.Join(cfg.ProjectDir, ".workshop", cfg.SDK, "sdk.yaml")
	if _, err := os.Stat(sdkYAML); err != nil {
		t.Errorf("agent SDK not seeded: %v", err)
	}
	hook := filepath.Join(cfg.ProjectDir, ".workshop", cfg.SDK, "hooks", "setup-base")
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
	cfg.AgentCmd = []string{"opencode", "run", "--log-level", "ERROR", "-m", "openrouter/qwen/qwen3-coder-plus"}
	cfg.EnvKeys = []string{"OPENROUTER_API_KEY"}
	r := New(cfg, fc)

	res, err := r.Run(context.Background(), RunRequest{
		Branch: "agent/skeleton",
		Prompt: "scaffold a go module",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The verified recipe order: ensure (info+launch) -> worktree add ->
	// stop -> remount workspace -> remount gitcommon -> start -> exec.
	wantSeq := []string{"info", "launch", "worktree", "stop", "remount", "remount", "start", "exec", "rev-parse"}
	if got := fc.verbs(); !slices.Equal(got, wantSeq) {
		t.Fatalf("sequence =\n  %v\nwant\n  %v", got, wantSeq)
	}

	// Two-mount rule: workspace -> the worktree; gitcommon -> the repo's .git.
	// Assert the whole remount argv against the builder the production path uses
	// (its flag layout is pinned by TestWorkshopArgs), so this test stays about
	// wiring the right plug+source rather than positional argument shape.
	wantWs := remountArgs(cfg.ProjectDir, cfg.Workshop, cfg.SDK, "workspace", res.WorktreePath)
	if got := fc.findCallN(t, "remount", 0).Args; !slices.Equal(got, wantWs) {
		t.Errorf("workspace remount args =\n  %v\nwant\n  %v", got, wantWs)
	}
	// The gitcommon source is the host .git absolute path, hard-coded here as
	// the load-bearing half of the two-mount rule.
	wantGc := remountArgs(cfg.ProjectDir, cfg.Workshop, cfg.SDK, "gitcommon", "/home/dev/repos/myproject/.git")
	if got := fc.findCallN(t, "remount", 1).Args; !slices.Equal(got, wantGc) {
		t.Errorf("gitcommon remount args =\n  %v\nwant\n  %v", got, wantGc)
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
	cfg.AgentCmd = []string{"opencode", "run"}
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
			cfg.AgentCmd = []string{"opencode", "run"}

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
	cfg.AgentCmd = []string{"opencode", "run"}
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
