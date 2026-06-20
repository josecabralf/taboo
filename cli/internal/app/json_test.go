package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// decodeJSONReport parses out as the doctor --json document, failing the test on
// invalid JSON.
func decodeJSONReport(t *testing.T, out string) jsonReport {
	t.Helper()
	var rep jsonReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\nraw:\n%s", err, out)
	}
	return rep
}

// TestDoctor_JSONAllOK asserts --json on a healthy host emits valid JSON with
// ok=true, every check status ok, and no execute error.
func TestDoctor_JSONAllOK(t *testing.T) {
	t.Parallel()
	fake := &fakeCommander{stdoutFn: okHostStdout}
	env := newHostEnv(t, fake)
	out, err := runDoctor(t, env, "--json")
	if err != nil {
		t.Fatalf("doctor --json error = %v, want nil", err)
	}
	rep := decodeJSONReport(t, out)
	if !rep.OK {
		t.Errorf("rep.OK = false, want true\n%s", out)
	}
	if len(rep.Checks) == 0 {
		t.Fatal("rep.Checks empty, want host checks")
	}
	for _, c := range rep.Checks {
		if c.Status != "ok" {
			t.Errorf("check %q status = %q, want ok", c.Name, c.Status)
		}
	}
}

// TestDoctor_JSONWithError asserts --json reports ok=false, marks the failing
// check error, and the command still returns the failure sentinel so the
// process exits non-zero.
func TestDoctor_JSONWithError(t *testing.T) {
	t.Parallel()
	fake := &fakeCommander{stdoutFn: okHostStdout, errFn: failOn("git")}
	env := newHostEnv(t, fake)
	out, err := runDoctor(t, env, "--json")
	if !errors.Is(err, errChecksFailed) {
		t.Fatalf("doctor --json error = %v, want errChecksFailed", err)
	}
	rep := decodeJSONReport(t, out)
	if rep.OK {
		t.Errorf("rep.OK = true, want false (git errored)\n%s", out)
	}
	var gitStatus string
	for _, c := range rep.Checks {
		if c.Name == "git" {
			gitStatus = c.Status
		}
	}
	if gitStatus != "error" {
		t.Errorf("git status = %q, want error", gitStatus)
	}
}

// TestRootCmd_WiresDoctor sanity-checks the root command exposes doctor and runs
// it through Execute, exercising the production wiring path (newRootCmd) with a
// fake Commander.
func TestRootCmd_WiresDoctor(t *testing.T) {
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
	root := newRootCmd(env)
	root.SetArgs([]string{"doctor"})
	if err := root.Execute(); err != nil {
		t.Fatalf("root doctor error = %v, want nil", err)
	}
	out, _ := env.Stdout.(*bytes.Buffer)
	if !strings.Contains(out.String(), "host readiness") {
		t.Errorf("root doctor output missing report header:\n%s", out.String())
	}
}
