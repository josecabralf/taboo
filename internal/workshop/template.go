package workshop

import (
	"fmt"
	"path/filepath"

	"github.com/josecabralf/taboo/internal/agent"
)

// Config describes a taboo-managed workshop and the agent that runs inside it.
type Config struct {
	// Workshop is the workshop name (the definition's `name:`).
	Workshop string
	// Base is the workshop base image, e.g. "ubuntu@24.04".
	Base string
	// Agent is the agent profile run inside the workshop: it names the SDK that
	// carries the mount plugs and bakes in the agent CLI, builds the run
	// invocation, and lists the credential env keys the agent needs.
	Agent agent.AgentProfile
	// RepoPath is the absolute path to the host git repository whose worktrees
	// the agent operates on.
	RepoPath string
	// ProjectDir is the host directory taboo owns: it holds the rendered
	// workshop definition and is passed to every `workshop --project` call.
	ProjectDir string
	// SourceDefinition is the selected source-definition name; empty means
	// auto-resolve the project's single workshop definition.
	SourceDefinition string
	// Strategy selects the workspace seam: omitted or "worktree" uses a linked
	// worktree path, "branch" operates in place on the checkout, and any other
	// value is rejected by Validate (a closed enum, not a fall-through). See
	// CONTEXT.md.
	Strategy BranchingStrategy
}

// BranchingStrategy is a named string for the workspace seam — named for godoc
// grouping and named-value safety; Validate guards typos.
type BranchingStrategy string

// Validate rejects an unrecognized strategy so a typo fails loudly instead of
// silently selecting a path. Empty is valid: it means the worktree default
// (config.LoadConfig likewise defaults an omitted strategy to worktree).
func (s BranchingStrategy) Validate() error {
	switch s {
	case "", StrategyBranch, StrategyWorktree:
		return nil
	default:
		return fmt.Errorf("unknown strategy %q: want %q or %q", s, StrategyBranch, StrategyWorktree)
	}
}

// StrategyBranch and StrategyWorktree are the recognized Config.Strategy values.
// They live here because this package owns the field, the single source of truth
// for the run and config packages. See CONTEXT.md.
const (
	StrategyBranch   BranchingStrategy = "branch"
	StrategyWorktree BranchingStrategy = "worktree"
)

type sdkDef struct {
	Name  string          `yaml:"name"`
	Plugs map[string]plug `yaml:"plugs,omitempty"`
}

type plug struct {
	Interface      string `yaml:"interface"`
	WorkshopTarget string `yaml:"workshop-target"`
	ReadOnly       bool   `yaml:"read-only,omitempty"`
}

// WorkspaceTarget and SessionsTarget are taboo's two RELOCATABLE mount targets.
// Per ADR 0009 they live under a reserved `/taboo/...` prefix so they cannot
// collide with the project's own mounts in the shared in-workshop namespace.
// (git-common, by contrast, must stay at the host .git absolute path — its path
// IS the mechanism, see GitCommonTarget.)
const WorkspaceTarget = "/taboo/workspace"

// SessionsTarget is the in-workshop mount target for the host sessions directory.
// A session-capable agent's session-dir env var (AgentProfile.Sessions().DirEnv)
// is pointed here at exec time so session files write through to the host and
// survive the per-run stop/remount/start swap, which wipes the rootfs.
const SessionsTarget = "/taboo/sessions"

// projectSDKRef returns the name used to reference an in-project SDK (one
// shipped under .workshop/<name>/) in a definition's `sdks:` list. Workshop
// resolves "project-<name>" to the bare "<name>" used for remount qualifiers
// and `info` output.
func projectSDKRef(name string) string { return "project-" + name }

// GitCommonTarget is the in-workshop mount target for the repo's main .git.
// It must equal the host .git absolute path so a linked worktree's .git
// pointer resolves identically inside and outside the workshop (the two-mount
// rule; see CONTEXT.md).
func GitCommonTarget(repoPath string) string {
	return filepath.Join(repoPath, ".git")
}

// WorktreesCommonTarget is the in-workshop mount target for the parent directory
// that holds every run's worktree (<ProjectDir>/worktrees, where <ProjectDir> is
// <repo>/.taboo; see Runner.worktreePath). Like GitCommonTarget it must equal its
// host absolute path: a linked worktree's admin dir (<repo>/.git/worktrees/<name>)
// records a *back-pointer* to the worktree's host path
// (<ProjectDir>/worktrees/<name>/.git), and git treats the
// worktree as stale/prunable — and will delete the admin dir on the next
// `git worktree prune` — unless that back-pointer resolves. With only the
// worktree (at the relocated /taboo/workspace) and .git mounted, the back-pointer
// path is invisible in the workshop, so an in-workshop prune destroys the admin
// dir on the host too (it is the same bind-mounted .git). Mounting the worktrees
// parent at its identical host path makes the back-pointer resolve on both sides
// for every branch, with no per-run target change. This is the third mount of the
// (formerly two-) mount rule; see CONTEXT.md and docs/adr/0011.
func WorktreesCommonTarget(projectDir string) string {
	return filepath.Join(projectDir, "worktrees")
}

// GitMount is one extra (plug, target) mount a strategy layers on top of the
// workspace. Target is both the plug's workshop-target and the remount source
// (an identical-path mount; see CONTEXT.md).
type GitMount struct{ Plug, Target string }

// StrategyGitMounts returns the extra git mounts a strategy needs beyond the
// single workspace mount. The branch strategy is self-contained and needs none;
// the worktree strategy needs gitcommon + worktrees (in that order).
func StrategyGitMounts(cfg Config) []GitMount {
	if cfg.Strategy == StrategyBranch {
		return nil
	}
	return []GitMount{
		{Plug: "gitcommon", Target: GitCommonTarget(cfg.RepoPath)},
		{Plug: "worktrees", Target: WorktreesCommonTarget(cfg.ProjectDir)},
	}
}
