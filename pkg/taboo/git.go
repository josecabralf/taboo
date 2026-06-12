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
