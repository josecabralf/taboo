package taboo

import (
	"slices"
	"testing"
)

func TestWorktreeAddArgs(t *testing.T) {
	got := worktreeAddArgs("/home/dev/repos/myproject", "agent/feature", "/tmp/wt-1")
	want := []string{"-C", "/home/dev/repos/myproject", "worktree", "add", "-b", "agent/feature", "/tmp/wt-1"}
	if !slices.Equal(got, want) {
		t.Errorf("worktreeAddArgs =\n  %v\nwant\n  %v", got, want)
	}
}
