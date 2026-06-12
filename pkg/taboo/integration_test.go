//go:build integration

// Integration tests exercise the real `workshop` CLI and LXD. They launch a
// fresh workshop (which installs the agent SDK — minutes) and remove it after.
//
//	go test -tags integration ./pkg/taboo/ -run Integration -v
//
// The live-agent test additionally requires OPENROUTER_API_KEY in the env.
package taboo

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// nonTmpDir returns a fresh directory under $HOME, registered for cleanup.
//
// It deliberately avoids t.TempDir() (which lives under /tmp): taboo mounts the
// repo's .git at its identical host path inside the workshop, and a target
// under /tmp resolves to the container's tmpfs, where the bind mount silently
// fails (the same class of problem as /run — see CONTEXT.md).
func nonTmpDir(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home dir: %v", err)
	}
	base := filepath.Join(home, ".taboo-it")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// initSeedRepo creates a host git repo with one seed commit and returns its path.
func initSeedRepo(t *testing.T) string {
	t.Helper()
	repo := nonTmpDir(t)
	run := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "seed@example.com")
	run("config", "user.name", "seed")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "seed")
	return repo
}

// newIntegrationRunner builds a Runner against the real workshop CLI and
// registers cleanup that removes the workshop and prunes the worktree.
func newIntegrationRunner(t *testing.T, repo string, agentCmd []string, envKeys []string) (*Runner, Config) {
	t.Helper()
	proj := nonTmpDir(t)
	ws := fmt.Sprintf("taboo-it-%d", os.Getpid())
	cfg := Config{
		Workshop:   ws,
		Base:       "ubuntu@24.04",
		SDK:        "opencode",
		RepoPath:   repo,
		ProjectDir: proj,
		AgentCmd:   agentCmd,
		EnvKeys:    envKeys,
	}
	t.Cleanup(func() {
		// Runs before nonTmpDir's RemoveAll (LIFO), so the project dir still
		// exists here for `workshop remove` to resolve the workshop.
		_ = exec.Command("workshop", "--project", proj, "remove", ws).Run()
		_ = exec.Command("git", "-C", repo, "worktree", "prune").Run()
	})
	return New(cfg, NewExecCommander()), cfg
}

// --- Shared warm-workshop fixture -------------------------------------------
//
// Standing up a workshop (LXD container + SDK install) costs minutes; the
// per-run swap (stop -> remount -> start) costs seconds. To avoid paying the
// launch on every LLM/expensive test, those tests share ONE warm workshop bound
// to ONE seed repo at a fixed host path. Isolation is recovered below the repo:
// each test gets its own branch + worktree (unique per t.Name()), so the only
// shared state is the append-only .git object store. See the integration
// fixture plan for the concern-by-concern rationale.
//
// Assumption: one integration run per host at a time (workshop/LXD is already a
// shared host resource). We do not engineer around concurrent `go test`
// processes; a package mutex only guards a stray t.Parallel() within one run.

const sharedWorkshop = "taboo-it-shared"

// sharedMu serializes shared-fixture runs: the single workshop has one
// `workspace` mount swapped by a global stop/remount/start, so two runs must
// never interleave their swaps.
var sharedMu sync.Mutex

// sharedBase is the fixed root holding the shared seed repo and project dir. It
// lives under $HOME (never /tmp) so the gitcommon mount target resolves inside
// the workshop — see nonTmpDir. It takes no *testing.T so TestMain's setup and
// teardown (which have none) can use it too; a failed home lookup is implausible
// and falls back to a deterministic path rather than panicking.
func sharedBase() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".taboo-it-shared") // unreachable in practice
	}
	return filepath.Join(home, ".taboo-it-shared")
}

func sharedRepoPath() string    { return filepath.Join(sharedBase(), "repo") }
func sharedProjectPath() string { return filepath.Join(sharedBase(), "project") }

// TestMain owns the shared fixture's lifecycle. setup() force-cleans any stale
// state and recreates the seed repo; launch stays lazy (the first shared Run
// triggers ensureWorkshop), so setup is cheap and self-healing. teardown() is
// best-effort — os.Exit skips defers, so it is called explicitly.
func TestMain(m *testing.M) {
	if err := setupShared(); err != nil {
		fmt.Fprintf(os.Stderr, "shared fixture setup: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	teardownShared()
	os.Exit(code)
}

// setupShared force-cleans leftovers from a prior (possibly killed) run, then
// recreates the seed repo at the fixed path. Idempotent: it tolerates an absent
// or Off/Pending stale workshop. Only git is strictly required here — the
// workshop cleanup is best-effort — so setup never hard-fails on a host without
// workshop installed.
func setupShared() error {
	cleanShared()
	return initSharedSeedRepo()
}

// teardownShared removes the shared workshop and wipes the fixed dirs. Best
// effort: a workshop left Off/Pending refuses `remove`, which we tolerate.
func teardownShared() { cleanShared() }

// cleanShared best-effort removes the shared workshop and the fixed base dir.
// `workshop remove` stops-then-deletes but refuses an Off/Pending workshop; in
// the Pending case a `start` first lets the remove through. All errors are
// tolerated (absent workshop, missing project dir, workshop not installed).
func cleanShared() {
	proj := sharedProjectPath()
	if err := exec.Command("workshop", "--project", proj, "remove", sharedWorkshop).Run(); err != nil {
		// Pending refuses remove; bring it up then retry once.
		_ = exec.Command("workshop", "--project", proj, "start", sharedWorkshop).Run()
		_ = exec.Command("workshop", "--project", proj, "remove", sharedWorkshop).Run()
	}
	_ = os.RemoveAll(sharedBase())
}

// initSharedSeedRepo creates the shared seed repo (one commit) at the fixed
// path. Mirrors initSeedRepo's git steps but at a fixed, freshly-wiped location.
func initSharedSeedRepo() error {
	repo := sharedRepoPath()
	if err := os.MkdirAll(repo, 0o755); err != nil {
		return err
	}
	git := func(args ...string) error {
		c := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := c.CombinedOutput(); err != nil {
			return fmt.Errorf("git %v: %w\n%s", args, err, out)
		}
		return nil
	}
	for _, step := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "seed@example.com"},
		{"config", "user.name", "seed"},
	} {
		if err := git(step...); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# seed\n"), 0o644); err != nil {
		return err
	}
	if err := git("add", "-A"); err != nil {
		return err
	}
	return git("commit", "-qm", "seed")
}

// sharedBranch derives a unique, ref-safe branch name from the test name, so
// worktrees and refs never collide between shared-fixture tests. Both
// sharedRunner (for cleanup) and the test body (for RunRequest.Branch) call it,
// so they agree without threading state.
func sharedBranch(t *testing.T) string {
	t.Helper()
	return "agent/" + sanitizeRef(t.Name())
}

// sanitizeRef maps a test name to a git-ref-safe slug (subtests contain '/',
// names may contain spaces).
func sanitizeRef(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, name)
}

// sharedRunner returns a Runner bound to the shared warm workshop + seed repo,
// running the given agent. It takes the package mutex for the test's duration
// (released last, after this test's worktree cleanup) and registers cleanup of
// only this test's branch + worktree, so -count=N and re-runs stay green.
func sharedRunner(t *testing.T, agentCmd []string, envKeys []string) (*Runner, Config) {
	t.Helper()
	sharedMu.Lock()
	t.Cleanup(sharedMu.Unlock) // registered first => runs last (lock held through worktree cleanup)

	cfg := Config{
		Workshop:   sharedWorkshop,
		Base:       "ubuntu@24.04",
		SDK:        "opencode",
		RepoPath:   sharedRepoPath(),
		ProjectDir: sharedProjectPath(),
		AgentCmd:   agentCmd,
		EnvKeys:    envKeys,
	}
	r := New(cfg, NewExecCommander())

	branch := sharedBranch(t)
	wt := r.worktreePath(branch)
	t.Cleanup(func() {
		// Prune only this test's worktree + branch — never `git reset` the trunk
		// (agent commits never land there). This is what makes re-runs re-add.
		_ = exec.Command("git", "-C", cfg.RepoPath, "worktree", "remove", "--force", wt).Run()
		_ = exec.Command("git", "-C", cfg.RepoPath, "branch", "-D", branch).Run()
	})
	return r, cfg
}

// TestIntegration_CommitLandsOnHostBranch drives the full taboo orchestration
// against real workshop using a deterministic shell "agent" (no LLM): it proves
// the launch -> worktree -> stop/remount/start -> exec -> commit-in-place path
// and UID write-through end-to-end.
func TestIntegration_CommitLandsOnHostBranch(t *testing.T) {
	repo := initSeedRepo(t)
	r, cfg := newIntegrationRunner(t, repo, []string{"bash", "-lc"}, nil)

	const script = `set -eux
git config user.email agent@example.com
git config user.name agent
echo "written inside the workshop" > TABOO.md
git add -A
git commit -qm "agent: add TABOO.md"`

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	var agentOut strings.Builder
	res, err := r.Run(ctx, RunRequest{
		Branch: "agent/it", Prompt: script, Timeout: 2 * time.Minute,
		Stdout: &agentOut, Stderr: &agentOut,
	})
	t.Logf("agent exec output:\n%s", agentOut.String())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Commit == "" {
		t.Fatal("RunResult.Commit is empty")
	}

	// The agent's file landed on the host worktree, committed on its branch.
	agentFile := filepath.Join(res.WorktreePath, "TABOO.md")
	info, err := os.Stat(agentFile)
	if err != nil {
		t.Fatalf("agent file not on host worktree: %v", err)
	}
	// UID write-through: the file is owned by the host user that ran the test.
	if info.Mode().Perm() == 0 {
		t.Errorf("unexpected file mode %v", info.Mode())
	}

	logOut, err := exec.Command("git", "-C", res.WorktreePath, "log", "--oneline").CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, logOut)
	}
	if !strings.Contains(string(logOut), "add TABOO.md") {
		t.Errorf("commit not on branch; log:\n%s", logOut)
	}
	t.Logf("workshop=%s commit=%s worktree=%s", cfg.Workshop, res.Commit, res.WorktreePath)
}

// TestIntegration_OpenCodeAgent runs the real OpenCode agent (qwen via
// OpenRouter). Skipped unless OPENROUTER_API_KEY is set.
func TestIntegration_OpenCodeAgent(t *testing.T) {
	if os.Getenv("OPENROUTER_API_KEY") == "" {
		t.Skip("OPENROUTER_API_KEY not set; skipping live-agent integration test")
	}
	agentCmd := []string{
		"opencode", "run", "--log-level", "ERROR",
		"-m", "openrouter/qwen/qwen3-coder-plus",
	}
	// Route through the shared warm workshop: only the first shared run pays the
	// launch; this test reuses it via ensureWorkshop. Isolation is its own
	// per-test branch + worktree (cleaned by sharedRunner).
	r, _ := sharedRunner(t, agentCmd, []string{"OPENROUTER_API_KEY"})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	res, err := r.Run(ctx, RunRequest{
		Branch:  sharedBranch(t),
		Prompt:  "Create a file named HELLO.md containing the single line 'hello from taboo', then commit it with the message 'add HELLO.md'.",
		Timeout: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The runner captures the agent's exec stdout on RunResult.Output even when
	// the caller supplies no Stdout writer of its own (the slice-2 feature).
	if res.Output == "" {
		t.Error("RunResult.Output is empty; agent stdout was not captured")
	}
	t.Logf("captured agent output (%d bytes):\n%s", len(res.Output), res.Output)

	// The agent should have produced at least one commit beyond the seed.
	out, err := exec.Command("git", "-C", res.WorktreePath, "log", "--oneline").CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, out)
	}
	if strings.Count(string(out), "\n") < 2 {
		t.Errorf("expected an agent commit beyond seed; log:\n%s", out)
	}
	t.Logf("agent commit=%s\nlog:\n%s", res.Commit, out)
}
