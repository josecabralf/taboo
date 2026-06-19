package taboo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// ErrUnknownWorkflow is the sentinel Plan wraps when the requested workflow name
// matches no entry in the config's Workflows map.
var ErrUnknownWorkflow = errors.New("taboo: unknown workflow")

// ErrNoPrompt is the sentinel Plan returns when no prompt is configured anywhere
// in the override → workflow → defaults precedence chain.
var ErrNoPrompt = errors.New("taboo: no prompt configured")

// PlanOverrides is the per-call override layer applied on top of the config when
// resolving a Plan. A field's zero value means "unset": fall through to the
// workflow, then the top-level/defaults layer. Numeric knobs gate on >0; strings
// gate on non-empty. Stdout/Stderr are output sinks (nil = discard), not part of
// the precedence chain.
type PlanOverrides struct {
	Agent, Model       string
	Timeout            time.Duration
	MaxIterations      int
	CompletionSignal   string
	Branch             string
	From               string
	Prompt, PromptFile string
	Stdout, Stderr     io.Writer
}

// Plan is a resolved, inspectable description of one run: the runner Config, the
// looped Request, the originating workflow name ("" = ad-hoc), and the resolved
// model string (a record of what NewProfile was built with — the field is
// informational; the profile on Config.Agent is what the run actually uses).
// Building it is pure (modulo reading a prompt file); running it via Run is the
// sole side effect.
type Plan struct {
	Config   Config
	Request  OrchestratedRequest
	Workflow string
	Model    string
}

// Plan resolves the config, the named workflow, and the per-call overrides into
// a single inspectable *Plan, replicating the CLI's resolvePlan precedence. The
// agent profile is re-resolved here (not reused from the workflow) so override
// agent/model take effect.
func (c *ProjectConfig) Plan(configDir, workflow string, vars map[string]string, ov PlanOverrides) (*Plan, error) {
	var wf Workflow
	if workflow != "" {
		found, ok := c.Workflows[workflow]
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownWorkflow, workflow)
		}
		wf = found
	}

	defaults := c.Defaults
	if defaults == nil {
		defaults = &RunDefaults{}
	}

	model := orElse(ov.Model, orElse(wf.Model, c.Model))
	agent := orElse(ov.Agent, orElse(wf.Agent, c.Agent))
	if agent == "" {
		return nil, errors.New("taboo: no agent configured (set agent: on the workflow or a top-level agent:)")
	}
	profile, err := NewProfile(agent, model)
	if err != nil {
		return nil, err
	}

	prompt, err := resolvePrompt(ov, wf, defaults, configDir)
	if err != nil {
		return nil, err
	}
	if len(vars) > 0 {
		prompt, err = Substitute(prompt, vars)
		if err != nil {
			return nil, err
		}
	}

	repoPath, err := resolveRepoPath(configDir, c.Repo)
	if err != nil {
		return nil, err
	}
	projectDir := filepath.Join(repoPath, ".taboo")

	sourceDefinition := orElse(ov.From, c.SourceDefinition)

	timeout := ResolveTimeout(wf, defaults)
	if ov.Timeout > 0 {
		timeout = ov.Timeout
	}
	maxIter := ResolveMaxIterations(wf, defaults)
	if ov.MaxIterations > 0 {
		maxIter = ov.MaxIterations
	}
	signal := ResolveCompletionSignal(defaults)
	if ov.CompletionSignal != "" {
		signal = ov.CompletionSignal
	}

	branch := resolveBranch(ov, defaults, workflow)

	stdout := orDiscard(ov.Stdout)
	stderr := orDiscard(ov.Stderr)

	return &Plan{
		Config: Config{
			Workshop:         WorkshopName(c.Workshop, profile.Name()),
			Base:             c.Base,
			Agent:            profile,
			RepoPath:         repoPath,
			ProjectDir:       projectDir,
			SourceDefinition: sourceDefinition,
		},
		Request: OrchestratedRequest{
			RunRequest:       RunRequest{Branch: branch, Prompt: prompt, Timeout: timeout, Stdout: stdout, Stderr: stderr},
			MaxIterations:    maxIter,
			CompletionSignal: signal,
		},
		Workflow: workflow,
		Model:    model,
	}, nil
}

// Run executes the resolved Plan over cmd, driving the orchestrator loop. It is
// the sole side effect of a Plan: building one is pure (modulo reading a prompt
// file), running it dispatches the workshop/git commands.
func (p *Plan) Run(ctx context.Context, cmd Commander) (OrchestratedResult, error) {
	return NewOrchestrator(New(p.Config, cmd)).Run(ctx, p.Request)
}

// locateAndPlan resolves a *Plan from disk: it discovers the nearest config
// ascending from startDir, loads it, and plans the named workflow with the given
// vars and overrides. A missing config is a distinct error naming startDir. This
// is the shared front half of RunWorkflow and RunWorkflowAs.
func locateAndPlan(startDir, workflow string, vars map[string]string, ov PlanOverrides) (*Plan, error) {
	path, found := FindConfig(startDir)
	if !found {
		return nil, fmt.Errorf("taboo: no taboo.yaml found from %s", startDir)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		return nil, err
	}
	return cfg.Plan(filepath.Dir(path), workflow, vars, ov)
}

// RunWorkflow is the one-call bridge: it locates the config above startDir, loads
// it, resolves the named workflow into a Plan, and runs it over cmd.
func RunWorkflow(ctx context.Context, startDir, workflow string, vars map[string]string, ov PlanOverrides, cmd Commander) (OrchestratedResult, error) {
	plan, err := locateAndPlan(startDir, workflow, vars, ov)
	if err != nil {
		return OrchestratedResult{}, err
	}
	return plan.Run(ctx, cmd)
}

// RunWorkflowAs is the typed one-call bridge. Like RunWorkflow it locates, loads,
// plans and runs the named workflow, but it threads a JSONResult[T] extractor
// into the plan so the agent's structured output is decoded in-loop and returned
// as a statically typed T, with no caller assertion needed. The generic lives
// only on this free function; a Plan.RunAs[T] method would be illegal Go.
//
// On a locate/load/plan failure it short-circuits before Run, returning the zero
// T and a zero OrchestratedResult. On an extraction failure (ErrNoResult /
// ErrInvalidResult) the Run already happened: the error surfaces with the zero T
// alongside the populated OrchestratedResult.
func RunWorkflowAs[T any](ctx context.Context, startDir, workflow string, vars map[string]string, ov PlanOverrides, cmd Commander) (T, OrchestratedResult, error) {
	var zero T
	plan, err := locateAndPlan(startDir, workflow, vars, ov)
	if err != nil {
		return zero, OrchestratedResult{}, err
	}
	plan.Request.ResultExtractor = JSONResult[T]()
	res, err := plan.Run(ctx, cmd)
	if err != nil {
		return zero, res, err
	}
	typed, _ := res.Result.(T)
	return typed, res, nil
}

// resolvePrompt applies the prompt precedence (first non-empty wins): inline
// override → override prompt-file → workflow inline → workflow prompt-file →
// defaults inline → defaults prompt-file → else ErrNoPrompt. A *-file value is
// read from disk (resolved against configDir); a read error is wrapped and
// returned (it is NOT ErrNoPrompt, which means nothing was configured anywhere).
func resolvePrompt(ov PlanOverrides, wf Workflow, defaults *RunDefaults, configDir string) (string, error) {
	switch {
	case ov.Prompt != "":
		return ov.Prompt, nil
	case ov.PromptFile != "":
		return readPromptFile(ov.PromptFile, configDir)
	case wf.Prompt != "":
		return wf.Prompt, nil
	case wf.PromptFile != "":
		return readPromptFile(wf.PromptFile, configDir)
	case defaults.Prompt != "":
		return defaults.Prompt, nil
	case defaults.PromptFile != "":
		return readPromptFile(defaults.PromptFile, configDir)
	default:
		return "", ErrNoPrompt
	}
}

// orDiscard returns w, or io.Discard when w is nil, so a nil output sink on
// PlanOverrides safely becomes a no-op writer the runner can stream to.
func orDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

// resolveRepoPath resolves the run's repo path, always absolute and
// config-anchored. The cases: an explicit repo: override is used as-is when
// absolute, else taken relative to configDir; a .taboo config dir resolves to its
// parent; a bare taboo.yaml resolves to configDir itself. ProjectDir is then
// repoPath/.taboo, structurally.
func resolveRepoPath(configDir, repo string) (string, error) {
	base := configDir
	switch {
	case repo != "":
		// An absolute repo: stands on its own; a relative one anchors to the
		// config dir (filepath.Join would otherwise nest an absolute path under it).
		base = repo
		if !filepath.IsAbs(repo) {
			base = filepath.Join(configDir, repo)
		}
	case filepath.Base(configDir) == ".taboo":
		base = filepath.Dir(configDir)
	}
	return filepath.Abs(base)
}

// resolveBranch returns the override branch verbatim, or a fresh per-run branch
// (prefix + label + timestamp + nanosecond) so back-to-back runs of the same
// workflow never collide. The label is the workflow name, or "adhoc" for a
// prompt-only run.
func resolveBranch(ov PlanOverrides, defaults *RunDefaults, workflow string) string {
	if ov.Branch != "" {
		return ov.Branch
	}
	label := workflow
	if label == "" {
		label = "adhoc"
	}
	now := time.Now()
	return fmt.Sprintf("%s%s-%s-%09d", defaults.BranchPrefix, label, now.Format("20060102-150405"), now.Nanosecond())
}

// FindConfig ascends from start (cleaned) looking for a config: at each dir it
// probes <dir>/taboo.yaml first, then <dir>/.taboo/taboo.yaml, returning the
// first existing path and true. It stops and returns "", false at the
// filesystem root (where filepath.Dir(dir) == dir) with no hit. This folds the
// CLI's findConfig into the library, statting the real filesystem directly.
func FindConfig(start string) (string, bool) {
	dir := filepath.Clean(start)
	for {
		if p := filepath.Join(dir, "taboo.yaml"); fileExists(p) {
			return p, true
		}
		if p := filepath.Join(dir, ".taboo", "taboo.yaml"); fileExists(p) {
			return p, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// fileExists reports whether p can be stat'd without error (presence-based, like
// the CLI's discovery probe).
func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// readPromptFile reads a configured prompt-file, resolving a relative path
// against configDir, and returns its contents verbatim.
func readPromptFile(path, configDir string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(configDir, path)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- trusted config-derived path
	if err != nil {
		return "", fmt.Errorf("taboo: read prompt-file %s: %w", path, err)
	}
	return string(data), nil
}
