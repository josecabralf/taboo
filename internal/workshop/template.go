package workshop

import (
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
}

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
