package run

import (
	"errors"
	"io/fs"
	"os"
	osexec "os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/josecabralf/taboo/internal/exec"
)

// TestRunResult_Dispose_RemovesWorktree pins the tracer bullet: Dispose issues a
// single non-force `git -C <repo> worktree remove <worktree>` through the
// handle's commander — matching `taboo clean`'s non-force teardown.
func TestRunResult_Dispose_RemovesWorktree(t *testing.T) {
	dir := t.TempDir()
	fc := &fakeCommander{}
	res := RunResult{
		handle: &runResultHandle{repoPath: "/repo", worktreePath: dir, cmd: fc},
	}

	if err := res.Dispose(); err != nil {
		t.Fatalf("Dispose: %v", err)
	}

	calls := fc.snapshot()
	if len(calls) != 1 {
		t.Fatalf("Dispose issued %d commands, want exactly 1", len(calls))
	}
	c := calls[0]
	want := []string{"-C", "/repo", "worktree", "remove", dir}
	if c.Name != "git" || !slices.Equal(c.Args, want) {
		t.Errorf("command = %q %v, want git %v", c.Name, c.Args, want)
	}
	// Non-force: Dispose must not pass -f/--force (matches taboo clean).
	for _, a := range c.Args {
		if a == "-f" || a == "--force" {
			t.Errorf("Dispose used force flag %q; want a non-force remove", a)
		}
	}
}

// TestRunResult_Dispose_NilHandle pins that a result with no worktree handle
// (zero value or a fake) returns a clear error instead of panicking.
func TestRunResult_Dispose_NilHandle(t *testing.T) {
	var res RunResult // zero value: handle is nil
	if err := res.Dispose(); err == nil {
		t.Error("Dispose on nil handle: got nil error, want non-nil")
	}
}

// TestRunResult_Dispose_IdempotentWhenAlreadyGone pins idempotency: a worktree
// already removed makes Dispose a clean no-op (nil error, zero commands) instead
// of letting git fail with "not a working tree".
func TestRunResult_Dispose_IdempotentWhenAlreadyGone(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "never-created")
	fc := &fakeCommander{}
	res := RunResult{handle: &runResultHandle{repoPath: "/repo", worktreePath: gone, cmd: fc}}

	if err := res.Dispose(); err != nil {
		t.Fatalf("Dispose on already-gone worktree: %v", err)
	}
	if n := len(fc.snapshot()); n != 0 {
		t.Errorf("issued %d commands for an already-gone worktree, want 0", n)
	}
}

// TestRunResult_Dispose_NilCommander pins that a present handle with no commander
// returns a clear error rather than panicking on a nil-interface call.
func TestRunResult_Dispose_NilCommander(t *testing.T) {
	res := RunResult{handle: &runResultHandle{worktreePath: t.TempDir()}} // cmd is nil
	if err := res.Dispose(); err == nil {
		t.Error("Dispose with nil commander: got nil error, want non-nil")
	}
}

// TestRunResult_Dispose_LeavesBranchAndWorkshop pins that Dispose is worktree-only:
// it never deletes the branch ref nor touches the workshop, so a later push or run
// can reuse them.
func TestRunResult_Dispose_LeavesBranchAndWorkshop(t *testing.T) {
	dir := t.TempDir()
	fc := &fakeCommander{}
	res := RunResult{handle: &runResultHandle{repoPath: "/repo", worktreePath: dir, cmd: fc}}

	if err := res.Dispose(); err != nil {
		t.Fatalf("Dispose: %v", err)
	}
	for _, c := range fc.snapshot() {
		if c.Name == "workshop" {
			t.Errorf("Dispose issued a workshop command: %v", c.Args)
		}
		if slices.Contains(c.Args, "branch") || slices.Contains(c.Args, "-D") || slices.Contains(c.Args, "-d") {
			t.Errorf("Dispose touched the branch ref: %v", c.Args)
		}
	}
}

// TestRunResult_Dispose_RealGitRemovesWorktreeKeepsBranch is the end-to-end proof
// for the "worktrees no longer accumulate" criterion: a real non-force
// `git worktree remove` frees the worktree on disk while the branch ref survives,
// and a repeat Dispose is a clean no-op.
func TestRunResult_Dispose_RealGitRemovesWorktreeKeepsBranch(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
	} {
		runGitT(t, repo, args...)
	}

	wt := filepath.Join(t.TempDir(), "wt")
	runGitT(t, repo, "worktree", "add", "-q", "-b", "agent/x", wt)
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("worktree not created: %v", err)
	}

	res := RunResult{handle: &runResultHandle{repoPath: repo, worktreePath: wt, cmd: exec.NewExecCommander()}}
	if err := res.Dispose(); err != nil {
		t.Fatalf("Dispose: %v", err)
	}
	if _, err := os.Stat(wt); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("worktree still present after Dispose: stat err = %v", err)
	}
	if out := captureGitT(t, repo, "branch", "--list", "agent/x"); !strings.Contains(out, "agent/x") {
		t.Errorf("branch agent/x missing after Dispose; want it to survive. branches: %q", out)
	}
	// Idempotent: a second Dispose, now that the worktree is gone, is a clean no-op.
	if err := res.Dispose(); err != nil {
		t.Errorf("second Dispose (already gone): %v", err)
	}
}

// runGitT runs git in dir and fails the test on error.
func runGitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := captureGitOutput(dir, args...); err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
}

// captureGitT runs git in dir, returning trimmed stdout, failing on error.
func captureGitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := captureGitOutput(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

func captureGitOutput(dir string, args ...string) (string, error) {
	cmd := osexec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
