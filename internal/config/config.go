// Package config parses taboo.yaml, the single source of truth read by the CLI
// and by Go callers driving runs through pkg, and resolves it plus a workflow and
// overrides into a *run.Plan.
//
// The edge to run is forced by a Go mechanic: (*ProjectConfig).Plan is a method
// and Go forbids methods on a non-local type, so the resolver must live here; its
// *run.Plan return and run.PlanOverrides param pull in run. run does not import
// config, so the DAG stays acyclic.
package config

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/josecabralf/taboo/internal/agent"
	"github.com/josecabralf/taboo/internal/workshop"
)

// Duration is a config-friendly time.Duration that (un)marshals Go duration
// strings like "30m" in YAML.
type Duration time.Duration

// UnmarshalYAML parses a Go duration string; an empty value yields zero.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	if s == "" {
		*d = 0
		return nil
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}

// MarshalYAML renders the duration as a Go duration string.
func (d Duration) MarshalYAML() (any, error) {
	return time.Duration(d).String(), nil
}

// ProjectConfig is the parsed taboo.yaml.
type ProjectConfig struct {
	// Workshop is the workshop name taboo provisions runs in.
	Workshop string `yaml:"workshop"`
	// Base is the workshop base image, e.g. "ubuntu@24.04".
	Base string `yaml:"base"`
	// Repo is the host git repository path whose worktrees the agent operates on.
	Repo string `yaml:"repo"`
	// Agent is the default agent name, resolved against the registry.
	Agent agent.AgentName `yaml:"agent"`
	// Model is the default model passed to the resolved agent.
	Model string `yaml:"model"`
	// Strategy is the workspace seam, a closed set: "worktree" (default, a per-run
	// linked worktree) or "branch" (in place on the checkout). Any other value is
	// rejected at load.
	Strategy workshop.BranchingStrategy `yaml:"strategy,omitempty"`
	// SourceDefinition names the workshop definition to derive from when several
	// exist; empty selects the sole definition.
	SourceDefinition string `yaml:"source-definition,omitempty"`
	// Defaults holds the scalar run settings applied when a workflow or flag does
	// not override them; nil when the block is omitted.
	Defaults *RunDefaults `yaml:"defaults,omitempty"`
	// Workflows are the named, reusable task types keyed by workflow name.
	Workflows map[string]Workflow `yaml:"workflows,omitempty"`
	// DefaultWorkflow names the workflow run when the CLI selects none.
	DefaultWorkflow string `yaml:"default-workflow,omitempty"`
	// Profile is the resolved top-level profile; nil when no agent is set. Not
	// serialized.
	Profile agent.AgentProfile `yaml:"-"`
}

// RunDefaults are scalar run settings applied when a workflow or flag does not
// override them. Both prompt and prompt-file exist here and at the workflow level
// to mirror the CLI's --prompt / --prompt-file flags.
type RunDefaults struct {
	// BranchPrefix is the prefix for branches taboo creates for a run.
	BranchPrefix string `yaml:"branch-prefix,omitempty"`
	// Prompt is the inline default instruction for a run.
	Prompt string `yaml:"prompt,omitempty"`
	// PromptFile is a path to a file whose contents are the run instruction.
	PromptFile string `yaml:"prompt-file,omitempty"`
	// Timeout bounds a single agent invocation, e.g. "30m".
	Timeout Duration `yaml:"timeout,omitempty"`
	// MaxIterations caps how many times the agent is re-run for a single task.
	MaxIterations int `yaml:"max-iterations,omitempty"`
	// CompletionSignal is the string whose appearance in agent output ends the
	// run early.
	CompletionSignal string `yaml:"completion-signal,omitempty"`
	// StopOnNoChange stops a looped run when an iteration produces no new commit.
	// Enable-only: any layer can turn it on, none can turn it off.
	StopOnNoChange bool `yaml:"stop-on-no-change,omitempty"`
}

// Workflow is a named, reusable task type that overrides scalar run params.
type Workflow struct {
	// Prompt is the inline instruction for this workflow.
	Prompt string `yaml:"prompt,omitempty"`
	// PromptFile is a path to a file whose contents are the instruction.
	PromptFile string `yaml:"prompt-file,omitempty"`
	// Model overrides the top-level model for this workflow.
	Model string `yaml:"model,omitempty"`
	// Agent overrides the top-level agent for this workflow.
	Agent agent.AgentName `yaml:"agent,omitempty"`
	// MaxIterations overrides the default iteration cap for this workflow.
	MaxIterations int `yaml:"max-iterations,omitempty"`
	// Timeout overrides the default per-invocation timeout, e.g. "30m".
	Timeout Duration `yaml:"timeout,omitempty"`
	// CompletionSignal overrides the default loop-stop sentinel for this workflow.
	CompletionSignal string `yaml:"completion-signal,omitempty"`
	// StopOnNoChange stops a looped run of this workflow when an iteration produces
	// no new commit. Enable-only: it cannot turn off a defaults-level enable.
	StopOnNoChange bool `yaml:"stop-on-no-change,omitempty"`
	// Profile is the resolved effective profile (workflow agent/model, falling back
	// to the top level). Not serialized.
	Profile agent.AgentProfile `yaml:"-"`
}

// ErrConfigRead is the sentinel LoadConfig wraps when the config file cannot be
// read (e.g. missing path).
var ErrConfigRead = errors.New("taboo: cannot read config")

// ErrConfigParse is the sentinel LoadConfig wraps on a malformed, unknown-field,
// or otherwise invalid config document.
var ErrConfigParse = errors.New("taboo: invalid config")

// defaultStrategy applies when the config omits one: the worktree strategy, the
// collision-safe path (each run gets its own branch + worktree) matching what
// Setup does for an unset strategy.
const defaultStrategy = workshop.StrategyWorktree

// LoadConfig reads and parses taboo.yaml at path, resolves every agent/model to
// an AgentProfile, and returns the config.
func LoadConfig(path string) (*ProjectConfig, error) {
	// The config path comes from a trusted CLI invocation, not end-user input.
	data, err := os.ReadFile(path) // #nosec G304
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrConfigRead, err)
	}
	cfg, err := decodeStrict(path, data)
	if err != nil {
		return nil, err
	}
	if cfg.Strategy == "" {
		cfg.Strategy = defaultStrategy
	}
	// The strategy is a closed set: reject an unknown value at load so validate /
	// doctor catch a typo before run time.
	if err := cfg.Strategy.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrConfigParse, path, err)
	}
	if err := cfg.resolveProfiles(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err) // preserves ErrUnknownAgent via %w
	}
	return &cfg, nil
}

// decodeStrict parses data as a single strict taboo.yaml at path: unknown keys
// and a trailing document are rejected, each failure wrapped as ErrConfigParse.
// An empty document decodes to the zero config.
func decodeStrict(path string, data []byte) (ProjectConfig, error) {
	var cfg ProjectConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // Reject any unknown key.
	decErr := dec.Decode(&cfg)
	if decErr != nil && !errors.Is(decErr, io.EOF) {
		return cfg, fmt.Errorf("%w: %s: %v", ErrConfigParse, path, decErr)
	}
	// taboo.yaml must be a single document: without this probe a stray "---" would
	// silently drop everything after the first. Read once more and reject any
	// trailing document. (An empty file already hit io.EOF, gated by decErr == nil.)
	if decErr == nil {
		if trailing := dec.Decode(&struct{}{}); !errors.Is(trailing, io.EOF) {
			return cfg, fmt.Errorf("%w: %s: multiple YAML documents not supported", ErrConfigParse, path)
		}
	}
	return cfg, nil
}

// resolveProfiles fills the top-level and every workflow's Profile from the
// configured agent/model. An empty agent leaves Profile nil without error;
// requiring the field is validate's job. Workflows are visited in sorted key
// order so an unknown-agent error is deterministic.
func (c *ProjectConfig) resolveProfiles() error {
	if c.Agent != "" {
		p, err := agent.NewProfile(c.Agent, c.Model)
		if err != nil {
			return err
		}
		c.Profile = p
	}

	for _, name := range slices.Sorted(maps.Keys(c.Workflows)) {
		wf := c.Workflows[name]
		agentName := cmp.Or(wf.Agent, c.Agent)
		if agentName == "" {
			continue // no agent anywhere for this workflow: leave Profile nil
		}
		model := cmp.Or(wf.Model, c.Model)
		p, err := agent.NewProfile(agentName, model)
		if err != nil {
			return fmt.Errorf("workflow %q: %w", name, err)
		}
		wf.Profile = p
		c.Workflows[name] = wf
	}
	return nil
}
