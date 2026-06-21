package exec

import (
	"context"
	"strings"
	"testing"
)

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
