package run

import "github.com/josecabralf/taboo/internal/exec"

// isWorktreeMutation reports whether c invokes "git worktree add" or
// "git worktree remove" — the two operations that mutate the shared repo's
// worktree registry (and refs, on add) and so must not run concurrently across
// slots. Pool uses it to serialize both creation and disposal.
func isWorktreeMutation(c exec.Cmd) bool {
	if c.Name != "git" {
		return false
	}
	for i := 0; i+1 < len(c.Args); i++ {
		if c.Args[i] == "worktree" && (c.Args[i+1] == "add" || c.Args[i+1] == "remove") {
			return true
		}
	}
	return false
}
