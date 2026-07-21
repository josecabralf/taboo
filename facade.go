// Package taboo runs coding agents in isolated, disposable workshops: each run
// gets a fresh git worktree mounted into a workshop, the agent execs against it
// and commits in place, and the result comes back as a typed value. Everything
// under internal/ is implementation; this package is the public face.
//
// There are three entry patterns, from highest-level to lowest.
//
// One call from a taboo.yaml. RunWorkflow locates the nearest taboo.yaml above a
// directory, resolves the named workflow into a run, and executes it over a
// Commander. RunWorkflowAs[T] does the same and decodes the agent's structured
// output into a typed T, with no caller assertion. This is the bridge most
// adopters want.
//
//	res, err := taboo.RunWorkflow(ctx, ".", "implement", vars, taboo.PlanOverrides{}, taboo.NewExecCommander())
//
// Inspect, then run. LoadConfig parses a taboo.yaml into a ProjectConfig;
// (*ProjectConfig).Plan resolves a workflow plus per-call PlanOverrides into a
// Plan, a pure, inspectable description of one run; (*Plan).Run executes it.
// Reach for this when you want to examine or adjust the resolved run before it
// happens.
//
// Fan out. NewPool builds a Pool from a Config that runs many RunRequests
// concurrently across a bounded set of workshops, returning one RunResult per
// request in input order. The Config for a Pool comes from a resolved Plan.
//
// The Commander interface is the single side-effecting seam: NewExecCommander
// returns the real one, and tests substitute a fake. Agent selection goes
// through NewProfile and the registry helpers (AgentNames, MatchModelFormat).
// Errors are matched with errors.Is against the package's sentinels.
package taboo

// facade.go declares the curated public surface of package taboo in terms of the
// decomposed internal packages. It is the single seam through which the internal/
// implementations reach the public import path github.com/josecabralf/taboo.
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
	"context"

	"github.com/josecabralf/taboo/internal/agent"
	"github.com/josecabralf/taboo/internal/config"
	"github.com/josecabralf/taboo/internal/exec"
	"github.com/josecabralf/taboo/internal/prompt"
	"github.com/josecabralf/taboo/internal/result"
	"github.com/josecabralf/taboo/internal/run"
	"github.com/josecabralf/taboo/internal/workshop"
)

// --- internal/exec (leaf): the host-command seam ---

// Cmd is one host-side process invocation, either a `workshop` container command
// or a `git` command. The run entry points assemble Cmds for you. Build one
// yourself only when driving a Commander directly, for example through Output.
type Cmd = exec.Cmd

// Commander runs host-side commands and is the one side-effecting seam in taboo:
// every container and git call passes through it. Pass NewExecCommander in
// production, and a fake in tests to exercise a whole run without touching the
// host or spawning a process.
type Commander = exec.Commander

// NewExecCommander returns a Commander that runs commands as real host processes.
func NewExecCommander() Commander { return exec.NewExecCommander() }

// Output runs cmd with a fresh stdout buffer and returns the raw captured
// stdout together with the run error. The string is untrimmed, and any Stdout
// already set on cmd is overwritten.
func Output(ctx context.Context, c Commander, cmd Cmd) (string, error) {
	return exec.Output(ctx, c, cmd)
}

// --- internal/agent (leaf): the per-agent abstraction and registry ---

// AgentName is the canonical identity of a registered agent, such as ClaudeCode.
// Use the named constants below rather than string literals.
type AgentName = agent.AgentName

// AgentProfile is taboo's per-agent abstraction. It names the SDK environment the
// agent runs in, builds the exact command taboo execs inside the workshop, and
// lists the credential env keys the agent needs. Obtain one from NewProfile.
type AgentProfile = agent.AgentProfile

// CommandOptions is the input to AgentProfile.BuildCommand: the per-run knobs
// (prompt, model, session, and so on) an AgentProfile turns into a concrete
// invocation.
type CommandOptions = agent.CommandOptions

// AgentCommand is the resolved agent invocation taboo execs inside the workshop:
// the argv and the environment the agent process runs with.
type AgentCommand = agent.AgentCommand

// SessionSpec locates an agent's on-disk session store so a run can resume a
// prior conversation instead of starting cold.
type SessionSpec = agent.SessionSpec

// Named agent constants for the public API. Use these with Workflow.Agent or
// NewProfile instead of literal strings.
const (
	OpenCode      = agent.OpenCode
	ClaudeCode    = agent.ClaudeCode
	GitHubCopilot = agent.GitHubCopilot
	Codex         = agent.Codex
)

// NewProfile resolves a canonical agent name to its AgentProfile, constructed for
// model. It returns ErrUnknownAgent (wrapped) when name matches no registered
// agent, so match the failure with errors.Is(err, ErrUnknownAgent).
func NewProfile(name AgentName, model string) (AgentProfile, error) {
	return agent.NewProfile(name, model)
}

// AgentNames returns every registered agent's canonical name, sorted. Use it to
// populate a CLI choice list or validate user input.
func AgentNames() []string { return agent.AgentNames() }

// MatchModelFormat reports whether model looks well-formed for the named agent and
// returns the expected shape to show the user when it does not. It is a format
// check only; it does not confirm the model exists.
func MatchModelFormat(agentName AgentName, model string) (ok bool, expected string) {
	return agent.MatchModelFormat(agentName, model)
}

// ErrUnknownAgent is the sentinel NewProfile wraps when a name matches no registered agent.
var ErrUnknownAgent = agent.ErrUnknownAgent

// --- internal/result (leaf): typed result extraction ---

// ResultExtractor turns an agent's captured stdout into a typed, validated value.
// Build one with JSONResult[T]. The run loop applies it after the last iteration,
// and the decoded value surfaces as OrchestratedResult.Result.
type ResultExtractor = result.ResultExtractor

// Validator is the opt-in semantic-validation hook JSONResult calls after a
// payload decodes. Return a non-nil error to reject a result that decoded cleanly
// but is not acceptable; the run surfaces it as ErrInvalidResult.
type Validator = result.Validator

// Option configures a JSONResult extractor. See WithDelimiters and WithStrictFields.
type Option = result.Option

// WithDelimiters overrides the result-block delimiters. The default pair is
// `<result>` and `</result>`; the agent wraps its JSON payload in them so taboo
// can find it in free-form output.
func WithDelimiters(open, close string) Option { return result.WithDelimiters(open, close) }

// WithStrictFields makes decoding reject a payload that carries any field absent
// from T, instead of ignoring the extra field. Use it to catch a drifted agent
// schema early.
func WithStrictFields() Option { return result.WithStrictFields() }

// ErrNoResult means no complete result block was found in the agent's output.
var ErrNoResult = result.ErrNoResult

// ErrInvalidResult means a result block was found but its payload would not decode/validate.
var ErrInvalidResult = result.ErrInvalidResult

// JSONResult builds a ResultExtractor that decodes the last result block's JSON
// payload into T. Pass it as RunRequest.ResultExtractor, or let RunWorkflowAs[T]
// wire it for you. It is a forwarding wrapper because Go has no generic alias.
func JSONResult[T any](opts ...Option) ResultExtractor { return result.JSONResult[T](opts...) }

// --- internal/prompt (leaf): placeholder substitution ---

// Substitute replaces every {{VAR}} placeholder in tmpl with vars[VAR]. A
// placeholder with no matching key is an error, so a missing variable fails the
// run instead of reaching the agent as literal `{{VAR}}`.
func Substitute(tmpl string, vars map[string]string) (string, error) {
	return prompt.Substitute(tmpl, vars)
}

// Placeholders returns the distinct {{VAR}} placeholder names tmpl references,
// sorted ascending. Use it to discover which vars a prompt needs before a run,
// for example to prompt the user for each one.
func Placeholders(tmpl string) []string {
	return prompt.Placeholders(tmpl)
}

// --- internal/workshop: the workshop runner input and the CLI-support facet ---

// Config describes a taboo-managed workshop and the agent that runs inside it: the
// workshop name and base image, the resolved AgentProfile, the host repo the agent
// operates on, and the branching Strategy. A Runner and a Pool both take a Config;
// you usually get one from a resolved Plan rather than building it by hand.
type Config = workshop.Config

// BranchingStrategy is the workspace seam a run takes: StrategyBranch or
// StrategyWorktree. It decides whether the agent works on a linked worktree or in
// place on the checkout.
type BranchingStrategy = workshop.BranchingStrategy

// Named strategy constants for the public API. Use these with Config.Strategy or
// ProjectConfig.Strategy instead of literal strings. StrategyWorktree (the
// default when Strategy is empty) gives each run its own linked worktree;
// StrategyBranch operates in place on the existing checkout.
const (
	StrategyBranch   = workshop.StrategyBranch
	StrategyWorktree = workshop.StrategyWorktree
)

// DryRunDerive validates that taboo could derive the agent's workshop from a
// source without launching anything or writing to the host filesystem. Use it to
// check a config and source before committing to a real run.
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

// RunRequest describes a single agent run: the branch to create, the base ref to
// start from, the resolved prompt, the timeout, where to stream output, lifecycle
// Hooks, and an optional ResultExtractor. A Pool takes a slice of these.
type RunRequest = run.RunRequest

// RunResult reports the outcome of a run: the branch and its Commit, the
// BaseCommit the branch started from, the captured agent Output, and any Err. Ask
// Changed whether the agent produced a new commit, read files from the run with
// Artifact, and release the worktree with Dispose.
type RunResult = run.RunResult

// OrchestratedRequest describes a looped run: a RunRequest plus the loop's knobs
// (MaxIterations, CompletionSignal, StopOnNoChange, and the ResultExtractor).
type OrchestratedRequest = run.OrchestratedRequest

// OrchestratedResult reports the outcome of a looped run: how many Iterations ran,
// why the loop stopped (StopReason), and the extracted Result when a
// ResultExtractor was set.
type OrchestratedResult = run.OrchestratedResult

// StopReason explains why an orchestrated run's iteration loop ended: StopSignal,
// StopMaxIterations, or StopNoChange.
type StopReason = run.StopReason

// Hook is a single setup command run at a lifecycle point, either on the host or,
// when InWorkshop is set, inside the workshop.
type Hook = run.Hook

// Hooks groups the lifecycle hook points a run can supply. OnWorkshopReady hooks
// run on every run after the worktree is mounted and before the agent execs, so
// keep them idempotent and cheap.
type Hooks = run.Hooks

// Pool fans multiple agent runs out across a bounded set of workshops, returning
// one RunResult per RunRequest in input order. Build one with NewPool.
type Pool = run.Pool

// Plan is the resolved, inspectable description of one run: the workshop Config,
// the OrchestratedRequest, and the resolved Workflow, Model, and prompt
// Placeholders. Get one from (*ProjectConfig).Plan, inspect or adjust it, then
// call Run.
type Plan = run.Plan

// PlanOverrides is the per-call override layer applied when resolving a Plan. A
// non-zero field wins over the workflow and the config defaults; a zero field
// falls through to them.
type PlanOverrides = run.PlanOverrides

// StopSignal means the agent emitted the completion signal and the loop stopped early.
const StopSignal = run.StopSignal

// StopMaxIterations means the loop exhausted MaxIterations without the signal.
const StopMaxIterations = run.StopMaxIterations

// StopNoChange means stop-on-no-change was enabled and an iteration ended with
// the branch tip unmoved, so the loop stopped at the fixed point.
const StopNoChange = run.StopNoChange

// NewPool returns a Pool that fans runs out across at most limit concurrent
// workshops. A Pool always fans out with the worktree strategy: each slot gets
// its own branch and worktree, so a branch-strategy config is run as worktree.
func NewPool(cfg Config, limit int, cmd Commander) *Pool { return run.NewPool(cfg, limit, cmd) }

// NewResultWithWorktree returns a RunResult whose Artifact reads from an existing
// worktree directory. Use it to inspect artifacts from a worktree taboo did not
// create this process. The result cannot Dispose; see NewResultWithWorktreeCmd.
func NewResultWithWorktree(worktree string) RunResult { return run.NewResultWithWorktree(worktree) }

// NewResultWithWorktreeCmd returns a RunResult that can both read artifacts from
// and Dispose an existing worktree directory, driving git through cmd.
func NewResultWithWorktreeCmd(worktree string, cmd Commander) RunResult {
	return run.NewResultWithWorktreeCmd(worktree, cmd)
}

// ErrForkLoop is returned when a forked run is given more than one iteration.
var ErrForkLoop = run.ErrForkLoop

// --- internal/config: the taboo.yaml model and its loaders ---

// ProjectConfig is the parsed taboo.yaml: the single source of truth read by both
// the CLI and Go callers. It holds the top-level workshop, agent, model, and
// strategy, the RunDefaults, and the named Workflows. Call Plan on it to resolve a
// workflow into a runnable Plan.
type ProjectConfig = config.ProjectConfig

// Workflow is a named, reusable task type in a taboo.yaml. Its fields override the
// top-level and RunDefaults values for that task (prompt, agent, model, and the
// loop knobs), so a project can keep several run shapes side by side.
type Workflow = config.Workflow

// RunDefaults are the scalar run settings applied when neither a workflow nor a
// flag overrides them: the branch prefix, prompt, timeout, and loop knobs.
type RunDefaults = config.RunDefaults

// LoadConfig reads and parses a taboo.yaml at path and resolves its agent/model
// profiles. It wraps ErrConfigRead when the file cannot be read and ErrConfigParse
// when the document is malformed or invalid.
func LoadConfig(path string) (*ProjectConfig, error) { return config.LoadConfig(path) }

// FindConfig ascends from start looking for the nearest taboo.yaml (bare, then
// under .taboo), returning the path and whether one was found.
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
