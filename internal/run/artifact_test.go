package run

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestSetup_PopulatesHandle pins the tracer-bullet wiring: a successful Setup
// hands the RunResult a private handle carrying the capability to read the run's
// artifacts. The handle must back-reference the host repo, point at the same
// worktree path the result reports, and carry a live commander for future
// repo-backed reads.
func TestSetup_PopulatesHandle(t *testing.T) {
	cfg := testConfig(t)
	fc := &fakeCommander{}
	r := New(cfg, fc)

	res, err := r.Setup(context.Background(), RunRequest{Branch: "agent/x", Prompt: "go"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	if res.handle == nil {
		t.Fatal("Setup did not populate res.handle")
	}
	if res.handle.repoPath != cfg.RepoPath {
		t.Errorf("handle.repoPath = %q, want %q", res.handle.repoPath, cfg.RepoPath)
	}
	if res.handle.worktreePath != res.WorktreePath {
		t.Errorf("handle.worktreePath = %q, want %q", res.handle.worktreePath, res.WorktreePath)
	}
	if res.handle.cmd == nil {
		t.Error("handle.cmd is nil; want the runner's commander")
	}
}

// TestRunResult_Artifact reads a file from the run's worktree through the handle
// and returns its contents verbatim.
func TestRunResult_Artifact(t *testing.T) {
	dir := t.TempDir()
	const want = "hello\n"
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte(want), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	res := RunResult{handle: &runResultHandle{worktreePath: dir}}
	got, err := res.Artifact("notes.md")
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	if got != want {
		t.Errorf("Artifact = %q, want %q", got, want)
	}
}

// TestRunResult_Artifact_NilHandle covers zero-value RunResults and test fakes
// with no handle: Artifact must return an error (not panic) and an empty string.
func TestRunResult_Artifact_NilHandle(t *testing.T) {
	var res RunResult // zero value: handle is nil
	got, err := res.Artifact("notes.md")
	if err == nil {
		t.Error("Artifact on nil handle: got nil error, want non-nil")
	}
	if got != "" {
		t.Errorf("Artifact on nil handle: got %q, want empty string", got)
	}
}

// TestRunResult_Artifact_RejectsAbsolutePath confirms an absolute path is
// rejected even when a handle is present — Artifact is public API, so it must
// not read outside the run's worktree.
func TestRunResult_Artifact_RejectsAbsolutePath(t *testing.T) {
	res := RunResult{handle: &runResultHandle{worktreePath: t.TempDir()}}
	got, err := res.Artifact("/etc/passwd")
	if err == nil {
		t.Error("Artifact on absolute path: got nil error, want non-nil")
	}
	if got != "" {
		t.Errorf("Artifact on absolute path: got %q, want empty string", got)
	}
}

// TestRunResult_Artifact_RejectsEscape confirms ".." escapes out of the
// worktree are rejected, guarding the public API's trust boundary.
func TestRunResult_Artifact_RejectsEscape(t *testing.T) {
	res := RunResult{handle: &runResultHandle{worktreePath: t.TempDir()}}
	malicious := []string{
		"../secret",
		"sub/../../escape",
		"..",
	}
	for _, relpath := range malicious {
		got, err := res.Artifact(relpath)
		if err == nil {
			t.Errorf("Artifact(%q): got nil error, want non-nil", relpath)
		}
		if got != "" {
			t.Errorf("Artifact(%q): got %q, want empty string", relpath, got)
		}
	}
}

// TestNewResultWithWorktree_Artifact confirms the public constructor attaches a
// worktree handle so an externally built result's Artifact reads from disk —
// Setup is otherwise the only handle source, which external callers cannot use.
func TestNewResultWithWorktree_Artifact(t *testing.T) {
	dir := t.TempDir()
	const want = "built\n"
	if err := os.WriteFile(filepath.Join(dir, "plan.md"), []byte(want), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	res := NewResultWithWorktree(dir)
	if res.WorktreePath != dir {
		t.Errorf("WorktreePath = %q, want %q", res.WorktreePath, dir)
	}
	got, err := res.Artifact("plan.md")
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	if got != want {
		t.Errorf("Artifact = %q, want %q", got, want)
	}
}

// TestNewResultWithWorktree_ZeroValueStillErrors pins that the constructor is the
// only handle source besides Setup: a hand-built zero-value RunResult has no
// handle and so its Artifact errors.
func TestNewResultWithWorktree_ZeroValueStillErrors(t *testing.T) {
	var res RunResult // zero value, NOT built via NewResultWithWorktree
	if _, err := res.Artifact("plan.md"); err == nil {
		t.Error("zero-value RunResult Artifact: got nil error, want non-nil")
	}
}

// TestRunResult_Artifact_NestedRelpath guards against the escape guard
// over-rejecting legitimate nested paths that stay inside the worktree.
func TestRunResult_Artifact_NestedRelpath(t *testing.T) {
	dir := t.TempDir()
	const want = "nested\n"
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "x.md"), []byte(want), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	res := RunResult{handle: &runResultHandle{worktreePath: dir}}
	got, err := res.Artifact("sub/x.md")
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	if got != want {
		t.Errorf("Artifact = %q, want %q", got, want)
	}
}
