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
	repo := initSeedRepo(t)
	agentCmd := []string{
		"opencode", "run", "--log-level", "ERROR",
		"-m", "openrouter/qwen/qwen3-coder-plus",
	}
	r, _ := newIntegrationRunner(t, repo, agentCmd, []string{"OPENROUTER_API_KEY"})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	res, err := r.Run(ctx, RunRequest{
		Branch:  "agent/opencode",
		Prompt:  "Create a file named HELLO.md containing the single line 'hello from taboo', then commit it with the message 'add HELLO.md'.",
		Timeout: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

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
