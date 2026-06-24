# Library API reference

The exported surface of `package taboo` (`github.com/josecabralf/taboo`).
Import path: `"github.com/josecabralf/taboo"`.

Signatures below are copied from the package's public surface. The package
decomposes into `internal/` packages behind a facade, so the implementations live
out of sight; everything documented here is reachable through the single import
path above. The generated godoc is the rendered source of truth:
<https://pkg.go.dev/github.com/josecabralf/taboo>.

The library has three entry patterns, from highest-level to lowest:

1. **One call from a `taboo.yaml`** — `RunWorkflow` / `RunWorkflowAs[T]`.
2. **Inspect, then run** — `LoadConfig` → `(*ProjectConfig).Plan` → `(*Plan).Run`.
3. **Fan out** — `NewPool` → `(*Pool).Run`.

## Entry points

### RunWorkflow and RunWorkflowAs

```go
func RunWorkflow(ctx context.Context, startDir, workflow string, vars map[string]string, ov PlanOverrides, cmd Commander) (OrchestratedResult, error)

func RunWorkflowAs[T any](ctx context.Context, startDir, workflow string, vars map[string]string, ov PlanOverrides, cmd Commander) (T, OrchestratedResult, error)
```

`RunWorkflow` is the one-call bridge: it locates the nearest `taboo.yaml` above
`startDir`, loads it, resolves the named `workflow` into a `Plan`, and runs it
over `cmd`. `vars` fills `{{VAR}}` placeholders in the resolved prompt; `ov`
applies per-call `PlanOverrides`. A missing config is a distinct error naming
`startDir`.

`RunWorkflowAs[T]` is the typed one-call bridge. It runs the same
locate-load-plan-run pipeline, but threads a `JSONResult[T]` extractor into the
plan so the agent's structured output is decoded from the final iteration's
output once the loop ends, and returned as a statically typed `T`, with no caller
assertion. On a locate/load/plan failure it
short-circuits before running, returning the zero `T` and a zero
`OrchestratedResult`. On an extraction failure (`ErrNoResult` / `ErrInvalidResult`)
the run already happened: the error surfaces with the zero `T` alongside the
populated `OrchestratedResult`. See [Iterate until done](../guides/iterate-until-done.md).

### Plan: inspect, then run

```go
func LoadConfig(path string) (*ProjectConfig, error)
func (c *ProjectConfig) Plan(configDir, workflow string, vars map[string]string, ov PlanOverrides) (*Plan, error)
func (p *Plan) Run(ctx context.Context, cmd Commander) (OrchestratedResult, error)

type Plan struct {
    Config   Config              // the resolved workshop runner input
    Request  OrchestratedRequest // the resolved looped run
    Workflow string              // originating workflow name ("" = ad-hoc)
    Model    string              // resolved model string (informational)
}
```

`LoadConfig` parses a `taboo.yaml` into a `ProjectConfig`. `(*ProjectConfig).Plan`
resolves a `workflow` plus per-call `PlanOverrides` into a `Plan`: a pure,
inspectable description of one run (modulo reading a prompt file). Inspect or
adjust `Plan.Config` and `Plan.Request` before running. `(*Plan).Run` executes
the resolved plan — the sole side effect — driving the iteration loop over `cmd`.

This path exposes the resolved run for inspection or adjustment before it
executes; `RunWorkflow` is the same pipeline collapsed into one call.

### PlanOverrides

```go
type PlanOverrides struct {
    Agent              AgentName
    Model              string
    Timeout            time.Duration
    MaxIterations      int
    CompletionSignal   string
    Branch             string
    BaseRef            string
    From               string
    Prompt, PromptFile string
    Stdout, Stderr     io.Writer
}
```

`PlanOverrides` is the per-call override layer applied on top of the config when
resolving a `Plan`. A field's zero value means "unset": fall through to the
workflow, then the top-level `defaults` layer. Numeric knobs gate on `>0`; strings
gate on non-empty.

`BaseRef` is threaded straight onto `RunRequest.BaseRef` — a per-call concern
with no config or workflow layer (see [RunRequest](#runrequest)). `From` selects
the workshop source-definition for this run, overriding the config's
`source-definition`. `Stdout`/`Stderr` are output sinks (nil = discard), not part
of the precedence chain.

### Pool: fan out

```go
func NewPool(cfg Config, limit int, cmd Commander) *Pool
func (p *Pool) Run(ctx context.Context, reqs []RunRequest) ([]RunResult, error)
```

`NewPool` returns a `Pool` that fans runs out across at most `limit` concurrent
workshops derived from `cfg`. A `limit` below 1 is treated as 1. Each concurrency
slot owns a workshop named `"<Workshop>-<slot>"` under a project directory
`"<ProjectDir>/slot-<slot>"`. The `Config` for a pool comes from a resolved
`Plan` (`plan.Config`) or is built directly.

`Run` executes each request concurrently, bounded by the pool's limit, and
returns one `RunResult` per request in input order (`results[i]` corresponds to
`reqs[i]`). A request that fails does not abort the batch: its error is recorded
on the corresponding `RunResult.Err` and the remaining runs proceed. The returned
error is non-nil only when the batch cannot be started at all (the context is
already canceled at entry). A single `Pool` is not safe for concurrent `Run`
calls. See [Run many prompts in parallel](../guides/fan-out-runs.md).

## Requests and results

### RunRequest

```go
type RunRequest struct {
    Branch        string        // new branch created for this run's worktree
    BaseRef       string        // start the worktree branch from this ref (empty = off HEAD, no fetch)
    Prompt        string        // agent instruction
    Timeout       time.Duration // bounds the agent exec (zero = no timeout)
    Stdout        io.Writer     // live agent stdout (nil = discard)
    Stderr        io.Writer     // live agent stderr (nil = discard)
    Hooks         Hooks         // lifecycle hooks
    ResumeSession string        // continue a prior session by id (empty = fresh)
    Fork          bool          // with ResumeSession, fork instead of append
}
```

`RunRequest` describes a single agent run. When `BaseRef` is set (e.g.
`"origin/feature-x"`), setup fetches `origin` and starts the run's branch from
that ref instead of the host repo's `HEAD`; empty starts a fresh branch off
`HEAD` with no fetch. `Fork` without `ResumeSession` is meaningless and ignored.
Agents with no native fork degrade to worktree-only isolation. A forked run must
be single-iteration: `Fork` with `MaxIterations > 1` returns `ErrForkLoop` before
any setup runs.

### RunResult

```go
type RunResult struct {
    Branch string
    Commit string // HEAD of the branch after the agent ran
    Output string // captured agent exec stdout (stderr is not retained)
    Err    error  // populated by Pool per run; nil from the single-run path
}
```

`RunResult` reports the outcome of a run. `Err` is populated by `Pool` when
fanning out so one failed run does not abort the batch. The run's worktree is
not exposed as a path; read files from it with `res.Artifact(relpath)`.

```go
func (r RunResult) Artifact(relpath string) (string, error)
func (r RunResult) Dispose() error

func NewResultWithWorktree(worktree string) RunResult
func NewResultWithWorktreeCmd(worktree string, cmd Commander) RunResult
```

`Artifact` reads the file at `relpath` within the run's worktree and returns its
contents. `relpath` must stay inside the worktree: an absolute path or a `..`
escape is rejected with `artifact "<relpath>": path escapes worktree`, and a
result that carries no worktree handle returns
`artifact: result has no worktree handle`.

`Dispose` removes the run's worktree with a non-force `git worktree remove`,
matching `taboo clean`'s teardown. It is explicit, never automatic: nothing on
the run path calls it for you. It is idempotent (a worktree already gone is
success), and it leaves the branch ref and the workshop intact, so a later push
or run can reuse them. There is no library equivalent of the full `clean`
command, which also tears down the workshop and branch. `Dispose` returns
`dispose: result has no worktree handle` when the result carries no handle.

`NewResultWithWorktree` and `NewResultWithWorktreeCmd` build a `RunResult` around
a bare worktree path so a consumer test can exercise `Artifact`/`Dispose` without
a full run. A result from `NewResultWithWorktree` carries no `Commander`, so
calling `Dispose` on it (when the worktree still exists) fails with
`dispose: result handle has no commander`; use `NewResultWithWorktreeCmd` to
supply one.

### OrchestratedRequest

```go
type OrchestratedRequest struct {
    RunRequest                       // embedded
    MaxIterations    int             // zero or negative = a single run
    CompletionSignal string          // sentinel in stdout that stops the loop early
    ResultExtractor  ResultExtractor // optional; parses a typed result post-loop
}
```

`OrchestratedRequest` describes a looped run: an embedded `RunRequest` plus the
loop's own knobs. It is the type of `Plan.Request`. `ResultExtractor` is nil to
skip extraction, leaving `OrchestratedResult.Result` nil.

### OrchestratedResult

```go
type OrchestratedResult struct {
    RunResult            // embedded (the final iteration's result)
    Iterations int
    StopReason StopReason
    Result     any // decoded by req.ResultExtractor, or nil
}
```

`OrchestratedResult` is what `RunWorkflow`, `RunWorkflowAs[T]`, and `Plan.Run`
return. `Iterations` is how many times the agent was run. `StopReason` is only
meaningful when the call returns a nil error; on a setup or exec failure it stays
at its zero value. `Result` is type-asserted by the caller
(e.g. `res.Result.(MyResult)`); `RunWorkflowAs[T]` returns it already typed.

### StopReason

```go
type StopReason string

const StopMaxIterations StopReason = "max-iterations"
const StopSignal        StopReason = "signal"
```

`StopReason` explains why an orchestrated run's iteration loop ended.
`StopMaxIterations` means the loop exhausted `MaxIterations` without seeing the
completion signal. `StopSignal` means the agent emitted the completion signal and
the loop stopped early.

## Building blocks

### AgentProfile

```go
type AgentProfile interface {
    Name() AgentName
    BuildCommand(CommandOptions) AgentCommand
    CredentialEnvKeys() []string
    Sessions() (SessionSpec, bool)
}

// Named agent constants
type AgentName string

const (
    OpenCode      AgentName = "opencode"
    ClaudeCode    AgentName = "claude-code"
    GitHubCopilot AgentName = "github-copilot"
    Pi            AgentName = "pi"
)

// Agent registry helpers
func NewProfile(name AgentName, model string) (AgentProfile, error)
func AgentNames() []string
func MatchModelFormat(agentName AgentName, model string) (ok bool, expected string)
```

`AgentProfile` is taboo's per-agent abstraction: it names the agent's SDK
environment and builds the invocation taboo runs inside the workshop. `Name`
equals the SDK name baked into the workshop. `CredentialEnvKeys` are host
environment variable names whose values reach the agent via
`workshop exec --env NAME`. `Sessions` reports where the agent persists session
state, with `ok` false for a sessionless agent. Resolve a profile through
`NewProfile`; the concrete profiles are listed in [agents.md](agents.md).

### CommandOptions

```go
type CommandOptions struct {
    Prompt        string
    ResumeSession string
    Fork          bool
}
```

`CommandOptions` is the input to `AgentProfile.BuildCommand`. `Fork` is ignored
without `ResumeSession` and a no-op for an agent whose CLI has no native fork.

### AgentCommand

```go
type AgentCommand struct {
    Argv  []string
    Stdin string
}
```

`AgentCommand` is the agent invocation taboo execs. `Argv` is the command and its
arguments. When `Stdin` is non-empty the runner pipes it to the agent's stdin
instead of carrying the prompt in argv.

### SessionSpec

```go
type SessionSpec struct {
    DirEnv string
    Subdir string
}
```

`SessionSpec` locates an agent's on-disk session store: `Subdir` under the
directory named by the `DirEnv` environment variable (e.g. `XDG_DATA_HOME`).
taboo points `DirEnv` at the sessions mount target so session files survive the
per-run rootfs wipe.

### ResultExtractor and JSONResult

```go
type ResultExtractor interface {
    Extract(output string) (any, error)
}

func JSONResult[T any](opts ...Option) ResultExtractor

type Option // opaque; construct with the options below
func WithDelimiters(open, close string) Option
func WithStrictFields() Option

type Validator interface {
    Validate() error
}
```

`ResultExtractor` turns an agent's captured output into a typed value, returned
as `any` for the caller to type-assert.

`JSONResult[T]` builds a `ResultExtractor` that locates the last
`<result>...</result>` block in the agent's output and decodes its JSON payload
into `T`. The caller's struct is the schema. `RunWorkflowAs[T]` wires this in
automatically. Block pairing anchors on the **last** `<result>` opening tag, then
takes the **first** `</result>` after it; if that last opening tag has no
following close tag, extraction returns `ErrNoResult` even when an earlier block
was complete.

`WithDelimiters` overrides the block delimiters; the defaults are `<result>` and
`</result>`. `WithStrictFields` makes decoding reject a payload that carries
fields absent from `T` (off by default). `Validator` is the opt-in
semantic-validation hook: if the decoded type implements it, `Extract` calls
`Validate` after decoding and treats a non-nil error as `ErrInvalidResult`. See
the [typed results guide](../guides/typed-results.md).

### Substitute

```go
func Substitute(tmpl string, vars map[string]string) (string, error)
```

`Substitute` replaces every `{{VAR}}` placeholder in `tmpl` with `vars[VAR]`,
where `VAR` matches `[A-Za-z_][A-Za-z0-9_]*`. It is pure. A placeholder with no
matching key returns an error of the form
`prompt template: undefined variable(s): ...`. The bridge runs `vars` through
this only when the map is non-empty; with no vars the prompt's `{{VAR}}`
placeholders are left untouched and produce no error.

### Hook and Hooks

```go
type Hook struct {
    Command    []string
    InWorkshop bool
}

type Hooks struct {
    OnWorkshopReady []Hook
}
```

`Hook` is a single setup command run at a lifecycle point. By default it runs on
the host through the `Commander` seam, in the run's worktree. Setting `InWorkshop`
runs it inside the workshop via `workshop exec` (cwd `/taboo/workspace`) with the
agent's credential env keys and the same mounts.

`OnWorkshopReady` hooks run after the workshop starts with the run's worktree
mounted, before the agent execs. They run on every run. See the
[hooks guide](../guides/prepare-the-workspace-with-hooks.md).

## Execution seam

```go
type Cmd struct {
    Name   string    // executable, e.g. "workshop" or "git"
    Args   []string  // arguments
    Dir    string    // working directory on the host (empty = inherit)
    Env    []string  // extra environment ("NAME=value"), appended to the host env
    Stdin  io.Reader // optional
    Stdout io.Writer // optional; nil discards
    Stderr io.Writer // optional; nil discards
}

type Commander interface {
    Run(ctx context.Context, c Cmd) error
}

func NewExecCommander() Commander
func Output(ctx context.Context, c Commander, cmd Cmd) (string, error)
```

`Cmd` is a single host-side process invocation (workshop or git). `Commander`
runs host-side commands and is the single side-effecting seam in taboo: the real
implementation shells out, and tests substitute a fake that records invocations.
`NewExecCommander` returns a `Commander` that runs commands as real host
processes via `os/exec`.

`Output` runs `cmd` and returns its raw captured stdout together with the run
error. The returned string is untrimmed, and any `Stdout` already set on `cmd` is
overwritten with the capture buffer.

## Config model

```go
func LoadConfig(path string) (*ProjectConfig, error)
func FindConfig(start string) (string, bool)

type ProjectConfig struct {
    Workshop         string              `yaml:"workshop"`
    Base             string              `yaml:"base"`
    Repo             string              `yaml:"repo"`
    Agent            AgentName           `yaml:"agent"`
    Model            string              `yaml:"model"`
    Strategy         string              `yaml:"strategy,omitempty"`
    SourceDefinition string              `yaml:"source-definition,omitempty"`
    Defaults         *RunDefaults        `yaml:"defaults,omitempty"`
    Workflows        map[string]Workflow `yaml:"workflows,omitempty"`
    DefaultWorkflow  string              `yaml:"default-workflow,omitempty"`
    Profile          AgentProfile        `yaml:"-"`
}
```

`LoadConfig` reads and parses a `taboo.yaml` at `path`, resolves the agent and
model of the top level and of every workflow to an `AgentProfile` via the
registry, and returns the config. Decoding is strict: unknown keys are rejected
and only a single YAML document is accepted. `Profile` is the resolved top-level
profile, nil when no agent is set; it is not serialized.

`FindConfig` ascends from `start` looking for the nearest `taboo.yaml` (bare,
then under `.taboo`), returning its path and whether one was found. It is the
locate step `RunWorkflow` runs for you. The full key reference is in
[taboo-yaml.md](taboo-yaml.md).

### RunDefaults

```go
type RunDefaults struct {
    BranchPrefix     string   `yaml:"branch-prefix,omitempty"`
    Prompt           string   `yaml:"prompt,omitempty"`
    PromptFile       string   `yaml:"prompt-file,omitempty"`
    Timeout          Duration `yaml:"timeout,omitempty"` // YAML duration string, e.g. "30m"
    MaxIterations    int      `yaml:"max-iterations,omitempty"`
    CompletionSignal string   `yaml:"completion-signal,omitempty"`
}
```

`RunDefaults` are scalar-only run settings applied when a workflow or flag does
not override them. `Timeout` is a `Duration` (a named `time.Duration`) written in
`taboo.yaml` as a duration string such as `30m` or `1h30m`. The type lives in the
internal config package, so callers set it through the YAML, not as a Go value.

### Workflow

```go
type Workflow struct {
    Prompt        string       `yaml:"prompt,omitempty"`
    PromptFile    string       `yaml:"prompt-file,omitempty"`
    Model         string       `yaml:"model,omitempty"`
    Agent         AgentName    `yaml:"agent,omitempty"`
    MaxIterations int          `yaml:"max-iterations,omitempty"`
    Timeout       Duration     `yaml:"timeout,omitempty"` // YAML duration string, e.g. "30m"
    Profile       AgentProfile `yaml:"-"`
}
```

`Workflow` is a named, reusable task type that overrides scalar run params.
`Profile` is the resolved effective profile (workflow agent and model, falling
back to the top level); it is not serialized. A workflow with no agent anywhere
leaves `Profile` nil.

### Config

```go
type Config struct {
    Workshop         string       // workshop name (the definition's `name:`)
    Base             string       // base image, e.g. "ubuntu@24.04"
    Agent            AgentProfile // the agent profile run inside the workshop
    RepoPath         string       // absolute path to the host git repository
    ProjectDir       string       // host directory taboo owns (.workshop/, worktrees/)
    SourceDefinition string       // selected source-definition name (empty = auto-resolve)
}
```

`Config` describes a taboo-managed workshop and the agent that runs inside it. It
is the runner input carried by `Plan.Config` and consumed by `NewPool`.
`RepoPath` is the absolute path to the host git repository whose worktrees the
agent operates on. `ProjectDir` is the host directory taboo owns: it holds the
rendered workshop definition and is passed to every `workshop --project` call.
`SourceDefinition` names the workshop definition to derive from when the repo
carries several; empty auto-resolves the project's single definition.

## Agent registry and CLI-support facet

```go
func NewProfile(name AgentName, model string) (AgentProfile, error)
func AgentNames() []string
func MatchModelFormat(agentName AgentName, model string) (ok bool, expected string)

func DryRunDerive(cfg Config, source []byte) (projectNames []string, err error)
func SourceDefinitions(repoPath string) ([]string, error)
func ValidateSourceDefinition(named []string, selection string) error
```

`NewProfile` resolves a canonical agent name to its `AgentProfile`, constructed
for `model`. It validates the name only; its sole error is a wrapped
`ErrUnknownAgent`. `AgentNames` returns every registered agent's canonical name,
sorted. `MatchModelFormat` reports whether `model` looks well-formed for the named
`agent` and returns that agent's human-readable expected-format string. It is
advisory: an unknown agent, a no-opinion hint, or a pattern match all yield `ok`
true. `expected` is `""` exactly when the agent is unknown or has no opinion.
Agent resolution is detailed in [agents.md](agents.md).

`DryRunDerive`, `SourceDefinitions`, and `ValidateSourceDefinition` are the
workshop-derivation facet the CLI's `validate`, `init`, and `list` commands lean
on. `SourceDefinitions` returns the sorted names of a repo's named workshop
definitions; `ValidateSourceDefinition` checks a selection names one of them;
`DryRunDerive` validates that taboo could derive the agent's workshop from a
source without launching anything or writing to the host filesystem.

## Errors

| Sentinel | Meaning |
|---|---|
| `ErrNoResult` | No complete result block was found in the agent's output. |
| `ErrInvalidResult` | A result block was found but its payload would not decode or validate. |
| `ErrForkLoop` | A forked run was given more than one iteration. |
| `ErrUnknownAgent` | An agent name matched no registered agent. |
| `ErrConfigRead` | The config file could not be read. |
| `ErrConfigParse` | The config document was malformed, carried an unknown field, or held multiple documents. |
| `ErrUnknownWorkflow` | The requested workflow name matched no config entry. |
| `ErrNoPrompt` | No prompt is configured anywhere in the precedence chain. |
| `ErrNoAgent` | No agent is configured anywhere in the precedence chain. |

All sentinels carry a `taboo:` message prefix and are wrapped with `%w`, so match
them with `errors.Is`.

## See also

- [Iterate until done](../guides/iterate-until-done.md) for the looped-run bridge.
- [Run many prompts in parallel](../guides/fan-out-runs.md) for `Pool`.
- [Get a typed result out of a run](../guides/typed-results.md) for `JSONResult[T]`.
- [Agents reference](agents.md) for the per-agent contract.
- [taboo.yaml reference](taboo-yaml.md) for the config schema.
- [Isolation model](../explanation/isolation-model.md) for the workshop and
  commit-in-place design.
