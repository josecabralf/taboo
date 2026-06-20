package app

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	taboo "github.com/josecabralf/taboo/pkg"
)

// seedSDKQuarantine lays out the in-project SDK quarantine the way slice 5
// materializes it: a real project-repo SDK dir (with a sentinel) that the
// quarantine SYMLINK points at, plus the seeded agent SDK as a REAL dir (also
// with a sentinel). It returns the symlink path and its target's sentinel so
// callers can assert the link is pruned while neither sentinel is touched.
func seedSDKQuarantine(t *testing.T, projectDir string) (link, targetSentinel, realDir, realSentinel string) {
	t.Helper()
	// The project's own .workshop/<x> that the quarantine link points at. It
	// lives outside projectDir so removing the link can never recurse into it.
	repoSDK := filepath.Join(t.TempDir(), ".workshop", "mylib")
	if err := os.MkdirAll(repoSDK, 0o750); err != nil {
		t.Fatalf("mkdir repo SDK: %v", err)
	}
	targetSentinel = filepath.Join(repoSDK, "keep.txt")
	if err := os.WriteFile(targetSentinel, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write target sentinel: %v", err)
	}

	quarantine := filepath.Join(projectDir, ".workshop")
	if err := os.MkdirAll(quarantine, 0o750); err != nil {
		t.Fatalf("mkdir quarantine: %v", err)
	}
	link = filepath.Join(quarantine, "mylib")
	if err := os.Symlink(repoSDK, link); err != nil {
		t.Fatalf("symlink quarantine SDK: %v", err)
	}

	// The seeded agent SDK is a real directory, not a link; it must survive.
	realDir = filepath.Join(quarantine, "opencode")
	if err := os.MkdirAll(realDir, 0o750); err != nil {
		t.Fatalf("mkdir seeded SDK: %v", err)
	}
	realSentinel = filepath.Join(realDir, "sentinel")
	if err := os.WriteFile(realSentinel, []byte("seeded"), 0o600); err != nil {
		t.Fatalf("write seeded sentinel: %v", err)
	}
	return link, targetSentinel, realDir, realSentinel
}

func exists(t *testing.T, p string) bool {
	t.Helper()
	_, err := os.Lstat(p)
	return err == nil
}

// TestClean_RemovesSdkLinksNotTargets is the data-loss guard: under the
// workshops scope, clean removes the quarantine SYMLINK only — it never recurses
// into the project's real .workshop/<x> the link points at, and it leaves the
// seeded agent SDK (a real dir) in place because it prunes by the symlink bit,
// not by directory membership. A DEFAULT clean (worktrees scope) leaves the link
// untouched, pinning the doWorkshops gate.
func TestClean_RemovesSdkLinksNotTargets(t *testing.T) {
	t.Parallel()

	t.Run("workshops scope removes the link, spares both targets", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeTabooProject(t, root, cleanProjectBody)
		projectDir := filepath.Join(root, ".taboo")
		link, targetSentinel, realDir, realSentinel := seedSDKQuarantine(t, projectDir)

		fake := &fakeCommander{stdoutFn: cleanFakeStdout(root)}
		env := configEnv(t, fake, root, nil)
		if _, _, err := cleanCmd(t, env, "--workshops", "--yes"); err != nil {
			t.Fatalf("clean --workshops --yes error = %v", err)
		}

		if exists(t, link) {
			t.Errorf("the quarantine SYMLINK %s must be removed", link)
		}
		// Link-safe: os.Remove never recursed into the link's target.
		if !exists(t, targetSentinel) {
			t.Errorf("the symlink TARGET %s must survive (removal must not recurse)", targetSentinel)
		}
		// Pruned by the symlink bit, not membership: the real seeded SDK stays.
		if !exists(t, realDir) {
			t.Errorf("the seeded agent SDK dir %s must survive (it is a real dir, not a link)", realDir)
		}
		if !exists(t, realSentinel) {
			t.Errorf("the seeded SDK's sentinel %s must survive", realSentinel)
		}
	})

	t.Run("default scope leaves the link in place", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeTabooProject(t, root, cleanProjectBody)
		projectDir := filepath.Join(root, ".taboo")
		link, _, _, _ := seedSDKQuarantine(t, projectDir)

		fake := &fakeCommander{stdoutFn: cleanFakeStdout(root)}
		env := configEnv(t, fake, root, nil)
		if _, _, err := cleanCmd(t, env, "--yes"); err != nil {
			t.Fatalf("default clean error = %v", err)
		}

		// The doWorkshops gate: a default (worktrees-only) clean never discovers
		// or removes the quarantine link.
		if !exists(t, link) {
			t.Errorf("a default clean must leave the quarantine SYMLINK %s in place", link)
		}
	})
}

// TestClean_DryRunListsSdkLinks asserts the --dry-run preview lists the
// quarantine link under an "SDK links" section and, being a dry run, removes
// nothing: the link survives on disk afterward.
func TestClean_DryRunListsSdkLinks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTabooProject(t, root, cleanProjectBody)
	projectDir := filepath.Join(root, ".taboo")
	link, _, _, _ := seedSDKQuarantine(t, projectDir)

	fake := &fakeCommander{stdoutFn: cleanFakeStdout(root)}
	env := configEnv(t, fake, root, nil)
	stdout, _, err := cleanCmd(t, env, "--workshops", "--dry-run")
	if err != nil {
		t.Fatalf("clean --workshops --dry-run error = %v", err)
	}

	if !strings.Contains(stdout, "SDK links") {
		t.Errorf("dry-run stdout missing the SDK links section:\n%s", stdout)
	}
	if !strings.Contains(stdout, link) {
		t.Errorf("dry-run plan missing the quarantine link %s:\n%s", link, stdout)
	}
	// A dry run mutates nothing: the link is still on disk.
	if !exists(t, link) {
		t.Errorf("--dry-run must remove nothing; link %s is gone", link)
	}
}

// cleanProjectBody is the taboo.yaml the clean tests build on. Its repo points
// at the shared testRepoPath fixture so the run preflight's repoLocationCheck
// passes. TestMain assigns it (and the matching assertions use testRepoPath)
// before any test runs; it must NOT be initialized at package scope because
// testRepoPath is empty until TestMain sets it.
var cleanProjectBody string

func buildCleanProjectBody(repo string) string {
	return "workshop: demo\nbase: ubuntu@24.04\nagent: opencode\nmodel: anthropic/claude\nrepo: " + repo + "\ndefaults:\n  branch-prefix: taboo/\n"
}

// cleanFakeStdout mirrors listFakeStdout: canned host stdout for the probes clean issues.
func cleanFakeStdout(root string) func(taboo.Cmd) string {
	return func(c taboo.Cmd) string {
		if c.Name == "workshop" && elemsContain(c.Args, "info") {
			return "name:     demo\nbase:     ubuntu@24.04\nstatus:   ready\nnotes:    --\n"
		}
		if c.Name == "git" && elemsContain(c.Args, "worktree", "list", "--porcelain") {
			managed := filepath.Join(root, ".taboo", "worktrees", "taboo-fix-123")
			return "worktree " + managed + "\nHEAD abc123\nbranch refs/heads/taboo/fix-123\n\n" +
				"worktree " + testRepoPath + "\nHEAD def456\nbranch refs/heads/main\n\n"
		}
		if c.Name == "git" && elemsContain(c.Args, "for-each-ref") {
			return "main\ntaboo/fix-123\ntaboo/refactor-456\ndevelop\n"
		}
		if c.Name == "git" && elemsContain(c.Args, "branch", "--merged") {
			return "  main\n  taboo/fix-123\n* develop\n"
		}
		return ""
	}
}

func cleanCmd(t *testing.T, env Env, args ...string) (string, string, error) {
	t.Helper()
	cmd := newCleanCmd(env)
	cmd.SetArgs(args)
	err := cmd.Execute()
	out, _ := env.Stdout.(*bytes.Buffer)
	errBuf, _ := env.Stderr.(*bytes.Buffer)
	if out == nil || errBuf == nil {
		t.Fatal("cleanCmd: env.Stdout and env.Stderr must be *bytes.Buffer")
	}
	return out.String(), errBuf.String(), err
}

func TestClean_DefaultRemovesWorktreesOnly(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTabooProject(t, root, cleanProjectBody)
	fake := &fakeCommander{stdoutFn: cleanFakeStdout(root)}
	env := configEnv(t, fake, root, nil)
	if _, _, err := cleanCmd(t, env); err != nil {
		t.Fatalf("clean error = %v, want nil", err)
	}
	managed := filepath.Join(root, ".taboo", "worktrees", "taboo-fix-123")
	if findInvocation(fake, "git", "-C", testRepoPath, "worktree", "remove", managed) == nil {
		t.Errorf("no git worktree remove for the managed worktree; calls: %v", invocations(fake))
	}
	if findInvocation(fake, "workshop", "remove") != nil {
		t.Errorf("default must not tear down workshops; calls: %v", invocations(fake))
	}
	if findInvocation(fake, "branch", "-D") != nil {
		t.Errorf("default must not delete branches; calls: %v", invocations(fake))
	}
	// The managed-remove call carries the repo path in its `-C <repo>` arg, so a
	// membership check would false-match; assert instead that no `worktree remove`
	// targets the main checkout as its removal path (the last positional arg).
	for _, inv := range invocations(fake) {
		if elemsContain(inv, "worktree", "remove") && inv[len(inv)-1] == testRepoPath {
			t.Errorf("must not remove the main checkout; calls: %v", invocations(fake))
		}
	}
}

func TestClean_WorkshopsTearsDownWorkshopsOnly(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTabooProject(t, root, cleanProjectBody)
	fake := &fakeCommander{stdoutFn: cleanFakeStdout(root)}
	env := configEnv(t, fake, root, nil)
	if _, _, err := cleanCmd(t, env, "--workshops"); err != nil {
		t.Fatalf("clean --workshops error = %v", err)
	}
	projectDir := filepath.Join(root, ".taboo")
	if findInvocation(fake, "workshop", "--project", projectDir, "remove", "demo-opencode") == nil {
		t.Errorf("no workshop teardown for demo-opencode; calls: %v", invocations(fake))
	}
	if findInvocation(fake, "worktree", "remove") != nil {
		t.Errorf("--workshops must not remove worktrees; calls: %v", invocations(fake))
	}
}

func TestClean_AllRemovesBoth(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTabooProject(t, root, cleanProjectBody)
	fake := &fakeCommander{stdoutFn: cleanFakeStdout(root)}
	env := configEnv(t, fake, root, nil)
	if _, _, err := cleanCmd(t, env, "--all"); err != nil {
		t.Fatalf("clean --all error = %v", err)
	}
	managed := filepath.Join(root, ".taboo", "worktrees", "taboo-fix-123")
	if findInvocation(fake, "git", "-C", testRepoPath, "worktree", "remove", managed) == nil {
		t.Errorf("--all must remove worktrees; calls: %v", invocations(fake))
	}
	projectDir := filepath.Join(root, ".taboo")
	if findInvocation(fake, "workshop", "--project", projectDir, "remove", "demo-opencode") == nil {
		t.Errorf("--all must tear down workshops; calls: %v", invocations(fake))
	}
}

func TestClean_SkipsUnprovisionedWorkshops(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTabooProject(t, root, cleanProjectBody)
	fake := &fakeCommander{stdoutFn: cleanFakeStdout(root), errFn: func(c taboo.Cmd) error {
		if c.Name == "workshop" && elemsContain(c.Args, "info") {
			return errors.New("no such workshop")
		}
		return nil
	}}
	env := configEnv(t, fake, root, nil)
	if _, _, err := cleanCmd(t, env, "--workshops"); err != nil {
		t.Fatalf("clean --workshops error = %v", err)
	}
	if findInvocation(fake, "workshop", "remove") != nil {
		t.Errorf("must not remove a not-provisioned workshop; calls: %v", invocations(fake))
	}
}

func TestClean_NoBranchPruneByDefault(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTabooProject(t, root, cleanProjectBody)
	fake := &fakeCommander{stdoutFn: cleanFakeStdout(root)}
	env := configEnv(t, fake, root, nil)
	if _, _, err := cleanCmd(t, env); err != nil {
		t.Fatalf("clean error = %v", err)
	}
	if findInvocation(fake, "branch", "-D") != nil {
		t.Errorf("default must not delete branches; calls: %v", invocations(fake))
	}
	if findInvocation(fake, "branch", "--merged") != nil {
		t.Errorf("default must not even probe merged branches; calls: %v", invocations(fake))
	}
}

func TestClean_PruneBranchesDeletesMerged(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTabooProject(t, root, cleanProjectBody)
	fake := &fakeCommander{stdoutFn: func(c taboo.Cmd) string {
		if c.Name == "git" && elemsContain(c.Args, "branch", "--merged") {
			return "  main\n  taboo/fix-123\n  taboo/refactor-456\n"
		}
		return cleanFakeStdout(root)(c)
	}}
	env := configEnv(t, fake, root, nil)
	if _, _, err := cleanCmd(t, env, "--prune-branches"); err != nil {
		t.Fatalf("clean --prune-branches error = %v", err)
	}
	for _, b := range []string{"taboo/fix-123", "taboo/refactor-456"} {
		if findInvocation(fake, "git", "-C", testRepoPath, "branch", "-D", b) == nil {
			t.Errorf("no branch -D for merged branch %s; calls: %v", b, invocations(fake))
		}
	}
}

func TestClean_RefusesUnmergedWithoutForce(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTabooProject(t, root, cleanProjectBody)
	fake := &fakeCommander{stdoutFn: cleanFakeStdout(root)}
	env := configEnv(t, fake, root, nil)
	_, _, err := cleanCmd(t, env, "--prune-branches")
	if err == nil {
		t.Fatal("an unmerged branch must make --prune-branches error")
	}
	if !strings.Contains(err.Error(), "taboo/refactor-456") || !strings.Contains(err.Error(), "--force") {
		t.Errorf("error %q must name the unmerged branch and --force", err)
	}
	if findInvocation(fake, "branch", "-D") != nil {
		t.Errorf("a refused prune must delete nothing; calls: %v", invocations(fake))
	}
	if findInvocation(fake, "worktree", "remove") != nil {
		t.Errorf("a refused prune must mutate nothing; calls: %v", invocations(fake))
	}
}

func TestClean_ForceDeletesUnmerged(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTabooProject(t, root, cleanProjectBody)
	fake := &fakeCommander{stdoutFn: cleanFakeStdout(root)}
	env := configEnv(t, fake, root, nil)
	if _, _, err := cleanCmd(t, env, "--prune-branches", "--force"); err != nil {
		t.Fatalf("clean --prune-branches --force error = %v", err)
	}
	if findInvocation(fake, "git", "-C", testRepoPath, "branch", "-D", "taboo/refactor-456") == nil {
		t.Errorf("--force must delete the unmerged branch; calls: %v", invocations(fake))
	}
}

func TestClean_EmptyPrefixRefusesPrune(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	body := "workshop: demo\nbase: ubuntu@24.04\nagent: opencode\nmodel: anthropic/claude\nrepo: " + testRepoPath + "\n"
	writeTabooProject(t, root, body)
	fake := &fakeCommander{stdoutFn: cleanFakeStdout(root)}
	env := configEnv(t, fake, root, nil)
	_, _, err := cleanCmd(t, env, "--prune-branches")
	if err == nil {
		t.Fatal("empty branch-prefix + --prune-branches must error")
	}
	if !strings.Contains(err.Error(), "branch-prefix") {
		t.Errorf("error %q should explain the missing branch-prefix", err)
	}
	if findInvocation(fake, "branch", "-D") != nil {
		t.Errorf("must delete nothing; calls: %v", invocations(fake))
	}
}

func TestClean_DryRunEmitsNothing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTabooProject(t, root, cleanProjectBody)
	fake := &fakeCommander{stdoutFn: cleanFakeStdout(root)}
	env := configEnv(t, fake, root, nil)
	stdout, _, err := cleanCmd(t, env, "--all", "--prune-branches", "--force", "--dry-run")
	if err != nil {
		t.Fatalf("clean --dry-run error = %v", err)
	}
	for _, verb := range [][]string{{"worktree", "remove"}, {"workshop", "remove"}, {"branch", "-D"}} {
		if findInvocation(fake, verb...) != nil {
			t.Errorf("--dry-run must mutate nothing, found %v; calls: %v", verb, invocations(fake))
		}
	}
	if !strings.Contains(stdout, "dry run") {
		t.Errorf("dry-run stdout missing header:\n%s", stdout)
	}
	managed := filepath.Join(root, ".taboo", "worktrees", "taboo-fix-123")
	if !strings.Contains(stdout, managed) {
		t.Errorf("dry-run plan missing the worktree path:\n%s", stdout)
	}
}

func TestClean_ConfirmDeclineAborts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTabooProject(t, root, cleanProjectBody)
	fake := &fakeCommander{stdoutFn: cleanFakeStdout(root)}
	env := configEnv(t, fake, root, nil)
	env.Interactive = func() bool { return true }
	env.Stdin = strings.NewReader("n\n")
	_, stderr, err := cleanCmd(t, env)
	if err != nil {
		t.Fatalf("declined clean error = %v, want nil", err)
	}
	if !strings.Contains(stderr, "Aborted.") {
		t.Errorf("stderr missing abort notice:\n%s", stderr)
	}
	if findInvocation(fake, "worktree", "remove") != nil {
		t.Errorf("a declined clean must mutate nothing; calls: %v", invocations(fake))
	}
}

// errReader always fails its Read with a non-EOF error, modeling a broken stdin.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("stdin broken") }

// TestClean_ConfirmReadErrorAborts locks confirmClean's read-error policy: a
// non-EOF stdin read error is treated as a decline, so the clean aborts cleanly
// (no error returned, "Aborted." on stderr) and mutates nothing.
func TestClean_ConfirmReadErrorAborts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTabooProject(t, root, cleanProjectBody)
	fake := &fakeCommander{stdoutFn: cleanFakeStdout(root)}
	env := configEnv(t, fake, root, nil)
	env.Interactive = func() bool { return true }
	env.Stdin = errReader{}
	_, stderr, err := cleanCmd(t, env)
	if err != nil {
		t.Fatalf("read-error clean error = %v, want nil", err)
	}
	if !strings.Contains(stderr, "Aborted.") {
		t.Errorf("stderr missing abort notice:\n%s", stderr)
	}
	if findInvocation(fake, "worktree", "remove") != nil {
		t.Errorf("a read-error clean must mutate nothing; calls: %v", invocations(fake))
	}
}

func TestClean_YesSkipsConfirm(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTabooProject(t, root, cleanProjectBody)
	fake := &fakeCommander{stdoutFn: cleanFakeStdout(root)}
	env := configEnv(t, fake, root, nil)
	env.Interactive = func() bool { return true }
	if _, _, err := cleanCmd(t, env, "--yes"); err != nil {
		t.Fatalf("clean --yes error = %v", err)
	}
	managed := filepath.Join(root, ".taboo", "worktrees", "taboo-fix-123")
	if findInvocation(fake, "worktree", "remove", managed) == nil {
		t.Errorf("--yes at a TTY must proceed; calls: %v", invocations(fake))
	}
}

func TestClean_NothingToClean(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTabooProject(t, root, cleanProjectBody)
	fake := &fakeCommander{stdoutFn: func(c taboo.Cmd) string {
		if c.Name == "git" && elemsContain(c.Args, "worktree", "list", "--porcelain") {
			return ""
		}
		return cleanFakeStdout(root)(c)
	}}
	env := configEnv(t, fake, root, nil)
	stdout, _, err := cleanCmd(t, env)
	if err != nil {
		t.Fatalf("clean error = %v", err)
	}
	if !strings.Contains(stdout, "Nothing to clean") {
		t.Errorf("stdout missing nothing-to-clean notice:\n%s", stdout)
	}
	if findInvocation(fake, "worktree", "remove") != nil {
		t.Errorf("nothing-to-clean must mutate nothing; calls: %v", invocations(fake))
	}
}

// TestClean_BestEffortContinuesOnFailure locks executeClean's best-effort
// contract: when one artifact's teardown fails, the remaining artifacts are
// still attempted, the failure is warned to stderr, and the joined error
// surfaces so the command exits non-zero. Here the worktree removal fails but
// the workshop teardown must still run.
func TestClean_BestEffortContinuesOnFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTabooProject(t, root, cleanProjectBody)
	fake := &fakeCommander{
		stdoutFn: cleanFakeStdout(root),
		errFn: func(c taboo.Cmd) error {
			if c.Name == "git" && elemsContain(c.Args, "worktree", "remove") {
				return errors.New("worktree is dirty")
			}
			return nil
		},
	}
	env := configEnv(t, fake, root, nil)

	_, stderr, err := cleanCmd(t, env, "--all")
	if err == nil {
		t.Fatal("a failed teardown must surface a non-nil error")
	}
	projectDir := filepath.Join(root, ".taboo")
	if findInvocation(fake, "workshop", "--project", projectDir, "remove", "demo-opencode") == nil {
		t.Errorf("teardown must continue past a failed worktree removal; calls: %v", invocations(fake))
	}
	if !strings.Contains(stderr, "warning") {
		t.Errorf("a failed removal must warn on stderr:\n%s", stderr)
	}
}

// TestClean_NonInteractiveProceedsWithoutPrompt locks the confirmation gate's
// other branch: a non-interactive caller (a pipe or CI, the default configEnv)
// proceeds without --yes and without ever printing the y/N prompt, so scripts
// are never blocked waiting on stdin.
func TestClean_NonInteractiveProceedsWithoutPrompt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTabooProject(t, root, cleanProjectBody)
	fake := &fakeCommander{stdoutFn: cleanFakeStdout(root)}
	env := configEnv(t, fake, root, nil) // Interactive nil ⇒ non-interactive.

	_, stderr, err := cleanCmd(t, env)
	if err != nil {
		t.Fatalf("clean error = %v", err)
	}
	managed := filepath.Join(root, ".taboo", "worktrees", "taboo-fix-123")
	if findInvocation(fake, "git", "-C", testRepoPath, "worktree", "remove", managed) == nil {
		t.Errorf("a non-interactive clean must proceed without --yes; calls: %v", invocations(fake))
	}
	if strings.Contains(stderr, "Continue?") {
		t.Errorf("a non-interactive clean must not prompt:\n%s", stderr)
	}
}
