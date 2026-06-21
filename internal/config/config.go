// Package config parses taboo.yaml — the single source of truth read by both the
// CLI and Go callers that drive runs through pkg — and resolves it, together with
// a named workflow and per-call overrides, into a runnable *run.Plan.
//
// It sits at the top of the internal DAG: it imports agent (profile resolution),
// prompt (variable substitution), workshop (workshop naming + the runner Config),
// and run (the Plan/PlanOverrides types and the Plan it produces). The edge to
// run is forced by a Go mechanic: (*ProjectConfig).Plan is a method, and Go
// forbids defining methods on a non-local (aliased) type, so the resolver method
// must live here where ProjectConfig is defined; its return type *run.Plan and
// param run.PlanOverrides therefore pull in run. run does NOT import config, so
// the DAG stays acyclic.
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
)

// Duration is a config-friendly time.Duration that (un)marshals Go duration
// strings such as "30m" or "1h30m" in YAML.
type Duration time.Duration

// UnmarshalYAML parses a Go duration string via time.ParseDuration; an empty
// value yields zero.
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

// ProjectConfig is the parsed taboo.yaml: the single source of truth read by
// both the CLI and Go callers that drive runs through pkg.
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
	// Strategy is the branch-strategy seam; it defaults to "branch" and accepts
	// any value for forward compatibility.
	Strategy string `yaml:"strategy,omitempty"`
	// SourceDefinition names the workshop definition to derive from when the repo
	// carries several named .workshop/*.yaml definitions; empty selects the sole
	// definition.
	SourceDefinition string `yaml:"source-definition,omitempty"`
	// Defaults holds the scalar run settings applied when a workflow or flag does
	// not override them; nil when the block is omitted.
	Defaults *RunDefaults `yaml:"defaults,omitempty"`
	// Workflows are the named, reusable task types keyed by workflow name.
	Workflows map[string]Workflow `yaml:"workflows,omitempty"`
	// DefaultWorkflow names the workflow run when the CLI selects none.
	DefaultWorkflow string `yaml:"default-workflow,omitempty"`
	// Profile is the resolved top-level profile (agent+model); nil when no agent
	// is set. Not serialized.
	Profile agent.AgentProfile `yaml:"-"`
}

// RunDefaults are scalar-only run settings applied when a workflow or flag does
// not override them. Both prompt (inline) and prompt-file exist here and at the
// workflow level to mirror the CLI's --prompt / --prompt-file flags; the run
// command resolves their precedence later.
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
}

// Workflow is a named, reusable task type that overrides scalar run params. Like
// RunDefaults it carries both prompt (inline) and prompt-file to mirror the
// CLI's --prompt / --prompt-file flags; the run command resolves precedence.
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
	// Profile is the resolved effective profile (workflow agent/model, falling
	// back to the top level). Not serialized.
	Profile agent.AgentProfile `yaml:"-"`
}

// ErrConfigRead is the sentinel LoadConfig wraps when the config file cannot be
// read (e.g. missing path).
var ErrConfigRead = errors.New("taboo: cannot read config")

// ErrConfigParse is the sentinel LoadConfig wraps on a malformed, unknown-field,
// or otherwise invalid config document.
var ErrConfigParse = errors.New("taboo: invalid config")

// defaultStrategy is the branch strategy applied when the config omits one.
const defaultStrategy = "branch"

// LoadConfig reads and parses a taboo.yaml at path, resolves the agent/model of
// the top level and of every workflow to an AgentProfile, and returns the
// config.
func LoadConfig(path string) (*ProjectConfig, error) {
	// Reading the caller-supplied config path is this function's entire purpose;
	// the path originates from a trusted CLI invocation, not from end-user input.
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
	if err := cfg.resolveProfiles(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err) // preserves ErrUnknownAgent via %w
	}
	return &cfg, nil
}

// decodeStrict parses data as a single strict taboo.yaml document at path: it
// rejects unknown keys and any trailing document, wrapping every failure as
// ErrConfigParse named with path. An empty document decodes to the zero config.
func decodeStrict(path string, data []byte) (ProjectConfig, error) {
	var cfg ProjectConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // schema is scalars and file paths only — reject any unknown key
	decErr := dec.Decode(&cfg)
	if decErr != nil && !errors.Is(decErr, io.EOF) {
		return cfg, fmt.Errorf("%w: %s: %v", ErrConfigParse, path, decErr)
	}
	// taboo.yaml must be a single document. Without this probe a stray "---"
	// would silently drop everything after the first document — the opposite of
	// the strict decode above. So we read once more and reject any trailing
	// document. (An empty file already hit io.EOF, gated out by decErr == nil.)
	if decErr == nil {
		if trailing := dec.Decode(&struct{}{}); !errors.Is(trailing, io.EOF) {
			return cfg, fmt.Errorf("%w: %s: multiple YAML documents not supported", ErrConfigParse, path)
		}
	}
	return cfg, nil
}

// resolveProfiles fills the top-level Profile and every workflow's Profile from
// the configured agent/model. A profile is resolved only where an agent is set;
// an empty agent leaves Profile nil without error — enforcing the required field
// is a later validate command's job, not the loader's. Workflows are visited in
// sorted key order so an unknown-agent error is deterministic.
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
