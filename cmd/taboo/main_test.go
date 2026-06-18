package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testRepoPath is a real, persistent (non-/tmp) repo directory holding a
// workshop.yaml that the run tests derive the agent workshop from. Taboo's
// repoLocationCheck rejects repos under /tmp|/run (tmpfs), and t.TempDir() lives
// under /tmp, so the run tests cannot use it for the repo; this fixture sits on
// persistent storage instead. It is read-only for the tests (every git/worktree
// op is faked through the Commander), so one shared dir is safe.
var testRepoPath string

func TestMain(m *testing.M) {
	dir, cleanup := setupTestRepo()
	testRepoPath = dir
	runProjectBody = buildRunProjectBody(dir)
	cleanProjectBody = buildCleanProjectBody(dir)
	listProjectBody = buildListProjectBody(dir)
	emptyListingBody = buildEmptyListingBody(dir)
	code := m.Run()
	cleanup()
	os.Exit(code)
}

func setupTestRepo() (string, func()) {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		base, err = os.UserHomeDir()
	}
	if err != nil || base == "" {
		panic("test setup: no persistent base dir for the repo fixture")
	}
	dir, err := os.MkdirTemp(base, "taboo-run-test-")
	if err != nil {
		panic("test setup: create repo fixture: " + err.Error())
	}
	// Guard the load-bearing invariant: the repo must NOT be under tmpfs or the
	// run preflight's repoLocationCheck would reject it and mask real failures.
	clean := filepath.Clean(dir)
	for _, bad := range []string{"/tmp", "/run"} {
		if clean == bad || strings.HasPrefix(clean, bad+"/") {
			panic("test setup: repo fixture landed under " + bad + " (tmpfs); repoLocationCheck would reject it: " + dir)
		}
	}
	// The source definition taboo derives the agent workshop from.
	src := "name: demo\nbase: ubuntu@24.04\nsdks:\n  - name: go\n"
	if err := os.WriteFile(filepath.Join(dir, "workshop.yaml"), []byte(src), 0o600); err != nil {
		panic("test setup: write workshop.yaml: " + err.Error())
	}
	return dir, func() { _ = os.RemoveAll(dir) }
}
