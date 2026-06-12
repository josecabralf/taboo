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
	// SDK is the name of the agent SDK that carries the mount plugs and bakes
	// in the agent CLI.
	SDK string
	// RepoPath is the absolute path to the host git repository whose worktrees
	// the agent operates on.
	RepoPath string
	// ProjectDir is the host directory taboo owns: it holds the rendered
	// workshop definition and is passed to every `workshop --project` call.
	ProjectDir string
	// AgentCmd is the agent CLI invocation, e.g.
	// {"opencode","run","-m","openrouter/qwen/qwen3-coder-plus"}. The run
	// prompt is appended as the final argument at exec time.
	AgentCmd []string
	// EnvKeys are host environment variable names whose values are passed into
	// the agent via `workshop exec --env NAME` (value never enters argv).
	EnvKeys []string
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
// shipped under .workshop/<name>/) in a definition's `sdks:` list. workshop
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
			Name: projectSDKRef(cfg.SDK),
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
