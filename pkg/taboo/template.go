package taboo

import (
	"path/filepath"
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
	Agent AgentProfile
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

// workspaceTarget and sessionsTarget are taboo's two RELOCATABLE mount targets.
// Per ADR 0009 they live under a reserved `/taboo/...` prefix so they cannot
// collide with the project's own mounts in the shared in-workshop namespace.
// (git-common, by contrast, must stay at the host .git absolute path — its path
// IS the mechanism, see gitCommonTarget.)
const workspaceTarget = "/taboo/workspace"

// sessionsTarget is the in-workshop mount target for the host sessions directory.
// A session-capable agent's session-dir env var (AgentProfile.Sessions().DirEnv)
// is pointed here at exec time so session files write through to the host and
// survive the per-run stop/remount/start swap, which wipes the rootfs.
const sessionsTarget = "/taboo/sessions"

// projectSDKRef returns the name used to reference an in-project SDK (one
// shipped under .workshop/<name>/) in a definition's `sdks:` list. Workshop
// resolves "project-<name>" to the bare "<name>" used for remount qualifiers
// and `info` output.
func projectSDKRef(name string) string { return "project-" + name }

// gitCommonTarget is the in-workshop mount target for the repo's main .git.
// It must equal the host .git absolute path so a linked worktree's .git
// pointer resolves identically inside and outside the workshop (the two-mount
// rule; see CONTEXT.md).
func gitCommonTarget(repoPath string) string {
	return filepath.Join(repoPath, ".git")
}
