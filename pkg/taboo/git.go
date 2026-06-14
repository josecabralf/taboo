package taboo

// worktreeAddArgs builds the `git` arguments to create a new linked worktree on
// a fresh branch:  git -C <repo> worktree add -b <branch> <path>.
func worktreeAddArgs(repoPath, branch, wtPath string) []string {
	return []string{"-C", repoPath, "worktree", "add", "-b", branch, wtPath}
}

// revParseHeadArgs builds `git -C <dir> rev-parse HEAD`.
func revParseHeadArgs(dir string) []string {
	return []string{"-C", dir, "rev-parse", "HEAD"}
}

// isWorktreeAdd reports whether c invokes "git worktree add".
// Pool uses it to serialize worktree creation across slots that share one repo.
func isWorktreeAdd(c Cmd) bool {
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
