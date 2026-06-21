package run

import "github.com/josecabralf/taboo/internal/exec"

// isWorktreeAdd reports whether c invokes "git worktree add".
// Pool uses it to serialize worktree creation across slots that share one repo.
func isWorktreeAdd(c exec.Cmd) bool {
	if c.Name != "git" {
		return false
	}
	for i := 0; i+1 < len(c.Args); i++ {
		if c.Args[i] == "worktree" && c.Args[i+1] == "add" {
			return true
		}
	}
	return false
}
