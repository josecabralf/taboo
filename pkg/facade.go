package taboo

// facade.go declares the curated public surface of package taboo in terms of the
// decomposed internal packages. It is the single seam through which the internal/
// implementations reach the public import path github.com/josecabralf/taboo/pkg.
//
// Two rules keep the seam honest:
//   - Every signature-bearing public type is a `=` alias, so a type declared by a
//     consumer still satisfies a taboo interface across the package boundary (a
//     defined-type copy would not).
//   - Sentinels are re-exported as `var ErrX = pkg.ErrX`, so pointer identity
//     survives and callers' errors.Is keeps matching the value the internal
//     package wraps.
//
// Funcs are thin forwarding wrappers rather than `var Fn = pkg.Fn` aliases so
// they render as proper function signatures in go doc (an aliased var leaks the
// internal package name onto the right-hand side). The wrappers are pure
// pass-throughs; they add no behavior.
//
// Declarations are grouped by source package in dependency order (leaves first).

import (
	"github.com/josecabralf/taboo/pkg/internal/agent"
	"github.com/josecabralf/taboo/pkg/internal/config"
	"github.com/josecabralf/taboo/pkg/internal/exec"
	"github.com/josecabralf/taboo/pkg/internal/prompt"
	"github.com/josecabralf/taboo/pkg/internal/result"
	"github.com/josecabralf/taboo/pkg/internal/run"
	"github.com/josecabralf/taboo/pkg/internal/workshop"
)

// --- internal/exec (leaf): the host-command seam ---

// Cmd is a single host-side process invocation (workshop or git).
type Cmd = exec.Cmd

// Commander runs host-side commands; the single side-effecting seam in taboo.
type Commander = exec.Commander

// NewExecCommander returns a Commander that runs commands as real host processes.
func NewExecCommander() Commander { return exec.NewExecCommander() }

// --- internal/agent (leaf): the per-agent abstraction and registry ---

// AgentProfile is taboo's per-agent abstraction: it names the agent's SDK
// environment and builds the exact invocation taboo runs inside the workshop.
type AgentProfile = agent.AgentProfile

// CommandOptions is the input to AgentProfile.BuildCommand.
type CommandOptions = agent.CommandOptions

// AgentCommand is the agent invocation taboo execs.
type AgentCommand = agent.AgentCommand

// SessionSpec locates an agent's on-disk session store.
type SessionSpec = agent.SessionSpec

// NewProfile resolves a canonical agent name to its AgentProfile, constructed for model.
func NewProfile(name, model string) (AgentProfile, error) { return agent.NewProfile(name, model) }

// AgentNames returns every registered agent's canonical name, sorted.
func AgentNames() []string { return agent.AgentNames() }

// MatchModelFormat reports whether model looks well-formed for the named agent.
func MatchModelFormat(agentName, model string) (ok bool, expected string) {
	return agent.MatchModelFormat(agentName, model)
}

// ErrUnknownAgent is the sentinel NewProfile wraps when a name matches no registered agent.
var ErrUnknownAgent = agent.ErrUnknownAgent

// --- internal/result (leaf): typed result extraction ---

// ResultExtractor turns an agent's captured output into a typed, validated result.
type ResultExtractor = result.ResultExtractor

// Validator is the opt-in semantic-validation hook called after decoding.
type Validator = result.Validator

// Option configures a JSONResult extractor.
type Option = result.Option

// WithDelimiters overrides the result block delimiters.
func WithDelimiters(open, close string) Option { return result.WithDelimiters(open, close) }

// WithStrictFields makes decoding reject fields absent from T.
func WithStrictFields() Option { return result.WithStrictFields() }

// ErrNoResult means no complete result block was found in the agent's output.
var ErrNoResult = result.ErrNoResult

// ErrInvalidResult means a result block was found but its payload would not decode/validate.
var ErrInvalidResult = result.ErrInvalidResult

// JSONResult builds a ResultExtractor that decodes the last result block's JSON
// payload into T. It is a forwarding wrapper because Go has no generic alias.
func JSONResult[T any](opts ...Option) ResultExtractor { return result.JSONResult[T](opts...) }

// --- internal/prompt (leaf): placeholder substitution ---

// Substitute replaces every {{VAR}} placeholder in tmpl with vars[VAR].
func Substitute(tmpl string, vars map[string]string) (string, error) {
	return prompt.Substitute(tmpl, vars)
}

// --- internal/workshop: the workshop runner input and the CLI-support facet ---

// Config describes a taboo-managed workshop and the agent that runs inside it.
type Config = workshop.Config

// DryRunDerive validates that taboo could derive the agent's workshop from a
// source without launching anything or writing to the host filesystem.
func DryRunDerive(cfg Config, source []byte) (projectNames []string, err error) {
	return workshop.DryRunDerive(cfg, source)
}

// SourceDefinitions returns the sorted names of the project's named workshop definitions.
func SourceDefinitions(repoPath string) ([]string, error) {
	return workshop.SourceDefinitions(repoPath)
}

// ValidateSourceDefinition checks that a selection names one of the project's named workshop definitions.
func ValidateSourceDefinition(named []string, selection string) error {
	return workshop.ValidateSourceDefinition(named, selection)
}

// --- internal/run: the run primitives, the inspectable Plan, and fan-out ---

// RunRequest describes a single agent run.
type RunRequest = run.RunRequest

// RunResult reports the outcome of a run.
type RunResult = run.RunResult

// OrchestratedRequest describes a looped run: a RunRequest plus the loop's knobs.
type OrchestratedRequest = run.OrchestratedRequest

// OrchestratedResult reports the outcome of a looped run.
type OrchestratedResult = run.OrchestratedResult

// StopReason explains why an orchestrated run's iteration loop ended.
type StopReason = run.StopReason

// Hook is a single setup command run at a lifecycle point.
type Hook = run.Hook

// Hooks groups the lifecycle hook points a run can supply.
type Hooks = run.Hooks

// Pool fans multiple agent runs out across a bounded set of workshops.
type Pool = run.Pool

// Plan is the resolved, inspectable description of one run.
type Plan = run.Plan

// PlanOverrides is the per-call override layer applied when resolving a Plan.
type PlanOverrides = run.PlanOverrides

// StopSignal means the agent emitted the completion signal and the loop stopped early.
const StopSignal = run.StopSignal

// StopMaxIterations means the loop exhausted MaxIterations without the signal.
const StopMaxIterations = run.StopMaxIterations

// NewPool returns a Pool that fans runs out across at most limit concurrent workshops.
func NewPool(cfg Config, limit int, cmd Commander) *Pool { return run.NewPool(cfg, limit, cmd) }

// ErrForkLoop is returned when a forked run is given more than one iteration.
var ErrForkLoop = run.ErrForkLoop

// --- internal/config: the taboo.yaml model and its loaders ---

// ProjectConfig is the parsed taboo.yaml: the single source of truth read by both the CLI and Go callers.
type ProjectConfig = config.ProjectConfig

// Workflow is a named, reusable task type that overrides scalar run params.
type Workflow = config.Workflow

// RunDefaults are scalar-only run settings applied when a workflow or flag does not override them.
type RunDefaults = config.RunDefaults

// LoadConfig reads and parses a taboo.yaml at path and resolves its agent/model profiles.
func LoadConfig(path string) (*ProjectConfig, error) { return config.LoadConfig(path) }

// FindConfig ascends from start looking for the nearest taboo.yaml (bare, then under .taboo).
func FindConfig(start string) (string, bool) { return config.FindConfig(start) }

// ErrConfigRead is the sentinel LoadConfig wraps when the config file cannot be read.
var ErrConfigRead = config.ErrConfigRead

// ErrConfigParse is the sentinel LoadConfig wraps on a malformed or invalid config document.
var ErrConfigParse = config.ErrConfigParse

// ErrUnknownWorkflow is the sentinel Plan wraps when the requested workflow name matches no config entry.
var ErrUnknownWorkflow = config.ErrUnknownWorkflow

// ErrNoPrompt is the sentinel Plan returns when no prompt is configured anywhere in the precedence chain.
var ErrNoPrompt = config.ErrNoPrompt

// ErrNoAgent is the sentinel Plan returns when no agent is configured anywhere in the precedence chain.
var ErrNoAgent = config.ErrNoAgent
