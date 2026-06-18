package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	taboo "github.com/josecabralf/taboo/pkg/taboo"
)

// errRunFailed is the sentinel run returns when its preflight finds an error
// (workshop unreachable, or the config fails validate). The preflight report is
// printed to stderr first; main maps the sentinel to a non-zero exit. It mirrors
// doctor's errChecksFailed but is run-specific so a caller can distinguish a
// preflight refusal from a failure inside the run itself.
var errRunFailed = errors.New("run: preflight failed")

// errNoPrompt is the sentinel resolvePrompt returns when neither the workflow
// nor the defaults configure any prompt at all; resolvePlan maps it to a
// friendly "set prompt or prompt-file" message, whereas a prompt-file that is
// configured but unreadable is a different, precisely-surfaced error.
var errNoPrompt = errors.New("run: no prompt configured")

// runOptions are the parsed flags for the run subcommand: the highest-precedence
// layer of run-param resolution (top-level config -> workflow -> these flags).
type runOptions struct {
	// prompt overrides the run instruction inline.
	prompt string
	// promptFile overrides the run instruction from a file (relative to the .taboo
	// dir, or absolute), taking effect only when prompt is empty.
	promptFile string
	// agent overrides the resolved agent.
	agent string
	// model overrides the resolved agent's model.
	model string
	// timeout overrides the per-exec timeout (zero leaves it unset).
	timeout time.Duration
	// iterations overrides the iteration cap (zero or less leaves it unset).
	iterations int
	// signal overrides the completion signal that ends the iteration loop early.
	signal string
	// branch overrides the auto-generated per-run branch verbatim.
	branch string
	// dryRun resolves the plan and prints it without touching the host.
	dryRun bool
	// yes skips the interactive pre-run confirmation (for non-interactive callers).
	yes bool
	// asJSON emits the machine result as a JSON object instead of the plain form.
	asJSON bool
	// varsFile is a JSON file of {"VAR":"value"} pairs substituted literally into
	// {{VAR}} placeholders in the resolved prompt (no shell expansion of the values).
	varsFile string
	// vars are repeatable KEY=VALUE template variables substituted literally into
	// {{KEY}} placeholders; they override matching --vars-file keys.
	vars []string
}

// newRunCmd builds the `run` subcommand: it selects what to run (a named
// workflow positionally, an ad-hoc `--prompt` off the top-level defaults, or a
// bare run's default-workflow), resolves the run params from taboo.yaml and the
// flags into an execution plan via the precedence chain (top-level -> workflow ->
// flags), runs a host preflight, and drives the plan end-to-end through
// pkg/taboo's Orchestrator on a fresh per-run branch. Live agent output streams to
// stderr so the machine result stays clean on stdout.
func newRunCmd(env Env) *cobra.Command {
	opts := runOptions{}
	cmd := &cobra.Command{
		Use:   "run [workflow]",
		Short: "Run a workflow (or an ad-hoc prompt) end-to-end on a fresh branch",
		Long: "run selects what to execute from taboo.yaml: a workflow named positionally, the " +
			"configured default-workflow for a bare run, or an ad-hoc --prompt with no workflow. It " +
			"resolves the prompt, model, timeout, iterations, and branch — where a CLI flag overrides " +
			"the workflow, which overrides the top-level defaults — then executes the run through a " +
			"workshop on a new per-run branch. Agent progress streams to stderr; the run result " +
			"(branch, commit, output) is written to stdout. --vars-file and --var inject " +
			"caller-supplied values literally into the prompt's {{VAR}} placeholders (see each flag).",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRun(cmd.Context(), env, &opts, args)
		},
	}
	cmd.Flags().StringVar(&opts.prompt, "prompt", "", "run instruction, overriding any configured prompt")
	cmd.Flags().StringVar(&opts.promptFile, "prompt-file", "", "file whose contents are the run instruction (relative to the .taboo dir)")
	cmd.Flags().StringVar(&opts.varsFile, "vars-file", "", "JSON file of {\"VAR\":\"value\"} pairs substituted literally into {{VAR}} placeholders (no shell expansion)")
	cmd.Flags().StringArrayVar(&opts.vars, "var", nil, "KEY=VALUE template variable substituted literally into {{KEY}} (repeatable; overrides --vars-file)")
	cmd.Flags().StringVar(&opts.agent, "agent", "", "override the resolved agent for this run")
	cmd.Flags().StringVar(&opts.model, "model", "", "override the resolved agent's model for this run")
	cmd.Flags().DurationVar(&opts.timeout, "timeout", 0, "override the per-exec timeout, e.g. 30m")
	cmd.Flags().IntVar(&opts.iterations, "iterations", 0, "override the max iteration cap for this run")
	cmd.Flags().StringVar(&opts.signal, "signal", "", "string that, when it appears in agent output, stops the iteration loop early (run treated as complete)")
	cmd.Flags().StringVar(&opts.branch, "branch", "", "branch name for this run (default: auto-generated from the workflow name — or \"adhoc\" for a --prompt run — and a timestamp)")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "resolve and print the plan without running anything")
	cmd.Flags().BoolVar(&opts.yes, "yes", false, "skip the interactive pre-run confirmation")
	cmd.Flags().BoolVar(&opts.asJSON, "json", false, "emit the run result as JSON")
	return cmd
}

// runPlan is the fully resolved description of one run: everything translated
// from the loaded config, the selection, and the flags into the values pkg/taboo
// consumes. It is the seam between resolution (pure, testable, side-effect free)
// and execution, so --dry-run can print it without running anything.
type runPlan struct {
	workflow         string
	runnerConfig     taboo.Config
	branch           string
	model            string
	prompt           string
	timeout          time.Duration
	maxIterations    int
	completionSignal string
	// adhoc marks a run with no named workflow (an off-the-defaults --prompt run),
	// so the human-facing summaries name it as such instead of as a workflow.
	adhoc bool
}

// runRun is the run command's select-resolve-preflight-execute flow. It discovers
// and loads the config, selects what to run (named workflow, ad-hoc, or default),
// resolves that into a plan, and then either prints the plan (--dry-run) or runs a
// host preflight and executes it. Each stage's failure is surfaced before the
// next, so a misconfigured project never reaches the workshop.
func runRun(ctx context.Context, env Env, opts *runOptions, args []string) error {
	configPath, cfg, err := loadProjectConfig(env)
	if err != nil {
		return err
	}
	sel, err := selectRun(cfg, args, opts)
	if err != nil {
		return err
	}
	plan, err := resolvePlan(cfg, configPath, sel, opts)
	if err != nil {
		return err
	}

	if opts.dryRun {
		printPlan(env, plan)
		return nil
	}

	if err := runPreflight(ctx, env); err != nil {
		return err
	}
	proceed, err := confirmRun(env, opts, plan)
	if err != nil {
		return err
	}
	if !proceed {
		_, _ = fmt.Fprintln(env.Stderr, "Aborted.")
		return nil
	}
	return executePlan(ctx, env, opts.asJSON, plan)
}

// confirmRun gates a real run behind an interactive confirmation: at a TTY
// (without --yes) it prints a one-line summary and reads a y/N answer, proceeding
// only on an explicit yes — so a user is never surprised by the multi-minute
// workshop launch. A non-interactive caller (a pipe, CI) or --yes proceeds
// without prompting, keeping scripts and automation unblocked; --dry-run never
// reaches here.
func confirmRun(env Env, opts *runOptions, plan runPlan) (bool, error) {
	if !runNeedsConfirm(isInteractive(env), opts) {
		return true, nil
	}
	return promptConfirm(env, plan)
}

// runNeedsConfirm reports whether a real run should pause for interactive
// confirmation: only when attached to a TTY and --yes was not passed.
func runNeedsConfirm(interactive bool, opts *runOptions) bool {
	return interactive && !opts.yes
}

// promptConfirm prints a one-line run summary to stderr and reads a y/N answer
// from stdin, returning true only on an explicit yes. A blank line (the default),
// EOF, or anything else declines, so an accidental Enter never launches a run.
func promptConfirm(env Env, plan runPlan) (bool, error) {
	target := fmt.Sprintf("workflow %q", plan.workflow)
	if plan.adhoc {
		target = "an ad-hoc prompt"
	}
	msg := fmt.Sprintf("About to run %s on branch %q (agent %s, workshop %s). Continue? [y/N] ",
		target, plan.branch, plan.runnerConfig.Agent.Name(), plan.runnerConfig.Workshop)
	return promptYesNo(env, msg)
}

// promptYesNo prints message to stderr and reads a y/N answer from stdin,
// returning true only on an explicit "y"/"yes". A blank line, EOF, or anything
// else declines. A non-EOF read error is returned so the caller can decide.
func promptYesNo(env Env, message string) (bool, error) {
	_, _ = fmt.Fprint(env.Stderr, message)
	line, err := bufio.NewReader(env.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// adhocLabel slugs an ad-hoc run (one with no named workflow) in its branch name
// and the dry-run plan.
const adhocLabel = "adhoc"

// runSelection is the outcome of choosing what a run targets: a named workflow
// (positional or the configured default-workflow) or an ad-hoc run off the
// top-level defaults. The label slugs the branch and names the run in the plan
// and errors; wf is the selected workflow block (the zero Workflow for an ad-hoc
// run, whose params come entirely from the top level and the flags).
type runSelection struct {
	label string
	wf    taboo.Workflow
	adhoc bool
}

// describe names the selection for an error message: a quoted workflow name, or a
// plain "ad-hoc run" (which has no name to quote).
func (s runSelection) describe() string {
	if s.adhoc {
		return "ad-hoc run"
	}
	return fmt.Sprintf("workflow %q", s.label)
}

// selectRun decides what a run invocation targets, applying the selection
// precedence: an explicit positional workflow, else an ad-hoc run when a prompt
// flag is set (gated on an agent being resolvable from --agent or top-level
// config), else the configured default-workflow, else an error listing what the
// user could have picked. It never guesses — a bare run with no default-workflow
// refuses rather than run an arbitrary workflow.
func selectRun(cfg *taboo.ProjectConfig, args []string, opts *runOptions) (runSelection, error) {
	if len(args) == 1 {
		name := args[0]
		wf, ok := cfg.Workflows[name]
		if !ok {
			return runSelection{}, unknownWorkflowError(cfg, name)
		}
		return runSelection{label: name, wf: wf}, nil
	}
	if opts.prompt != "" || opts.promptFile != "" {
		// An ad-hoc run has no workflow, so its effective agent is the flag over the
		// top level (the zero Workflow contributes nothing).
		if effectiveAgent(cfg, taboo.Workflow{}, opts) == "" {
			return runSelection{}, errors.New("an ad-hoc run (--prompt/--prompt-file with no workflow) needs a top-level agent: in " +
				"taboo.yaml — set one (or pass --agent), or name a workflow")
		}
		return runSelection{label: adhocLabel, adhoc: true}, nil
	}
	if cfg.DefaultWorkflow != "" {
		wf, ok := cfg.Workflows[cfg.DefaultWorkflow]
		if !ok {
			return runSelection{}, fmt.Errorf("default-workflow %q is not defined (configured workflows: %s)",
				cfg.DefaultWorkflow, availableWorkflows(cfg))
		}
		return runSelection{label: cfg.DefaultWorkflow, wf: wf}, nil
	}
	return runSelection{}, noSelectionError(cfg)
}

// unknownWorkflowError reports a positional workflow the config does not define,
// naming the configured workflows (or that none exist).
func unknownWorkflowError(cfg *taboo.ProjectConfig, name string) error {
	if len(cfg.Workflows) == 0 {
		return fmt.Errorf("unknown workflow %q — no workflows are configured in taboo.yaml", name)
	}
	return fmt.Errorf("unknown workflow %q (configured workflows: %s)", name, availableWorkflows(cfg))
}

// noSelectionError reports a bare `taboo run` with nothing to select: no
// positional workflow, no prompt flag, and no default-workflow. It lists the
// available workflows (or that none exist) so the user knows what to name.
func noSelectionError(cfg *taboo.ProjectConfig) error {
	if len(cfg.Workflows) == 0 {
		return errors.New("no workflow given and none configured — add a workflows: block, or pass --prompt for an ad-hoc run")
	}
	return fmt.Errorf("no workflow given and no default-workflow set (available workflows: %s); "+
		"name one, set default-workflow, or pass --prompt", availableWorkflows(cfg))
}

// loadProjectConfig discovers the project's taboo.yaml from the working
// directory and loads it. A missing config is a clear, actionable error (run
// init) rather than an opaque not-found, because run is the first command an
// agent reaches for and the remedy is always the same.
func loadProjectConfig(env Env) (string, *taboo.ProjectConfig, error) {
	wd, err := env.Getwd()
	if err != nil {
		return "", nil, fmt.Errorf("determine working directory: %w", err)
	}
	path, found := findConfig(wd, statFileExists)
	if !found {
		return "", nil, fmt.Errorf("no taboo.yaml found from %s — run `taboo init` first", wd)
	}
	cfg, err := taboo.LoadConfig(path)
	if err != nil {
		return "", nil, err
	}
	return path, cfg, nil
}

// resolvePlan translates the loaded config + the selection + the flags into a
// runPlan. It bridges two shapes: the library consumes a flat Config +
// OrchestratedRequest, while the config layers flags over workflow over top
// level. Every precedence rule and required-field refusal lives here so both
// --dry-run and a real run resolve identically. The workflow lookup already
// happened in selectRun, so sel.wf is the chosen block (zero for an ad-hoc run).
func resolvePlan(cfg *taboo.ProjectConfig, configPath string, sel runSelection, opts *runOptions) (runPlan, error) {
	// A non-nil defaults lets the resolvers drop their nil guards: the config may
	// omit the defaults block entirely, so substitute an empty one here.
	defaults := cfg.Defaults
	if defaults == nil {
		defaults = &taboo.RunDefaults{}
	}

	profile, err := resolveRunProfile(cfg, sel, opts)
	if err != nil {
		return runPlan{}, err
	}

	base := filepath.Dir(configPath)
	prompt, err := resolvePrompt(opts, sel.wf, defaults, base)
	if errors.Is(err, errNoPrompt) {
		return runPlan{}, fmt.Errorf("%s has no prompt (set prompt or prompt-file)", sel.describe())
	}
	if err != nil {
		return runPlan{}, fmt.Errorf("%s: read prompt-file: %w", sel.describe(), err)
	}

	prompt, err = injectVars(prompt, opts, base)
	if err != nil {
		return runPlan{}, fmt.Errorf("%s: %w", sel.describe(), err)
	}

	repoPath, err := filepath.Abs(cfg.Repo)
	if err != nil {
		return runPlan{}, fmt.Errorf("resolve repo path %q: %w", cfg.Repo, err)
	}

	plan := runPlan{
		workflow: sel.label,
		runnerConfig: taboo.Config{
			Workshop:   taboo.WorkshopName(cfg.Workshop, profile.Name()),
			Base:       cfg.Base,
			Agent:      profile,
			RepoPath:   repoPath,
			ProjectDir: base,
		},
		model:            resolveModel(cfg, sel, opts),
		prompt:           prompt,
		timeout:          resolveTimeout(opts, sel.wf, defaults),
		maxIterations:    resolveMaxIterations(opts, sel.wf, defaults),
		completionSignal: resolveSignal(opts, defaults),
		adhoc:            sel.adhoc,
	}
	plan.branch = resolveBranch(opts.branch, defaults.BranchPrefix, sel.label)
	return plan, nil
}

// resolveRunProfile resolves the run's agent profile from the effective agent and
// model, applying the full precedence chain (top-level config -> workflow ->
// CLI flags) to each. The agent and model are re-resolved here rather than reused
// from LoadConfig's wf.Profile so a --model (or --agent) flag can override them;
// with no flags the result is identical to the loaded profile. A missing agent
// anywhere is refused, and an unknown agent (only reachable via a flag, since
// LoadConfig already validated config agents) is surfaced through unknownAgentError,
// with a fuzzy "did you mean" suggestion when a registered agent is close.
func resolveRunProfile(cfg *taboo.ProjectConfig, sel runSelection, opts *runOptions) (taboo.AgentProfile, error) {
	agent := effectiveAgent(cfg, sel.wf, opts)
	if agent == "" {
		return nil, fmt.Errorf("%s has no agent configured (set agent: on the workflow or a top-level agent:)", sel.describe())
	}
	profile, err := taboo.NewProfile(agent, resolveModel(cfg, sel, opts))
	if err != nil {
		return nil, unknownAgentError(agent, err)
	}
	return profile, nil
}

// resolveModel applies the model precedence chain (top-level config -> workflow ->
// --model flag). It is the single source of that precedence, shared by the
// profile resolution and the dry-run plan so both report the same model.
func resolveModel(cfg *taboo.ProjectConfig, sel runSelection, opts *runOptions) string {
	return orElse(opts.model, orElse(sel.wf.Model, cfg.Model))
}

// effectiveAgent applies the agent precedence chain (top-level config -> workflow
// -> --agent flag) to one workflow block. The ad-hoc gate in selectRun passes the
// zero Workflow (an ad-hoc run has none), so it reduces to flag-then-top-level
// there, while resolveRunProfile passes the selected block. Sharing it keeps the
// ad-hoc gate and the profile resolution from drifting apart.
func effectiveAgent(cfg *taboo.ProjectConfig, wf taboo.Workflow, opts *runOptions) string {
	return orElse(opts.agent, orElse(wf.Agent, cfg.Agent))
}

// unknownAgentError turns NewProfile's wrapped ErrUnknownAgent into a CLI message
// with a fuzzy "did you mean" suggestion when a registered agent is close enough,
// reusing unknownAgentMessage (the same builder validate uses). When nothing is
// close, the original wrapped error (which already names the bad agent) is
// returned unchanged so callers can still errors.Is it.
func unknownAgentError(name string, err error) error {
	if msg, ok := unknownAgentMessage(name, taboo.AgentNames()); ok {
		return errors.New(msg)
	}
	return err
}

// unknownAgentMessage builds the "unknown agent X" report and reports whether a
// candidate was close enough to append a fuzzy "did you mean Y?" hint. It is the
// single source of that message, shared by validate's agentChecks (which always
// uses the text) and run's unknownAgentError (which only overrides its wrapped
// sentinel when a suggestion exists), so the two surface an identical message.
func unknownAgentMessage(name string, candidates []string) (string, bool) {
	msg := fmt.Sprintf("unknown agent %q", name)
	suggestion, ok := suggestAgent(name, candidates)
	if ok {
		msg += fmt.Sprintf("; did you mean %q?", suggestion)
	}
	return msg, ok
}

// availableWorkflows lists the config's workflow names, sorted, for the
// unknown-workflow error so the user sees exactly what they can pick.
func availableWorkflows(cfg *taboo.ProjectConfig) string {
	names := make([]string, 0, len(cfg.Workflows))
	for name := range cfg.Workflows {
		names = append(names, name)
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

// resolvePrompt applies the prompt precedence (flag inline → workflow inline →
// workflow file → defaults inline → defaults file), returning the first non-empty
// resolution. A prompt-file path is read relative to the config file's directory
// (the same base validate uses), so a relative path resolves identically here and
// in the preflight. No prompt anywhere is an error — there is nothing for the
// agent to do.
func resolvePrompt(opts *runOptions, wf taboo.Workflow, defaults *taboo.RunDefaults, base string) (string, error) {
	if opts.prompt != "" {
		return opts.prompt, nil
	}
	if opts.promptFile != "" {
		return readPromptFile(opts.promptFile, base)
	}
	if wf.Prompt != "" {
		return wf.Prompt, nil
	}
	if wf.PromptFile != "" {
		return readPromptFile(wf.PromptFile, base)
	}
	if defaults.Prompt != "" {
		return defaults.Prompt, nil
	}
	if defaults.PromptFile != "" {
		return readPromptFile(defaults.PromptFile, base)
	}
	return "", errNoPrompt
}

// injectVars fills the prompt's {{VAR}} placeholders from --vars-file and --var
// (--var wins on a key clash). With no vars it returns the prompt unchanged, so a
// prompt that legitimately contains {{...}} is left alone. It uses the pure
// taboo.Substitute (a single literal pass), not the shell-expanding
// PromptTemplate.Resolve, so injected values are never shell-expanded or
// re-substituted — safe for untrusted text. See docs/run-vars.md.
func injectVars(prompt string, opts *runOptions, base string) (string, error) {
	vars, err := resolveVars(opts, base)
	if err != nil {
		return "", err
	}
	if len(vars) == 0 {
		return prompt, nil
	}
	return taboo.Substitute(prompt, vars)
}

// resolveVars gathers the run's caller-supplied template variables, layering the
// repeatable --var KEY=VALUE flags on top of the --vars-file JSON object so a --var
// overrides a matching file key. The vars-file path is relative to the config dir
// (like --prompt-file); a missing file or malformed JSON fails fast with a clear
// error so a half-formed injection never reaches the agent.
func resolveVars(opts *runOptions, base string) (map[string]string, error) {
	vars := map[string]string{}
	if opts.varsFile != "" {
		path := resolvePromptFilePath(opts.varsFile, base)
		data, err := os.ReadFile(path) // #nosec G304 -- caller-supplied vars path, read as literal text only
		if err != nil {
			return nil, fmt.Errorf("read vars-file: %w", err)
		}
		if err := json.Unmarshal(data, &vars); err != nil {
			return nil, fmt.Errorf("parse vars-file %s: %w", opts.varsFile, err)
		}
	}
	for _, kv := range opts.vars {
		key, val, ok := strings.Cut(kv, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid --var %q: want KEY=VALUE", kv)
		}
		vars[key] = val
	}
	return vars, nil
}

// readPromptFile returns the contents of a prompt file, resolving a relative
// path against base. The contents are used verbatim as the prompt.
func readPromptFile(path, base string) (string, error) {
	resolved := resolvePromptFilePath(path, base)
	// resolved derives from a trusted config path under a trusted base, not from
	// untrusted input.
	data, err := os.ReadFile(resolved) // #nosec G304
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// resolvePromptFilePath resolves a configured prompt-file path: absolute paths
// are used verbatim, relative ones resolve against base (the config file's
// directory). Both run (readPromptFile) and validate (promptFileChecks) share
// this so a relative prompt-file resolves identically in both.
func resolvePromptFilePath(path, base string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(base, path)
}

// resolveTimeout applies flag-then-workflow-then-defaults precedence for the
// per-exec timeout (zero means no timeout).
func resolveTimeout(opts *runOptions, wf taboo.Workflow, defaults *taboo.RunDefaults) time.Duration {
	if opts.timeout > 0 {
		return opts.timeout
	}
	if wf.Timeout != 0 {
		return time.Duration(wf.Timeout)
	}
	return time.Duration(defaults.Timeout)
}

// resolveMaxIterations applies flag-then-workflow-then-defaults precedence for
// the iteration cap (zero or negative means a single run; the Orchestrator floors
// it).
func resolveMaxIterations(opts *runOptions, wf taboo.Workflow, defaults *taboo.RunDefaults) int {
	if opts.iterations > 0 {
		return opts.iterations
	}
	if wf.MaxIterations > 0 {
		return wf.MaxIterations
	}
	return defaults.MaxIterations
}

// resolveSignal applies flag-then-defaults precedence for the completion signal.
// There is no workflow-level signal field, so this is a two-layer chain (the
// completion signal lives only in the defaults block and as a CLI flag).
func resolveSignal(opts *runOptions, defaults *taboo.RunDefaults) string {
	if opts.signal != "" {
		return opts.signal
	}
	return defaults.CompletionSignal
}

// resolveBranch returns the run's branch: the override verbatim when set, else a
// fresh name composed of the prefix, the workflow, and a timestamp so every run
// lands on its own branch and never collides with a prior one. The nanosecond
// component keeps two back-to-back runs of the same workflow within the same
// wall-clock second from producing an identical branch (which would make the
// real git worktree add fail on a name clash).
func resolveBranch(override, prefix, workflow string) string {
	if override != "" {
		return override
	}
	now := time.Now()
	return fmt.Sprintf("%s%s-%s-%09d", prefix, workflow, now.Format("20060102-150405"), now.Nanosecond())
}

// runPreflight gathers the lightweight host probe (is workshop callable) plus
// the run-scoped config-correctness checks and refuses the run when any errors.
// The checks are run-scoped (runConfigChecks, not the full validateChecks): they
// skip prompt-file existence, which resolvePlan already proved for the one file
// this run consumes. The report goes to stderr (not stdout) so a refusal does not
// pollute the machine result stream a successful run writes there. It returns
// errRunFailed so main exits non-zero without echoing cobra noise.
func runPreflight(ctx context.Context, env Env) error {
	checks := []check{checkWorkshop(ctx, env)}
	checks = append(checks, runConfigChecks(ctx, env, statFileExists)...)
	if anyError(checks) {
		writeHuman(env.Stderr, "taboo run — preflight checks", checks)
		return errRunFailed
	}
	return nil
}

// executePlan builds a Runner + Orchestrator from the plan and runs it. Live
// agent output (stdout and stderr both) is routed to env.Stderr to keep the
// machine result clean on env.Stdout; a brief start line goes to stderr too so an
// interactive caller sees the run begin. On success the machine result is written
// to stdout; a failure inside the run is printed to stderr and returned (exit 1),
// mirroring init.
func executePlan(ctx context.Context, env Env, asJSON bool, plan runPlan) error {
	target := fmt.Sprintf("workflow %q", plan.workflow)
	if plan.adhoc {
		target = "ad-hoc prompt"
	}
	_, _ = fmt.Fprintf(env.Stderr, "Running %s on branch %q (agent %s)…\n", target, plan.branch, plan.runnerConfig.Agent.Name())

	runner := taboo.New(plan.runnerConfig, env.Cmd)
	orch := taboo.NewOrchestrator(runner)
	req := taboo.OrchestratedRequest{
		RunRequest: taboo.RunRequest{
			Branch:  plan.branch,
			Prompt:  plan.prompt,
			Timeout: plan.timeout,
			Stdout:  env.Stderr,
			Stderr:  env.Stderr,
		},
		MaxIterations:    plan.maxIterations,
		CompletionSignal: plan.completionSignal,
	}
	res, err := orch.Run(ctx, req)
	if err != nil {
		_, _ = fmt.Fprintln(env.Stderr, "Error:", err)
		return err
	}
	return writeRunResult(env, asJSON, res)
}

// jsonRunResult is the --json machine result shape. It is a deliberately flat
// projection of OrchestratedResult: the fields a caller scripts against, with the
// StopReason flattened to a string.
type jsonRunResult struct {
	Branch     string `json:"branch"`
	Commit     string `json:"commit"`
	Worktree   string `json:"worktree"`
	Output     string `json:"output"`
	Iterations int    `json:"iterations"`
	StopReason string `json:"stopReason"`
}

// writeRunResult writes the run's machine result to stdout in the requested
// format. The plain form is just branch + commit — the two values a caller
// usually wants — and deliberately omits the captured agent output: that output
// already streamed live to stderr during the run, and echoing the captured copy
// back onto stdout would defeat the clean-stdout contract (a caller parsing
// stdout must not have to skip past arbitrary agent chatter). The JSON form keeps
// `output` so a structured consumer can still read it.
func writeRunResult(env Env, asJSON bool, res taboo.OrchestratedResult) error {
	if asJSON {
		enc := json.NewEncoder(env.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(jsonRunResult{
			Branch:     res.Branch,
			Commit:     res.Commit,
			Worktree:   res.WorktreePath,
			Output:     res.Output,
			Iterations: res.Iterations,
			StopReason: string(res.StopReason),
		})
	}
	_, _ = fmt.Fprintf(env.Stdout, "branch: %s\n", res.Branch)
	_, _ = fmt.Fprintf(env.Stdout, "commit: %s\n", res.Commit)
	return nil
}

// printPlan renders the resolved plan to stdout for --dry-run: the workflow,
// branch, agent, and the scalar run params, so a user can confirm what a real run
// would do without any host side effects. Every label is padded to one width so
// the values line up in a single column; the longest label
// ("completion-signal:") sets that width.
func printPlan(env Env, plan runPlan) {
	_, _ = fmt.Fprintln(env.Stdout, "taboo run (dry run) — resolved plan:")
	planLabel, planTarget := "workflow:", plan.workflow
	if plan.adhoc {
		planLabel, planTarget = "run:", "ad-hoc (--prompt)"
	}
	_, _ = fmt.Fprintf(env.Stdout, "  %-18s %s\n", planLabel, planTarget)
	_, _ = fmt.Fprintf(env.Stdout, "  %-18s %s\n", "branch:", plan.branch)
	_, _ = fmt.Fprintf(env.Stdout, "  %-18s %s\n", "agent:", plan.runnerConfig.Agent.Name())
	_, _ = fmt.Fprintf(env.Stdout, "  %-18s %s\n", "model:", plan.model)
	_, _ = fmt.Fprintf(env.Stdout, "  %-18s %s\n", "workshop:", plan.runnerConfig.Workshop)
	_, _ = fmt.Fprintf(env.Stdout, "  %-18s %s\n", "repo:", plan.runnerConfig.RepoPath)
	_, _ = fmt.Fprintf(env.Stdout, "  %-18s %s\n", "timeout:", plan.timeout)
	_, _ = fmt.Fprintf(env.Stdout, "  %-18s %d\n", "max-iterations:", plan.maxIterations)
	_, _ = fmt.Fprintf(env.Stdout, "  %-18s %s\n", "completion-signal:", plan.completionSignal)
	_, _ = fmt.Fprintf(env.Stdout, "  %-18s %s\n", "prompt:", promptSummary(plan.prompt))
}

// promptSummary renders a prompt on one line so a multi-line (often
// prompt-file-backed) prompt cannot shatter printPlan's aligned column. It shows
// the first line, truncated to 60 runes with a trailing ellipsis when longer,
// and appends a line count whenever the prompt spans multiple lines or was
// truncated so the reader knows the displayed text is only a preview.
func promptSummary(prompt string) string {
	first := prompt
	multiline := false
	if i := strings.IndexByte(prompt, '\n'); i >= 0 {
		first = prompt[:i]
		multiline = true
	}

	runes := []rune(first)
	truncated := false
	if len(runes) > 60 {
		first = string(runes[:60]) + "…"
		truncated = true
	}

	if multiline || truncated {
		lines := strings.Count(prompt, "\n") + 1
		unit := "lines"
		if lines == 1 {
			unit = "line"
		}
		return fmt.Sprintf("%s (%d %s)", first, lines, unit)
	}
	return first
}
