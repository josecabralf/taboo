package taboo

import (
	"path/filepath"

	"gopkg.in/yaml.v3"
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
}

// definition is the on-disk workshop definition taboo renders and owns.
type definition struct {
	Name string   `yaml:"name"`
	Base string   `yaml:"base"`
	SDKs []sdkDef `yaml:"sdks"`
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

const workspaceTarget = "/workspace"

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

// renderDefinition produces the workshop definition YAML for cfg.
func renderDefinition(cfg Config) (string, error) {
	def := definition{
		Name: cfg.Workshop,
		Base: cfg.Base,
		SDKs: []sdkDef{{
			Name: projectSDKRef(cfg.Agent.Name()),
			Plugs: map[string]plug{
				"workspace": {Interface: "mount", WorkshopTarget: workspaceTarget},
				"gitcommon": {Interface: "mount", WorkshopTarget: gitCommonTarget(cfg.RepoPath)},
			},
		}},
	}
	out, err := yaml.Marshal(def)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
