//go:build integration

// Integration tests exercise the real `workshop` CLI and LXD. They launch a
// fresh workshop (which installs the agent SDK, taking minutes) and remove it after.
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
	"slices"
	"strings"
	"testing"
	"time"
)

// nonTmpDir returns a fresh directory under $HOME, registered for cleanup.
//
// It deliberately avoids t.TempDir() (which lives under /tmp): taboo mounts the
// repo's .git at its identical host path inside the workshop, and a target
// under /tmp resolves to the container's tmpfs, where the bind mount silently
// fails (the same class of problem as /run; see CONTEXT.md).
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

// scriptProfile is a deterministic stand-in AgentProfile for integration tests
// that need a predictable "agent" (a shell script) rather than a live LLM. It
// reports Name() "opencode" so the real opencode SDK environment is still
// launched and remounted, while BuildCommand runs the supplied argv with the
// prompt appended.
type scriptProfile struct {
	argv []string
}

func (scriptProfile) Name() string { return "opencode" }

func (p scriptProfile) BuildCommand(opts CommandOptions) AgentCommand {
	return AgentCommand{Argv: append(slices.Clone(p.argv), opts.Prompt)}
}

func (scriptProfile) CredentialEnvKeys() []string { return nil }

func (scriptProfile) Sessions() (SessionSpec, bool) { return SessionSpec{}, false }

// newIntegrationRunner builds a Runner against the real workshop CLI and
// registers cleanup that removes the workshop and prunes the worktree.
func newIntegrationRunner(t *testing.T, repo string, agent AgentProfile) (*Runner, Config) {
	t.Helper()
	proj := nonTmpDir(t)
	ws := fmt.Sprintf("taboo-it-%d", os.Getpid())
	cfg := Config{
		Workshop:   ws,
		Base:       "ubuntu@24.04",
		Agent:      agent,
		RepoPath:   repo,
		ProjectDir: proj,
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
	r, cfg := newIntegrationRunner(t, repo, scriptProfile{argv: []string{"bash", "-lc"}})

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
	r, cfg := newIntegrationRunner(t, repo, OpenCode("openrouter/qwen/qwen3-coder-plus"))

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

	// Session capture: OpenCode's session storage was redirected (XDG_DATA_HOME)
	// to the mounted host sessions dir, so its session files must be present on
	// the host after the run (write-through over the bind-mount).
	spec, _ := OpenCode("").Sessions()
	sessDir := filepath.Join(cfg.ProjectDir, "sessions", spec.Subdir)
	before := dirEntryNames(t, sessDir)
	if len(before) == 0 {
		t.Fatalf("host sessions dir %q is empty after the run; nothing was captured", sessDir)
	}
	t.Logf("host session files under %s after run 1: %d entries", sessDir, len(before))

	// Survival across the swap: the session files written above were produced
	// after run 1's final `start`, so run 1 never actually swapped them. Drive a
	// second Setup against the reused workshop — another stop/remount/start, which
	// wipes the rootfs — and assert every run-1 file is still on the host. This is
	// the acceptance criterion the single-run write-through check cannot prove.
	if _, err := r.Setup(ctx, RunRequest{Branch: "agent/opencode-2"}); err != nil {
		t.Fatalf("second Setup (stop/remount/start swap): %v", err)
	}
	after := dirEntryNames(t, sessDir)
	for _, name := range before {
		if !slices.Contains(after, name) {
			t.Errorf("session file %q did not survive a second stop/remount/start swap; after=%v", name, after)
		}
	}
	t.Logf("session files survived a second swap: %d before, %d after", len(before), len(after))
}

// dirEntryNames returns the names of dir's entries, failing the test if it
// cannot be read.
func dirEntryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %q: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
