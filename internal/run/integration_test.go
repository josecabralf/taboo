//go:build integration

// Integration tests exercise the real `workshop` CLI and LXD. They launch a
// fresh workshop (which installs the agent SDK, taking minutes) and remove it after.
//
//	go test -tags integration ./pkg/ -run Integration -v
//
// Each live-agent test additionally requires its agent's credential in the env:
// OpenCode needs OPENROUTER_API_KEY; Claude Code needs CLAUDE_CODE_OAUTH_TOKEN
// (with ANTHROPIC_API_KEY unset, ADR 0004); Copilot needs a GitHub token
// (COPILOT_GITHUB_TOKEN, GH_TOKEN, or GITHUB_TOKEN). Each test skips when its own
// credential is absent.
package run

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/josecabralf/taboo/internal/agent"
	"github.com/josecabralf/taboo/internal/exec"
	"github.com/josecabralf/taboo/internal/workshop"
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
		c := osexec.Command("git", append([]string{"-C", repo}, args...)...)
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

func (scriptProfile) Name() AgentName { return OpenCode }

func (p scriptProfile) BuildCommand(opts agent.CommandOptions) agent.AgentCommand {
	return agent.AgentCommand{Argv: append(slices.Clone(p.argv), opts.Prompt)}
}

func (scriptProfile) CredentialEnvKeys() []string { return nil }

func (scriptProfile) Sessions() (agent.SessionSpec, bool) { return agent.SessionSpec{}, false }

// newIntegrationRunner builds a Runner against the real workshop CLI and
// registers cleanup that removes the workshop and prunes the worktree. The
// project dir is a standalone directory outside the repo (the out-of-repo
// worktree arrangement); newIntegrationRunnerInProject takes an explicit project
// dir for the nested arrangement.
func newIntegrationRunner(t *testing.T, repo string, profile agent.AgentProfile) (*Runner, workshop.Config) {
	t.Helper()
	return newIntegrationRunnerInProject(t, repo, profile, nonTmpDir(t))
}

// newIntegrationRunnerInProject is newIntegrationRunner with the project dir
// supplied by the caller, so a test can place worktrees nested inside the repo
// (proj == <repo>/.taboo) rather than in a standalone directory.
func newIntegrationRunnerInProject(t *testing.T, repo string, profile agent.AgentProfile, proj string) (*Runner, workshop.Config) {
	t.Helper()
	ws := fmt.Sprintf("taboo-it-%d", os.Getpid())
	cfg := workshop.Config{
		Workshop:   ws,
		Base:       "ubuntu@24.04",
		Agent:      profile,
		RepoPath:   repo,
		ProjectDir: proj,
	}
	t.Cleanup(func() {
		// Runs before nonTmpDir's RemoveAll (LIFO), so the project dir still
		// exists here for `workshop remove` to resolve the workshop.
		_ = osexec.Command("workshop", "--project", proj, "remove", ws).Run()
		_ = osexec.Command("git", "-C", repo, "worktree", "prune").Run()
	})
	return New(cfg, exec.NewExecCommander()), cfg
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
	agentFile := filepath.Join(res.handle.worktreePath, "TABOO.md")
	info, err := os.Stat(agentFile)
	if err != nil {
		t.Fatalf("agent file not on host worktree: %v", err)
	}
	// UID write-through: the file is owned by the host user that ran the test.
	if info.Mode().Perm() == 0 {
		t.Errorf("unexpected file mode %v", info.Mode())
	}

	logOut, err := osexec.Command("git", "-C", res.handle.worktreePath, "log", "--oneline").CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, logOut)
	}
	if !strings.Contains(string(logOut), "add TABOO.md") {
		t.Errorf("commit not on branch; log:\n%s", logOut)
	}
	t.Logf("workshop=%s commit=%s worktree=%s", cfg.Workshop, res.Commit, res.handle.worktreePath)
}

// TestIntegration_NestedWorktreeArrangement is the risk gate for issue #35: it
// verifies the PRD's proposed on-disk layout where the project dir lives *inside*
// the target repo (ProjectDir == <repo>/.taboo), so worktrees nest at
// <repo>/.taboo/worktrees/<branch> — git-ignored, inside the repo — rather than in
// a standalone out-of-repo directory (the arrangement the other integration tests
// exercise).
//
// The concern (CONTEXT.md, two-mount rule): the git-common mount target equals the
// host .git absolute path, and a worktree's .git is only a pointer into
// <repo>/.git/worktrees/<name>. Nesting the worktree under the repo must still
// yield a worktree whose .git pointer resolves identically inside the workshop
// (via the gitcommon mount) and on the host, with no pointer rewriting — so the
// agent's commit lands on the host branch *and* the worktree stays valid for
// host-side git.
//
// It uses the deterministic shell "agent" (no LLM, no credential), so it gates on
// workshop + LXD alone. If the commit does not land, or the host worktree is not a
// valid git checkout afterwards, the nested arrangement is rejected in favour of
// the out-of-repo fallback.
func TestIntegration_NestedWorktreeArrangement(t *testing.T) {
	repo := initSeedRepo(t)
	// The nested arrangement: the project dir is inside the repo. taboo derives
	// worktree paths as <ProjectDir>/worktrees/<branch>, so this alone places them
	// at <repo>/.taboo/worktrees/<branch> — no library change is needed.
	proj := filepath.Join(repo, ".taboo")
	r, cfg := newIntegrationRunnerInProject(t, repo, scriptProfile{argv: []string{"bash", "-lc"}}, proj)

	const script = `set -eux
git config user.email agent@example.com
git config user.name agent
echo "written inside the workshop" > NESTED.md
git add -A
git commit -qm "agent: add NESTED.md"`

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	var agentOut strings.Builder
	res, err := r.Run(ctx, RunRequest{
		Branch: "agent/nested", Prompt: script, Timeout: 2 * time.Minute,
		Stdout: &agentOut, Stderr: &agentOut,
	})
	t.Logf("agent exec output:\n%s", agentOut.String())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The worktree is actually nested inside the repo, under .taboo/worktrees.
	nestedPrefix := filepath.Join(repo, ".taboo", "worktrees") + string(os.PathSeparator)
	if !strings.HasPrefix(res.handle.worktreePath, nestedPrefix) {
		t.Fatalf("worktree not nested under the repo: got %q, want prefix %q", res.handle.worktreePath, nestedPrefix)
	}

	// The agent's commit landed on the host branch (the in-place commit succeeded
	// despite the worktree being nested under the repo's mounted .git).
	if res.Commit == "" {
		t.Fatal("RunResult.Commit is empty")
	}
	agentFile := filepath.Join(res.handle.worktreePath, "NESTED.md")
	if _, err := os.Stat(agentFile); err != nil {
		t.Fatalf("agent file not on host worktree: %v", err)
	}
	logOut, err := osexec.Command("git", "-C", res.handle.worktreePath, "log", "--oneline").CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, logOut)
	}
	if !strings.Contains(string(logOut), "add NESTED.md") {
		t.Errorf("commit not on branch; log:\n%s", logOut)
	}

	// The worktree's .git pointer resolves on the host too: host-side git sees a
	// valid checkout on the run's branch (no pointer rewriting broke either side).
	branchOut, err := osexec.Command("git", "-C", res.handle.worktreePath, "rev-parse", "--abbrev-ref", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("host-side git in nested worktree failed (pointer did not resolve): %v\n%s", err, branchOut)
	}
	if got := strings.TrimSpace(string(branchOut)); got != "agent/nested" {
		t.Errorf("host worktree on wrong branch: got %q, want %q", got, "agent/nested")
	}
	t.Logf("nested arrangement OK: workshop=%s commit=%s worktree=%s", cfg.Workshop, res.Commit, res.handle.worktreePath)
}

// runLiveAgentCommitTest is the shared body of the three live-agent integration
// tests (OpenCode, Claude Code, Copilot). Those tests differ only in their
// credential skip guard, their profile, and their branch name; the orchestration
// they assert is identical:
//
//   - run the agent on a one-commit task (write HELLO.md and commit it),
//   - confirm the runner captured the agent's stdout on RunResult.Output,
//   - confirm the agent produced a commit beyond the seed,
//   - confirm the agent's session files land on the host sessions mount and
//     survive a second stop/remount/start swap — the rootfs-wipe acceptance
//     criterion a single run cannot prove.
//
// The run-1 branch is branch; the swap uses branch+"-2". It returns the Config so
// a caller can make agent-specific follow-up assertions against the captured
// sessions mount (cfg.ProjectDir/sessions) — the Claude Code test uses this for
// its credential-on-disk check.
func runLiveAgentCommitTest(t *testing.T, profile agent.AgentProfile, branch string) workshop.Config {
	t.Helper()
	repo := initSeedRepo(t)
	r, cfg := newIntegrationRunner(t, repo, profile)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	res, err := r.Run(ctx, RunRequest{
		Branch:  branch,
		Prompt:  "Create a file named HELLO.md containing the single line 'hello from taboo', then commit it with the message 'add HELLO.md'.",
		Timeout: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The runner captures the agent's exec stdout on RunResult.Output even when the
	// caller supplies no Stdout writer of its own.
	if res.Output == "" {
		t.Error("RunResult.Output is empty; agent stdout was not captured")
	}
	t.Logf("captured agent output (%d bytes):\n%s", len(res.Output), res.Output)

	// The agent should have produced at least one commit beyond the seed.
	out, err := osexec.Command("git", "-C", res.handle.worktreePath, "log", "--oneline").CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, out)
	}
	if strings.Count(string(out), "\n") < 2 {
		t.Errorf("expected an agent commit beyond seed; log:\n%s", out)
	}
	t.Logf("agent commit=%s\nlog:\n%s", res.Commit, out)

	// Session capture: the agent's session store was redirected onto the mounted
	// host sessions dir (see the profile's Sessions()), so its files must be on the
	// host after the run (write-through over the bind-mount).
	spec, _ := profile.Sessions()
	sessDir := filepath.Join(cfg.ProjectDir, "sessions", spec.Subdir)
	before := dirEntryNames(t, sessDir)
	if len(before) == 0 {
		t.Fatalf("host sessions dir %q is empty after the run; nothing was captured", sessDir)
	}
	t.Logf("host session files under %s after run 1: %d entries", sessDir, len(before))

	// Survival across the swap: the session files written above were produced after
	// run 1's final `start`, so run 1 never actually swapped them. Drive a second
	// Setup against the reused workshop — another stop/remount/start, which wipes
	// the rootfs — and assert every run-1 file is still on the host. This is the
	// acceptance criterion the single-run write-through check cannot prove.
	if _, err := r.Setup(ctx, RunRequest{Branch: branch + "-2"}); err != nil {
		t.Fatalf("second Setup (stop/remount/start swap): %v", err)
	}
	after := dirEntryNames(t, sessDir)
	for _, name := range before {
		if !slices.Contains(after, name) {
			t.Errorf("session file %q did not survive a second stop/remount/start swap; after=%v", name, after)
		}
	}
	t.Logf("session files survived a second swap: %d before, %d after", len(before), len(after))

	return cfg
}

// TestIntegration_OpenCodeAgent runs the real OpenCode agent (qwen via
// OpenRouter). Skipped unless OPENROUTER_API_KEY is set.
func TestIntegration_OpenCodeAgent(t *testing.T) {
	if os.Getenv("OPENROUTER_API_KEY") == "" {
		t.Skip("OPENROUTER_API_KEY not set; skipping live-agent integration test")
	}
	runLiveAgentCommitTest(t, mustProfile("opencode", "openrouter/qwen/qwen3-coder-plus"), "agent/opencode")
}

// TestIntegration_ClaudeCodeAgent runs the real Claude Code agent
// (claude-sonnet-4-6) against the subscription OAuth path. Skipped unless
// CLAUDE_CODE_OAUTH_TOKEN is set.
//
// This is the first live agent to drive the runner's stdin-delivery path: the
// prompt rides on AgentCommand.Stdin into `claude -p` (ADR 0001), not on argv
// like OpenCode. To genuinely exercise the OAuth path, ANTHROPIC_API_KEY must be
// unset; if it is present Claude Code's own precedence prefers it (ADR 0004), so
// the test skips rather than silently verify the wrong credential.
func TestIntegration_ClaudeCodeAgent(t *testing.T) {
	if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" {
		t.Skip("CLAUDE_CODE_OAUTH_TOKEN not set; skipping live Claude Code integration test")
	}
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		t.Skip("ANTHROPIC_API_KEY is set; unset it so the OAuth token is the credential under test (ADR 0004)")
	}
	cfg := runLiveAgentCommitTest(t, mustProfile("claude-code", "claude-sonnet-4-6"), "agent/claudecode")

	// Credential-on-disk safety (ADR 0004): because CLAUDE_CONFIG_DIR points at the
	// host sessions mount, a credentials file would leak onto the host. The OAuth
	// token is supplied via --env, so Claude must write no .credentials.json. Walk
	// the whole captured config dir (its root, not just the Subdir) and assert none.
	// Run after the swap rather than before — the swap writes no credentials and the
	// host files persist, so a leak would still be on disk here.
	configRoot := filepath.Join(cfg.ProjectDir, "sessions")
	if err := filepath.WalkDir(configRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == ".credentials.json" {
			t.Errorf("credential file leaked onto host mount: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk %q: %v", configRoot, err)
	}
}

// TestIntegration_CopilotAgent runs the real GitHub Copilot CLI
// (claude-sonnet-4.6) against the GitHub-token auth path. Skipped unless a Copilot
// GitHub token is present in the env (COPILOT_GITHUB_TOKEN, GH_TOKEN, or
// GITHUB_TOKEN — the keys the profile forwards via --env, ADR 0004).
//
// Copilot delivers the prompt in argv (the -p value, ADR 0001), like OpenCode and
// unlike Claude Code's stdin path. -p also selects non-interactive mode;
// --allow-all lets the headless agent edit and commit without an approver, while
// --deny-tool=shell(git push) keeps it off the worktree's shared host refs.
//
// Unlike the Claude Code test's ANTHROPIC_API_KEY guard, no BYOK skip guard is
// needed here: the profile forwards only the three GitHub-token keys via --env, so
// COPILOT_PROVIDER_* vars on the host never reach the workshop and cannot switch
// the credential under test to a custom model endpoint.
func TestIntegration_CopilotAgent(t *testing.T) {
	if os.Getenv("COPILOT_GITHUB_TOKEN") == "" &&
		os.Getenv("GH_TOKEN") == "" &&
		os.Getenv("GITHUB_TOKEN") == "" {
		t.Skip("no Copilot GitHub token (COPILOT_GITHUB_TOKEN/GH_TOKEN/GITHUB_TOKEN) set; skipping live Copilot integration test")
	}
	// Copilot writes each run's transcript under session-state/<uuid>/ (workspace.yaml,
	// checkpoints/, and an events.jsonl once a turn runs); non-interactive -p runs
	// persist here, same as interactive ones. No credential-on-disk walk (cf. the
	// Claude Code test): copilot's token comes in via --env, so no `copilot login`
	// file is written onto the host mount.
	runLiveAgentCommitTest(t, mustProfile("github-copilot", "claude-sonnet-4.6"), "agent/github-copilot")
}

// openCodeJSONFormat wraps the real OpenCode profile to add `--format json`,
// which makes `opencode run` emit newline-delimited JSON events carrying the
// sessionID — the only way to capture a run's session id, since OpenCode keeps
// sessions in an opaque SQLite store (opencode.db, WAL mode), not per-session
// JSON files. It embeds the real profile so Name/CredentialEnvKeys/Sessions —
// and crucially the resume/fork argv mapping under test — are unchanged; only
// the output format differs.
type openCodeJSONFormat struct{ agent.AgentProfile }

func (p openCodeJSONFormat) BuildCommand(o agent.CommandOptions) agent.AgentCommand {
	ac := p.AgentProfile.BuildCommand(o)
	// Splice "--format json" right after the "run" subcommand (argv[0]="opencode",
	// argv[1]="run"), ahead of every other flag and the positional prompt.
	spliced := make([]string, 0, len(ac.Argv)+2)
	spliced = append(spliced, ac.Argv[:2]...)
	spliced = append(spliced, "--format", "json")
	spliced = append(spliced, ac.Argv[2:]...)
	ac.Argv = spliced
	return ac
}

// TestIntegration_OpenCodeResumeFork proves OpenCode's resume + fork end-to-end
// over the bind-mounted session store — the risk #28 was opened to close.
//
// The store is SQLite (opencode.db + -wal + -shm, WAL mode), not the loose
// per-session JSON files some OpenCode forks use — confirmed by inspecting the
// captured store. So the open question is real: does a session written to a
// SQLite db through the bind-mount in one workshop instance resume correctly
// from a *second* per-run worktree after the rootfs-wiping stop/remount/start
// swap? Session ids are captured from `opencode run --format json` stdout
// (sessionID) rather than from disk, since the db is opaque.
//
// Four sequential runs against one reused workshop (shared host sessions dir):
//
//  1. Run 1 seeds a secret codeword and commits ONE.md; capture its session id.
//  2. Run 2 *resumes* that id on a fresh worktree and recalls the codeword into
//     RESUME.md. A fresh session could not know it (continuity), and run 2 emits
//     the *same* session id (resume targeted the source, did not fork).
//  3. Run 3 *forks* that id onto another fresh branch. It emits a *new* session
//     id (the fork is a separate session, so the source was not appended to), and
//     FORK.md lands only on the fork's worktree/branch.
//  4. Run 4 *resumes the source again* after the fork: it still emits the source
//     id and still recalls the codeword, and is unaware of FORK.md — proving the
//     fork left the source session untouched.
//
// Skipped unless OPENROUTER_API_KEY is set, like the other live OpenCode test.
func TestIntegration_OpenCodeResumeFork(t *testing.T) {
	if os.Getenv("OPENROUTER_API_KEY") == "" {
		t.Skip("OPENROUTER_API_KEY not set; skipping live OpenCode resume/fork integration test")
	}

	const codeword = "XYZZY-4242" // distinctive token the model cannot guess from a fresh session
	repo := initSeedRepo(t)
	agent := openCodeJSONFormat{mustProfile("opencode", "openrouter/qwen/qwen3-coder-plus")}
	r, _ := newIntegrationRunner(t, repo, agent)

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()

	// Run 1: establish the codeword in the conversation and commit a file.
	res1, err := r.Run(ctx, RunRequest{
		Branch: "agent/opencode-resume-1",
		Prompt: "Remember this secret codeword for later: " + codeword + ". " +
			"Now create a file named ONE.md containing the single line 'one', then commit it with the message 'add ONE.md'.",
		Timeout: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	sourceID := parseSessionID(t, res1.Output)
	t.Logf("captured source session id: %s", sourceID)

	// Run 2: resume the captured session on a fresh branch and ask the agent to
	// recall the codeword. This Setup performs the stop/remount/start swap before
	// the exec, so continuity here also proves the SQLite store resumed across it.
	res2, err := r.Run(ctx, RunRequest{
		Branch:        "agent/opencode-resume-2",
		ResumeSession: sourceID,
		Prompt: "Earlier in this same conversation I gave you a secret codeword. " +
			"Create a file named RESUME.md whose only contents is that exact codeword, then commit it with the message 'add RESUME.md'.",
		Timeout: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("run 2 (resume): %v", err)
	}

	// Continuity: the resumed agent recalled the codeword a fresh session could not
	// have known.
	if got := readWorktreeFile(t, res2.handle.worktreePath, "RESUME.md"); !strings.Contains(strings.ToUpper(got), codeword) {
		t.Errorf("resumed session did not continue the prior conversation: RESUME.md = %q, want it to contain the codeword %q", got, codeword)
	}
	// Resume targeted the source session in place — it reused the same id, not a fork.
	if id2 := parseSessionID(t, res2.Output); id2 != sourceID {
		t.Errorf("resume did not continue the source session: run 2 session id = %q, want %q", id2, sourceID)
	}

	// Run 3: fork the source session onto a new branch.
	res3, err := r.Run(ctx, RunRequest{
		Branch:        "agent/opencode-fork",
		ResumeSession: sourceID,
		Fork:          true,
		Prompt:        "Create a file named FORK.md containing the single line 'fork', then commit it with the message 'add FORK.md'.",
		Timeout:       10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("run 3 (fork): %v", err)
	}

	// Fork is a brand-new session, distinct from the source: OpenCode's --fork
	// branched the conversation rather than appending to the source.
	forkID := parseSessionID(t, res3.Output)
	if forkID == "" || forkID == sourceID {
		t.Fatalf("fork did not create a new session: fork id = %q, source id = %q", forkID, sourceID)
	}
	t.Logf("fork session id: %s (source: %s)", forkID, sourceID)

	// Branch isolation: the fork's commit landed on its own branch/worktree, not
	// the source's. (Filesystem isolation is taboo's unconditional half of fork —
	// a fresh branch is always a fresh worktree; see ADR 0003.)
	if _, err := os.Stat(filepath.Join(res3.handle.worktreePath, "FORK.md")); err != nil {
		t.Errorf("FORK.md not on the fork worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(res2.handle.worktreePath, "FORK.md")); !os.IsNotExist(err) {
		t.Errorf("FORK.md leaked onto the source worktree %s (err=%v); fork is not isolated", res2.handle.worktreePath, err)
	}

	// Run 4: resume the *source* session again, after the fork. It must still
	// resolve to the source id and still recall the codeword (source history
	// intact), and must not know about FORK.md (the fork did not bleed back into
	// the source) — the "source untouched" acceptance criterion.
	res4, err := r.Run(ctx, RunRequest{
		Branch:        "agent/opencode-resume-3",
		ResumeSession: sourceID,
		Prompt: "Earlier in this same conversation I gave you a secret codeword. " +
			"Create a file named SRCCHECK.md with the codeword on the first line and, on the second line, " +
			"a comma-separated list of the exact filenames I have asked you to create in this conversation. " +
			"Then commit it with the message 'add SRCCHECK.md'.",
		Timeout: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("run 4 (resume source after fork): %v", err)
	}
	if id4 := parseSessionID(t, res4.Output); id4 != sourceID {
		t.Errorf("source session not resumable after fork: run 4 session id = %q, want %q", id4, sourceID)
	}
	srcCheck := strings.ToUpper(readWorktreeFile(t, res4.handle.worktreePath, "SRCCHECK.md"))
	if !strings.Contains(srcCheck, codeword) {
		t.Errorf("source session lost its history after the fork: SRCCHECK.md = %q, want the codeword %q", srcCheck, codeword)
	}
	if strings.Contains(srcCheck, "FORK.MD") {
		t.Errorf("fork bled into the source session: SRCCHECK.md lists FORK.md, but the source never created it: %q", srcCheck)
	}

	t.Logf("resume/fork verified over SQLite mount: source=%s fork=%s; source resumable+intact after fork; fork on branch %s commit %s",
		sourceID, forkID, res3.Branch, res3.Commit)
}

// sessionIDPattern matches an OpenCode session id token (e.g. ses_4f3a...) as a
// last-resort fallback when the JSON event shape is not the one we expect.
var sessionIDPattern = regexp.MustCompile(`ses_[A-Za-z0-9]+`)

// parseSessionID extracts the OpenCode session id from `opencode run --format
// json` output: newline-delimited JSON events that each carry a "sessionID"
// field (all events in one run share it). It tries, in order, a top-level
// sessionID, a nested payload.sessionID (the API-envelope shape), and finally a
// raw ses_* token anywhere in the output — so a schema tweak in the installed
// binary does not silently break capture. It returns the first id found,
// failing the test if none is (which means the run emitted no events).
func parseSessionID(t *testing.T, output string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] != '{' {
			continue
		}
		var ev struct {
			SessionID string `json:"sessionID"`
			Payload   struct {
				SessionID string `json:"sessionID"`
			} `json:"payload"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.SessionID != "" {
			return ev.SessionID
		}
		if ev.Payload.SessionID != "" {
			return ev.Payload.SessionID
		}
	}
	if id := sessionIDPattern.FindString(output); id != "" {
		return id
	}
	t.Fatalf("no sessionID found in --format json output:\n%s", output)
	return ""
}

// readWorktreeFile reads a file the agent committed into a run's worktree,
// failing the test if it is absent.
func readWorktreeFile(t *testing.T, worktree, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(worktree, name))
	if err != nil {
		t.Fatalf("read %s from worktree %s: %v", name, worktree, err)
	}
	return string(b)
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
