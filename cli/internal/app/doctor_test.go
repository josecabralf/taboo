package app

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/josecabralf/taboo"
)

// invocations renders each recorded Commander call as a [name, args...] slice so
// tests can assert the exact external commands doctor emitted at the seam.
func invocations(f *fakeCommander) [][]string {
	out := make([][]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, append([]string{c.Name}, c.Args...))
	}
	return out
}

// fakeCommander is the CLI's test double for taboo.Commander. It records
// every invocation and is programmed per-call: errFn decides whether a matched
// command fails, and stdoutFn supplies the stdout the probe parses. It mirrors
// the prior-art fake in pkg/taboo/runner_test.go but is owned by this package
// because the library's fake is unexported.
type fakeCommander struct {
	calls    []taboo.Cmd
	errFn    func(c taboo.Cmd) error
	stdoutFn func(c taboo.Cmd) string
}

func (f *fakeCommander) Run(_ context.Context, c taboo.Cmd) error {
	f.calls = append(f.calls, c)
	if f.stdoutFn != nil && c.Stdout != nil {
		if s := f.stdoutFn(c); s != "" {
			_, _ = c.Stdout.Write([]byte(s))
		}
	}
	if f.errFn != nil {
		return f.errFn(c)
	}
	return nil
}

// okHostStdout is the stdout program that makes every host probe succeed: a
// workshop version at the floor and harmless version banners for the rest.
func okHostStdout(c taboo.Cmd) string {
	if c.Name == "workshop" {
		return "workshop version 1.8.0\n"
	}
	return c.Name + " version 1.0.0\n"
}

// runDoctor executes a freshly built doctor command with the given env and
// extra flags, returning the captured stdout and the execute error.
func runDoctor(t *testing.T, env Env, args ...string) (string, error) {
	t.Helper()
	cmd := newDoctorCmd(env)
	cmd.SetArgs(args)
	err := cmd.Execute()
	out, _ := env.Stdout.(*bytes.Buffer)
	if out == nil {
		t.Fatal("runDoctor: env.Stdout must be a *bytes.Buffer")
	}
	return out.String(), err
}

// findStatus returns the status token for the named check in human-readable
// output, or "" if the check is absent. It parses the report's "[status] name
// message" column layout (writeHuman in report.go): it pins the status COLUMN
// and matches the check-name field exactly, so a status word inside a message —
// or a temp-dir path that happens to contain the check name — cannot masquerade
// as the status (a substring scan would let it).
func findStatus(out, name string) string {
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "[") {
			continue
		}
		end := strings.IndexByte(trimmed, ']')
		if end < 0 {
			continue
		}
		if fields := strings.Fields(trimmed[end+1:]); len(fields) > 0 && fields[0] == name {
			return strings.TrimSpace(trimmed[1:end])
		}
	}
	return ""
}

// TestDoctor_AllHostChecksOK is the tracer bullet: with every host probe
// programmed to succeed, doctor returns no error and reports every host check
// ok.
func TestDoctor_AllHostChecksOK(t *testing.T) {
	t.Parallel()
	fake := &fakeCommander{stdoutFn: okHostStdout}
	env := Env{
		Cmd:       fake,
		Stdin:     strings.NewReader(""),
		Stdout:    &bytes.Buffer{},
		Stderr:    &bytes.Buffer{},
		LookupEnv: func(string) (string, bool) { return "", false },
		Getwd:     func() (string, error) { return t.TempDir(), nil },
	}
	out, err := runDoctor(t, env)
	if err != nil {
		t.Fatalf("doctor error = %v, want nil", err)
	}
	for _, name := range []string{"workshop", "lxd", "lxd-reachable", "git", "go"} {
		if got := findStatus(out, name); got != "ok" {
			t.Errorf("check %q status = %q, want %q\nfull output:\n%s", name, got, "ok", out)
		}
	}
}

// TestDoctor_WorkshopProbeFails asserts that when `workshop --version` errors,
// doctor returns the sentinel failure error and the workshop check is error.
func TestDoctor_WorkshopProbeFails(t *testing.T) {
	t.Parallel()
	fake := &fakeCommander{
		stdoutFn: okHostStdout,
		errFn: func(c taboo.Cmd) error {
			if c.Name == "workshop" {
				return errors.New("exec: \"workshop\": executable file not found in $PATH")
			}
			return nil
		},
	}
	env := Env{
		Cmd:       fake,
		Stdin:     strings.NewReader(""),
		Stdout:    &bytes.Buffer{},
		Stderr:    &bytes.Buffer{},
		LookupEnv: func(string) (string, bool) { return "", false },
		Getwd:     func() (string, error) { return t.TempDir(), nil },
	}
	out, err := runDoctor(t, env)
	if !errors.Is(err, errChecksFailed) {
		t.Fatalf("doctor error = %v, want errChecksFailed", err)
	}
	if got := findStatus(out, "workshop"); got != "error" {
		t.Errorf("workshop status = %q, want %q\nfull output:\n%s", got, "error", out)
	}
}

// TestHostChecks_ProbeInvocations pins the exact external commands the host
// checks emit, in order, at the Commander seam — so a regression that probes the
// wrong tool or flag (e.g. `lxc list` instead of `lxc info`) is caught even when
// the rendered status is unchanged.
func TestHostChecks_ProbeInvocations(t *testing.T) {
	t.Parallel()
	fake := &fakeCommander{stdoutFn: okHostStdout}
	env := Env{
		Cmd:       fake,
		Stdin:     strings.NewReader(""),
		Stdout:    &bytes.Buffer{},
		Stderr:    &bytes.Buffer{},
		LookupEnv: func(string) (string, bool) { return "", false },
		Getwd:     func() (string, error) { return t.TempDir(), nil },
	}
	if _, err := runDoctor(t, env); err != nil {
		t.Fatalf("doctor error = %v, want nil", err)
	}
	want := [][]string{
		{"workshop", "--version"},
		{"lxc", "version"},
		{"lxc", "info"},
		{"git", "--version"},
		{"go", "version"},
	}
	got := invocations(fake)
	if len(got) < len(want) {
		t.Fatalf("recorded %d host calls, want at least %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if !slices.Equal(got[i], w) {
			t.Errorf("host call %d = %v, want %v", i, got[i], w)
		}
	}
}
