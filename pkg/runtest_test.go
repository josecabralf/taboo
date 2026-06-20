package taboo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// fakeCommander records every invocation and can be programmed to return stdout
// or fail specific commands. It is the pkg-root test stand-in for the run
// package's Commander seam: the bridge tests (plan_test) drive Plan.Run /
// RunWorkflow over it without touching workshop or git. The full-featured fake
// (concurrency gate, snapshots) lives in pkg/internal/run; this is the trimmed
// subset the facade-level tests need.
type fakeCommander struct {
	mu        sync.Mutex
	calls     []Cmd
	errFn     func(c Cmd) error
	stdoutFn  func(c Cmd) string
	worktrees map[string]struct{}
}

func (f *fakeCommander) Run(_ context.Context, c Cmd) error {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	// Model git's statefulness: a second `worktree add -b <branch>` for a branch
	// already added fails, as real git does, so a loop that re-creates the
	// worktree every iteration is caught rather than silently accepted.
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
	f.mu.Unlock()

	if stdoutFn != nil && c.Stdout != nil {
		if s := stdoutFn(c); s != "" {
			_, _ = c.Stdout.Write([]byte(s))
		}
	}
	if errFn != nil {
		return errFn(c)
	}
	return nil
}

// countVerb returns how many recorded calls have the given workshop/git verb.
func (f *fakeCommander) countVerb(verb string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if verbOf(c) == verb {
			n++
		}
	}
	return n
}

// verbOf returns the workshop/git subcommand verb of a recorded call: the token
// after "--project <dir>" for workshop calls, the token after "-C <repo>" for git.
func verbOf(c Cmd) string {
	if c.Name == "git" {
		if len(c.Args) >= 3 {
			return c.Args[2] // -C <repo> <verb>
		}
		return c.Name
	}
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
// <branch> <path>` invocation. The ok result is false for any other call.
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

// testConfig builds a runnable Config whose RepoPath holds a project workshop.yaml
// the runner can materialize from.
func testConfig(t *testing.T) Config {
	t.Helper()
	repo := t.TempDir()
	writeProjectDef(t, repo, "name: myproject\nbase: ubuntu@24.04\nsdks:\n  - name: go\n")
	return Config{
		Workshop:   "taboo-run",
		Base:       "ubuntu@24.04",
		Agent:      mustProfile("opencode", openCodeModel),
		RepoPath:   repo,
		ProjectDir: t.TempDir(),
	}
}

// writeProjectDef drops a workshop.yaml at the repo root (the file taboo derives from).
func writeProjectDef(t *testing.T, repo, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, "workshop.yaml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write project def: %v", err)
	}
}
