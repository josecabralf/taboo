package app

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/josecabralf/taboo"
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

// execRoot drives executeRoot — the seam the taboo binary maps every command
// through — with argv args (the invocation minus the binary name), returning
// the exit code and the captured stdout/stderr. Cobra reads os.Args inside
// ExecuteContext, so the helper swaps it for the call; tests using it must
// stay serial (no t.Parallel) to avoid racing on the swap.
func execRoot(t *testing.T, env Env, args ...string) (int, string, string) {
	t.Helper()
	oldArgs := os.Args
	os.Args = append([]string{"taboo"}, args...)
	defer func() { os.Args = oldArgs }()
	code := executeRoot(env)
	out, _ := env.Stdout.(*bytes.Buffer)
	errBuf, _ := env.Stderr.(*bytes.Buffer)
	if out == nil || errBuf == nil {
		t.Fatal("execRoot: env.Stdout and env.Stderr must be *bytes.Buffer")
	}
	return code, out.String(), errBuf.String()
}

// TestExecuteRoot_PrintsRefusalOnceToStderr is the flagship repro from #140:
// `taboo clean --prune-branches` in a project with no configured branch-prefix
// used to exit 1 with zero bytes on either stream. Through executeRoot the
// refusal is exactly one `Error:` line on stderr, stdout stays empty (JSON
// purity), and the exit code is 1.
func TestExecuteRoot_PrintsRefusalOnceToStderr(t *testing.T) {
	root := t.TempDir()
	// No defaults block: branch-prefix is unset, so --prune-branches must refuse.
	writeTabooProject(t, root, "workshop: demo\nbase: ubuntu@24.04\nagent: opencode\nmodel: anthropic/claude\nrepo: "+testRepoPath+"\n")
	env := configEnv(t, &fakeCommander{}, root, nil)

	code, stdout, stderr := execRoot(t, env, "clean", "--prune-branches")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	want := "Error: --prune-branches needs a configured branch-prefix; without one every branch would match\n"
	if stderr != want {
		t.Errorf("stderr = %q, want exactly %q", stderr, want)
	}
	if stdout != "" {
		t.Errorf("stdout must stay empty on an error path, got: %q", stdout)
	}
}

// TestExecuteRoot_JSONWithoutDryRunRefusal pins the other formerly-silent clean
// refusal: `clean --json` without `--dry-run` is one `Error:` line on stderr,
// an empty stdout, and exit 1.
func TestExecuteRoot_JSONWithoutDryRunRefusal(t *testing.T) {
	root := t.TempDir()
	writeTabooProject(t, root, cleanProjectBody)
	env := configEnv(t, &fakeCommander{}, root, nil)

	code, stdout, stderr := execRoot(t, env, "clean", "--json")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if want := "Error: --json requires --dry-run\n"; stderr != want {
		t.Errorf("stderr = %q, want exactly %q", stderr, want)
	}
	if stdout != "" {
		t.Errorf("stdout must stay empty on an error path, got: %q", stdout)
	}
}

// TestExecuteRoot_ParseErrorsPrintNoUsage asserts cobra's own errors ride the
// same seam: an unknown flag and an unknown subcommand each print one `Error:`
// line to stderr — SilenceUsage still suppresses the usage dump — and exit 1.
func TestExecuteRoot_ParseErrorsPrintNoUsage(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "unknown flag", args: []string{"clean", "--bogus"},
			want: "Error: unknown flag: --bogus\n"},
		{name: "unknown command", args: []string{"frobnicate"},
			want: "Error: unknown command \"frobnicate\" for \"taboo\"\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := configEnv(t, &fakeCommander{}, t.TempDir(), nil)
			code, stdout, stderr := execRoot(t, env, tt.args...)
			if code != 1 {
				t.Fatalf("exit code = %d, want 1", code)
			}
			if stderr != tt.want {
				t.Errorf("stderr = %q, want exactly %q (one line, no usage dump)", stderr, tt.want)
			}
			if strings.Contains(stdout, "Usage:") {
				t.Errorf("stdout must not carry a usage dump, got: %q", stdout)
			}
		})
	}
}

// TestExecuteRoot_DoctorSentinelTrailsReport pins the deliberate sentinel
// behavior: doctor with a failed check keeps its human report on stdout
// unchanged and gains exactly one trailing `Error:` line on stderr — the
// sentinel verdict — with exit 1. No "already reported" special-casing.
func TestExecuteRoot_DoctorSentinelTrailsReport(t *testing.T) {
	fake := &fakeCommander{
		stdoutFn: okHostStdout,
		errFn: func(c taboo.Cmd) error {
			if c.Name == "workshop" {
				return errors.New("exec: \"workshop\": executable file not found in $PATH")
			}
			return nil
		},
	}
	env := configEnv(t, fake, t.TempDir(), nil)

	code, stdout, stderr := execRoot(t, env, "doctor")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if got := findStatus(stdout, "workshop"); got != "error" {
		t.Errorf("stdout report: workshop status = %q, want error\nfull:\n%s", got, stdout)
	}
	if want := "Error: doctor: one or more checks failed\n"; stderr != want {
		t.Errorf("stderr = %q, want exactly the one trailing sentinel line %q", stderr, want)
	}
}

// TestExecuteRoot_SuccessIsQuiet asserts the happy path is untouched: a passing
// command exits 0 and executeRoot adds nothing to stderr.
func TestExecuteRoot_SuccessIsQuiet(t *testing.T) {
	env := configEnv(t, &fakeCommander{}, t.TempDir(), nil)
	code, stdout, stderr := execRoot(t, env, "version")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if stderr != "" {
		t.Errorf("stderr must stay empty on success, got: %q", stderr)
	}
	if !strings.HasPrefix(stdout, "taboo ") {
		t.Errorf("stdout = %q, want the version line", stdout)
	}
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
