package exec

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// fakeCommander is a stand-in Commander: it writes a canned string to the
// supplied Stdout and records the Cmd it was handed.
type fakeCommander struct {
	stdout string
	err    error
	gotCmd Cmd
}

func (f *fakeCommander) Run(_ context.Context, c Cmd) error {
	f.gotCmd = c
	if f.stdout != "" {
		_, _ = io.WriteString(c.Stdout, f.stdout)
	}
	return f.err
}

func TestOutput_ReturnsRawCapturedStdout(t *testing.T) {
	f := &fakeCommander{stdout: "  hi\n"}
	got, err := Output(context.Background(), f, Cmd{Name: "git", Args: []string{"rev-parse"}})
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if got != "  hi\n" {
		t.Errorf("Output = %q, want %q (untrimmed)", got, "  hi\n")
	}
}

func TestOutput_PassesThroughRunError(t *testing.T) {
	wantErr := errors.New("boom")
	f := &fakeCommander{stdout: "partial", err: wantErr}
	got, err := Output(context.Background(), f, Cmd{Name: "git"})
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
	if got != "partial" {
		t.Errorf("Output = %q, want %q (captured-so-far returned)", got, "partial")
	}
}

func TestOutput_ForwardsCmdNameAndArgs(t *testing.T) {
	f := &fakeCommander{}
	if _, err := Output(context.Background(), f, Cmd{Name: "git", Args: []string{"status", "-s"}}); err != nil {
		t.Fatalf("Output: %v", err)
	}
	if f.gotCmd.Name != "git" {
		t.Errorf("Name = %q, want %q", f.gotCmd.Name, "git")
	}
	if strings.Join(f.gotCmd.Args, " ") != "status -s" {
		t.Errorf("Args = %v, want [status -s]", f.gotCmd.Args)
	}
}

func TestExecCommander_CapturesStdout(t *testing.T) {
	var out strings.Builder
	err := NewExecCommander().Run(context.Background(), Cmd{
		Name:   "git",
		Args:   []string{"--version"},
		Stdout: &out,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "git version") {
		t.Errorf("stdout = %q, want it to contain 'git version'", out.String())
	}
}

func TestExecCommander_ReturnsErrorForMissingBinary(t *testing.T) {
	err := NewExecCommander().Run(context.Background(), Cmd{Name: "definitely-not-a-real-binary-xyz"})
	if err == nil {
		t.Fatal("expected error running a missing binary, got nil")
	}
}
