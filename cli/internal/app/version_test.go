package app

import (
	"bytes"
	"runtime/debug"
	"strings"
	"testing"
)

// versionCmd builds a version command with env, runs it with args, and returns
// the captured stdout and the execute error. It mirrors listCmd.
func versionCmd(t *testing.T, env Env, args ...string) (string, error) {
	t.Helper()
	cmd := newVersionCmd(env)
	cmd.SetArgs(args)
	err := cmd.Execute()
	out, _ := env.Stdout.(*bytes.Buffer)
	if out == nil {
		t.Fatal("versionCmd: env.Stdout must be *bytes.Buffer")
	}
	return out.String(), err
}

// stubBuildInfo overrides the readBuildInfo seam for the duration of the test,
// restoring the real implementation on cleanup, so the version command's output
// is driven independently of how the test binary was built.
func stubBuildInfo(t *testing.T, info *debug.BuildInfo, ok bool) {
	t.Helper()
	orig := readBuildInfo
	t.Cleanup(func() { readBuildInfo = orig })
	readBuildInfo = func() (*debug.BuildInfo, bool) { return info, ok }
}

// TestVersion_PrintsModuleVersion locks the happy path: when build info carries
// a main-module version, the command prints "taboo <version>" to stdout.
func TestVersion_PrintsModuleVersion(t *testing.T) {
	stubBuildInfo(t, &debug.BuildInfo{Main: debug.Module{Version: "v0.1.2"}}, true)
	env := Env{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}

	stdout, err := versionCmd(t, env)
	if err != nil {
		t.Fatalf("version error = %v, want nil", err)
	}
	if got := strings.TrimSpace(stdout); got != "taboo v0.1.2" {
		t.Errorf("stdout = %q, want %q", got, "taboo v0.1.2")
	}
}

// TestVersion_UnknownWhenNoBuildInfo locks the fallback: when build info is
// unavailable, the command still succeeds and reports "unknown" rather than
// crashing or printing an empty version.
func TestVersion_UnknownWhenNoBuildInfo(t *testing.T) {
	stubBuildInfo(t, nil, false)
	env := Env{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}

	stdout, err := versionCmd(t, env)
	if err != nil {
		t.Fatalf("version error = %v, want nil", err)
	}
	if got := strings.TrimSpace(stdout); got != "taboo unknown" {
		t.Errorf("stdout = %q, want %q", got, "taboo unknown")
	}
}

// TestVersion_UnknownWhenEmptyVersion locks the empty-version branch: build info
// present but with no main-module version (a plain `go build` can leave it
// blank) also falls back to "unknown".
func TestVersion_UnknownWhenEmptyVersion(t *testing.T) {
	stubBuildInfo(t, &debug.BuildInfo{Main: debug.Module{Version: ""}}, true)
	env := Env{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}

	stdout, err := versionCmd(t, env)
	if err != nil {
		t.Fatalf("version error = %v, want nil", err)
	}
	if got := strings.TrimSpace(stdout); got != "taboo unknown" {
		t.Errorf("stdout = %q, want %q", got, "taboo unknown")
	}
}

// TestVersion_RejectsArgs locks the cobra.NoArgs contract: version takes no
// positional arguments and errors when given one.
func TestVersion_RejectsArgs(t *testing.T) {
	stubBuildInfo(t, &debug.BuildInfo{Main: debug.Module{Version: "v0.1.2"}}, true)
	env := Env{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}

	if _, err := versionCmd(t, env, "extra"); err == nil {
		t.Fatal("version with a positional arg = nil error, want a NoArgs rejection")
	}
}
