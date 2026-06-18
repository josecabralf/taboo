# Library API reference

The exported surface of `package taboo` (`github.com/josecabralf/taboo/pkg/taboo`).
Import path: `taboo "github.com/josecabralf/taboo/pkg/taboo"`.

Signatures below are copied from the Go source files named in each section. The
generated godoc, including unexported helpers' doc comments, is the rendered
source of truth: <https://pkg.go.dev/github.com/josecabralf/taboo/pkg/taboo>.

## Entry points

Source: `runner.go`, `orchestrator.go`, `pool.go`, `commander.go`.

`Runner` drives one agent run. `Orchestrator` wraps a `Runner` in an iteration
loop. `Pool` fans runs out across bounded workshops. All three take a `Config`
and a `Commander`.

### Runner

```go
func New(cfg Config, cmd Commander) *Runner
func (r *Runner) Run(ctx context.Context, req RunRequest) (RunResult, error)
func (r *Runner) Setup(ctx context.Context, req RunRequest) (RunResult, error)
func (r *Runner) Exec(ctx context.Context, req RunRequest, base RunResult) (RunResult, error)
```

`New` returns a `Runner` bound to `cfg`, driving workshop and git commands
through `cmd`.

`Run` executes one agent run end-to-end: `Setup` the worktree, then `Exec` the
agent once in it.

`Setup` ensures the workshop exists, creates a fresh worktree on `req.Branch`,
and swaps it (and the repo's `.git`) into the workshop via stop, remount, start.
It runs once per worktree. The returned `RunResult` carries `Branch` and
`WorktreePath` for the subsequent `Exec` call(s).

`Exec` runs the agent once in the worktree `Setup` prepared (`base.WorktreePath`),
then records the agent's stdout and the branch HEAD on the returned result.
Calling it more than once re-runs the agent in place. The `base` argument
supplies `Branch` and `WorktreePath` from `Setup`.

### Orchestrator

```go
func NewOrchestrator(runner *Runner) *Orchestrator
func (o *Orchestrator) Run(ctx context.Context, req OrchestratedRequest) (OrchestratedResult, error)
```

`NewOrchestrator` returns an `Orchestrator` that drives `runner`.

`Run` prepares the worktree once via `Runner.Setup`, then re-execs the agent up
to `req.MaxIterations` times in that same worktree with `Runner.Exec`, stopping
early once `req.CompletionSignal` appears in the agent's stdout. On a `Setup` or
`Exec` failure it returns the populated result so far alongside the error, with
`StopReason` left at its zero value. A forked request with `MaxIterations` above
1 returns `ErrForkLoop` before any `Setup`.

### Pool

```go
func NewPool(cfg Config, limit int, cmd Commander) *Pool
func (p *Pool) Run(ctx context.Context, reqs []RunRequest) ([]RunResult, error)
```

`NewPool` returns a `Pool` that fans runs out across at most `limit` concurrent
workshops derived from `cfg`. A `limit` below 1 is treated as 1. Each
concurrency slot owns a workshop named `"<Workshop>-<slot>"` under a project
directory `"<ProjectDir>/slot-<slot>"`.

`Run` executes each request concurrently, bounded by the pool's limit, and
returns one `RunResult` per request in input order (`results[i]` corresponds to
`reqs[i]`). A request that fails does not abort the batch: its error is recorded
on the corresponding `RunResult.Err` and the remaining runs proceed. The
returned error is non-nil only when the batch cannot be started at all (the
context is already canceled at entry). A single `Pool` is not safe for
concurrent `Run` calls.

## Requests and results

Source: `template.go` (`Config`), `runner.go` (`RunRequest`, `RunResult`),
`orchestrator.go` (`OrchestratedRequest`, `OrchestratedResult`, `StopReason`).

### Config

```go
type Config struct {
    Workshop   string       // workshop name (the definition's `name:`)
    Base       string       // base image, e.g. "ubuntu@24.04"
    Agent      AgentProfile // the agent profile run inside the workshop
    RepoPath   string       // absolute path to the host git repository
    ProjectDir string       // host directory taboo owns (.workshop/, worktrees/)
}
```

`Config` describes a taboo-managed workshop and the agent that runs inside it.
`RepoPath` is the absolute path to the host git repository whose worktrees the
agent operates on. `ProjectDir` is the host directory taboo owns: it holds the
rendered workshop definition and is passed to every `workshop --project` call.

### RunRequest

```go
type RunRequest struct {
    Branch        string        // new branch created for this run's worktree
    Prompt        string        // agent instruction
    Timeout       time.Duration // bounds the agent exec (zero = no timeout)
    Stdout        io.Writer     // live agent stdout (nil = discard)
    Stderr        io.Writer     // live agent stderr (nil = discard)
    Hooks         Hooks         // lifecycle hooks
    ResumeSession string        // continue a prior session by id (empty = fresh)
    Fork          bool          // with ResumeSession, fork instead of append
}
```

`RunRequest` describes a single agent run. `Fork` without `ResumeSession` is
meaningless and ignored. Agents with no native fork degrade to worktree-only
isolation.

### RunResult

```go
type RunResult struct {
    Branch       string
    WorktreePath string
    Commit       string // HEAD of the branch after the agent ran
    Output       string // captured agent exec stdout (stderr is not retained)
    Err          error  // populated by Pool per run; nil from Runner.Run/Setup/Exec
}
```

`RunResult` reports the outcome of a run. `Err` is populated by `Pool` when
fanning out so one failed run does not abort the batch. The single-run
primitives (`Runner.Run`, `Setup`, `Exec`) return their error separately and
leave `Err` nil.

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
loop's own knobs. `ResultExtractor` is nil to skip extraction, leaving
`OrchestratedResult.Result` nil.

### OrchestratedResult

```go
type OrchestratedResult struct {
    RunResult            // embedded (the final iteration's result)
    Iterations int
    StopReason StopReason
    Result     any // decoded by req.ResultExtractor, or nil
}
```

`OrchestratedResult` reports the outcome of a looped run. `Iterations` is how
many times the agent was run. `StopReason` is only meaningful when `Run` returns
a nil error; on a `Setup` or `Exec` failure it stays at its zero value. `Result`
is type-asserted by the caller (e.g. `res.Result.(MyResult)`).

### StopReason

```go
type StopReason string

const StopMaxIterations StopReason = "max-iterations"
const StopSignal        StopReason = "signal"
```

`StopReason` explains why an orchestrated run's iteration loop ended.
`StopMaxIterations` means the loop exhausted `MaxIterations` without seeing the
completion signal. `StopSignal` means the agent emitted the completion signal
and the loop stopped early.

## Building blocks

Source: `agent.go` (`AgentProfile`, `CommandOptions`, `AgentCommand`,
`SessionSpec`), `result.go` (`ResultExtractor`, `JSONResult`, `Option`,
`Validator`), `prompt.go` (`Substitute`, `PromptTemplate`), `hooks.go` (`Hook`,
`Hooks`).

### AgentProfile

```go
type AgentProfile interface {
    Name() string
    BuildCommand(CommandOptions) AgentCommand
    CredentialEnvKeys() []string
    Sessions() (SessionSpec, bool)
}
```

`AgentProfile` is taboo's per-agent abstraction: it names the agent's SDK
environment and builds the invocation taboo runs inside the workshop. `Name`
equals the SDK name baked into the workshop. `CredentialEnvKeys` are host
environment variable names whose values reach the agent via
`workshop exec --env NAME`. `Sessions` reports where the agent persists session
state, with `ok` false for a sessionless agent. The concrete profiles are listed
in [agents.md](agents.md).

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

`AgentCommand` is the agent invocation taboo execs. `Argv` is the command and
its arguments. When `Stdin` is non-empty the runner pipes it to the agent's
stdin instead of carrying the prompt in argv.

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

type Option func(*extractOptions)
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
into `T`. The caller's struct is the schema.

`WithDelimiters` overrides the block delimiters; the defaults are `<result>` and
`</result>`. `WithStrictFields` makes decoding reject a payload that carries
fields absent from `T` (off by default). `Validator` is the opt-in
semantic-validation hook: if the decoded type implements it, `Extract` calls
`Validate` after decoding and treats a non-nil error as `ErrInvalidResult`. See
the [typed results guide](../guides/typed-results.md).

### Substitute and PromptTemplate

```go
func Substitute(tmpl string, vars map[string]string) (string, error)

func NewPromptTemplate(cmd Commander, project, ws string) *PromptTemplate
func (p *PromptTemplate) Resolve(ctx context.Context, tmpl string, vars map[string]string) (string, error)
func (p *PromptTemplate) Expand(ctx context.Context, prompt string) (string, error)
```

`Substitute` replaces every `{{VAR}}` placeholder in `tmpl` with `vars[VAR]`,
where `VAR` matches `[A-Za-z_][A-Za-z0-9_]*`. It is pure. A placeholder with no
matching key returns an error of the form
`prompt template: undefined variable(s): ...`.

`PromptTemplate` resolves a prompt against a workshop: `{{VAR}}` substitution
followed by shell-expression expansion executed inside the workshop through the
`Commander` seam. `Resolve` runs substitution then expansion; `Expand` runs only
the in-workshop shell expansion.

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
the host through the `Commander` seam, in the run's worktree. Setting
`InWorkshop` runs it inside the workshop via `workshop exec` (cwd `/workspace`)
with the agent's credential env keys and the same mounts.

`OnWorkshopReady` hooks run after the workshop starts with the run's worktree
mounted, before the agent execs. They run on every run. See the
[hooks guide](../guides/prepare-the-workspace-with-hooks.md).

## Execution seam

Source: `commander.go`.

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
```

`Cmd` is a single host-side process invocation (workshop or git). `Commander`
runs host-side commands and is the single side-effecting seam in taboo: the real
implementation shells out, and tests substitute a fake that records invocations.
`NewExecCommander` returns a `Commander` that runs commands as real host
processes via `os/exec`.

## Config and registry

Source: `config.go` (`LoadConfig`, `ProjectConfig`, `RunDefaults`, `Workflow`,
`Duration`), `registry.go` (`NewProfile`, `AgentNames`, `MatchModelFormat`),
`workshop.go` (`WorkshopName`), `agent_opencode.go`, `agent_claudecode.go`,
`agent_copilot.go` (constructors).

### LoadConfig and ProjectConfig

```go
func LoadConfig(path string) (*ProjectConfig, error)

type ProjectConfig struct {
    Workshop        string              `yaml:"workshop"`
    Base            string              `yaml:"base"`
    Repo            string              `yaml:"repo"`
    Agent           string              `yaml:"agent"`
    Model           string              `yaml:"model"`
    Strategy        string              `yaml:"strategy,omitempty"`
    Defaults        *RunDefaults        `yaml:"defaults,omitempty"`
    Workflows       map[string]Workflow `yaml:"workflows,omitempty"`
    DefaultWorkflow string              `yaml:"default-workflow,omitempty"`
    Profile         AgentProfile        `yaml:"-"`
}
```

`LoadConfig` reads and parses a `taboo.yaml` at `path`, resolves the agent and
model of the top level and of every workflow to an `AgentProfile` via the
registry, and returns the config. Decoding is strict: unknown keys are rejected
and only a single YAML document is accepted. `Profile` is the resolved top-level
profile, nil when no agent is set. It is not serialized. The full key reference
is in [taboo-yaml.md](../reference/taboo-yaml.md).

### RunDefaults

```go
type RunDefaults struct {
    BranchPrefix     string   `yaml:"branch-prefix,omitempty"`
    Prompt           string   `yaml:"prompt,omitempty"`
    PromptFile       string   `yaml:"prompt-file,omitempty"`
    Timeout          Duration `yaml:"timeout,omitempty"`
    MaxIterations    int      `yaml:"max-iterations,omitempty"`
    CompletionSignal string   `yaml:"completion-signal,omitempty"`
}
```

`RunDefaults` are scalar-only run settings applied when a workflow or flag does
not override them.

### Workflow

```go
type Workflow struct {
    Prompt        string       `yaml:"prompt,omitempty"`
    PromptFile    string       `yaml:"prompt-file,omitempty"`
    Model         string       `yaml:"model,omitempty"`
    Agent         string       `yaml:"agent,omitempty"`
    MaxIterations int          `yaml:"max-iterations,omitempty"`
    Timeout       Duration     `yaml:"timeout,omitempty"`
    Profile       AgentProfile `yaml:"-"`
}
```

`Workflow` is a named, reusable task type that overrides scalar run params.
`Profile` is the resolved effective profile (workflow agent and model, falling
back to the top level). It is not serialized. A workflow with no agent anywhere
leaves `Profile` nil.

### Duration

```go
type Duration time.Duration
```

`Duration` is a config-friendly `time.Duration` that marshals and unmarshals Go
duration strings such as `30m` or `1h30m` in YAML. An empty value yields zero.

### NewProfile, AgentNames, MatchModelFormat

```go
func NewProfile(name, model string) (AgentProfile, error)
func AgentNames() []string
func MatchModelFormat(agent, model string) (ok bool, expected string)
```

`NewProfile` resolves a canonical agent name to its `AgentProfile`, constructed
for `model`. It validates the name only; its sole error is a wrapped
`ErrUnknownAgent`. `AgentNames` returns every registered agent's canonical name,
sorted. `MatchModelFormat` reports whether `model` looks well-formed for the
named `agent` and returns that agent's human-readable expected-format string. It
is advisory: an unknown agent, a no-opinion hint, or a pattern match all yield
`ok` true. `expected` is `""` exactly when the agent is unknown or has no
opinion. Agent resolution is detailed in [agents.md](agents.md).

### Agent constructors

```go
func OpenCode(model string) AgentProfile
func ClaudeCode(model string) AgentProfile
func Copilot(model string) AgentProfile
```

Each constructor returns the `AgentProfile` for that agent's CLI configured to
run `model`. See [agents.md](agents.md) for the per-agent contract.

### WorkshopName

```go
func WorkshopName(base, agent string) string
```

`WorkshopName` derives the per-agent workshop name from a base name and an agent
name (`"<base>-<agent>"`), so taboo provisions one workshop per distinct agent,
reused across runs.

## Errors

Source: `result.go`, `orchestrator.go`, `registry.go`, `config.go`.

| Sentinel | Source | Meaning |
|---|---|---|
| `ErrNoResult` | `result.go` | No complete result block was found in the agent's output. |
| `ErrInvalidResult` | `result.go` | A result block was found but its payload would not decode or validate. |
| `ErrForkLoop` | `orchestrator.go` | A forked run was given more than one iteration. |
| `ErrUnknownAgent` | `registry.go` | An agent name matched no registered agent. |
| `ErrConfigRead` | `config.go` | The config file could not be read. |
| `ErrConfigParse` | `config.go` | The config document was malformed, carried an unknown field, or held multiple documents. |

The exact messages are:

```go
var ErrNoResult      = errors.New("taboo: no result block found")
var ErrInvalidResult = errors.New("taboo: result block invalid")
var ErrForkLoop      = errors.New("taboo: fork cannot be combined with multiple iterations")
var ErrUnknownAgent  = errors.New("taboo: unknown agent")
var ErrConfigRead    = errors.New("taboo: cannot read config")
var ErrConfigParse   = errors.New("taboo: invalid config")
```

These are wrapped with `%w`, so match them with `errors.Is`.

## See also

- [Agents reference](agents.md) for the per-agent contract.
- [taboo.yaml reference](../reference/taboo-yaml.md) for the config schema.
- [Isolation model](../explanation/isolation-model.md) for the workshop and
  commit-in-place design.
