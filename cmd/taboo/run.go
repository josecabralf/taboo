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

	taboo "github.com/josecabralf/taboo/pkg"
)

// errRunFailed is the sentinel run returns when its preflight finds an error
// (workshop unreachable, or the config fails validate). The preflight report is
// printed to stderr first; main maps the sentinel to a non-zero exit. It mirrors
// doctor's errChecksFailed but is run-specific so a caller can distinguish a
// preflight refusal from a failure inside the run itself.
var errRunFailed = errors.New("run: preflight failed")

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
	// from selects the workshop definition to derive the agent workshop from,
	// overriding taboo.yaml's source-definition.
	from string
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
	cmd.Flags().StringVar(&opts.from, "from", "", "the workshop definition to derive the agent workshop from; overrides taboo.yaml source-definition")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "resolve and print the plan without running anything")
	cmd.Flags().BoolVar(&opts.yes, "yes", false, "skip the interactive pre-run confirmation")
	cmd.Flags().BoolVar(&opts.asJSON, "json", false, "emit the run result as JSON")
	return cmd
}

// runRun is the run command's select-resolve-preflight-execute flow. It discovers
// and loads the config, selects what to run (named workflow, ad-hoc, or default),
// resolves that into a plan via the pkg/taboo config→run bridge, and then either
// prints the plan (--dry-run) or runs a host preflight and executes it. Each
// stage's failure is surfaced before the next, so a misconfigured project never
// reaches the workshop.
func runRun(ctx context.Context, env Env, opts *runOptions, args []string) error {
	configPath, cfg, err := loadProjectConfig(env)
	if err != nil {
		return err
	}
	sel, err := selectRun(cfg, args, opts)
	if err != nil {
		return err
	}
	configDir := filepath.Dir(configPath)
	vars, err := resolveVars(opts, configDir)
	if err != nil {
		return fmt.Errorf("%s: %w", sel.describe(), err)
	}
	plan, err := cfg.Plan(configDir, sel.workflowName(), vars, planOverrides(env, opts))
	if err != nil {
		return mapPlanError(cfg, sel, opts, err)
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
	return executeRun(ctx, env, opts.asJSON, plan)
}

// mapPlanError translates the bridge's library-owned sentinels into the CLI's
// existing user-facing wording. The bridge owns resolution and returns neutral,
// errors.Is-matchable sentinels; the CLI re-adds the run-command phrasing — the
// selection-scoped no-prompt and no-agent hints and the fuzzy unknown-agent
// suggestion — that the neutral sentinels drop. Non-sentinel errors pass through
// verbatim.
func mapPlanError(cfg *taboo.ProjectConfig, sel runSelection, opts *runOptions, err error) error {
	switch {
	case errors.Is(err, taboo.ErrNoPrompt):
		return fmt.Errorf("%s has no prompt (set prompt or prompt-file)", sel.describe())
	case errors.Is(err, taboo.ErrNoAgent):
		return fmt.Errorf("%s has no agent configured (set agent: on the workflow or a top-level agent:)", sel.describe())
	case errors.Is(err, taboo.ErrUnknownAgent):
		return unknownAgentError(effectiveAgent(cfg, sel.wf, opts), err)
	case errors.Is(err, taboo.ErrUnknownWorkflow):
		return unknownWorkflowError(cfg, sel.label)
	default:
		return err
	}
}

// planOverrides packs the CLI flags into the bridge's PlanOverrides. The agent's
// live output streams to stderr (both sinks point at env.Stderr) so the machine
// result on stdout stays clean.
func planOverrides(env Env, opts *runOptions) taboo.PlanOverrides {
	return taboo.PlanOverrides{
		Agent: opts.agent, Model: opts.model,
		Timeout: opts.timeout, MaxIterations: opts.iterations,
		CompletionSignal: opts.signal, Branch: opts.branch, From: opts.from,
		Prompt: opts.prompt, PromptFile: opts.promptFile,
		Stdout: env.Stderr, Stderr: env.Stderr,
	}
}

// confirmRun gates a real run behind an interactive confirmation: at a TTY
// (without --yes) it prints a one-line summary and reads a y/N answer, proceeding
// only on an explicit yes — so a user is never surprised by the multi-minute
// workshop launch. A non-interactive caller (a pipe, CI) or --yes proceeds
// without prompting, keeping scripts and automation unblocked; --dry-run never
// reaches here.
func confirmRun(env Env, opts *runOptions, plan *taboo.Plan) (bool, error) {
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
func promptConfirm(env Env, plan *taboo.Plan) (bool, error) {
	target := fmt.Sprintf("workflow %q", plan.Workflow)
	if plan.Workflow == "" {
		target = "an ad-hoc prompt"
	}
	msg := fmt.Sprintf("About to run %s on branch %q (agent %s, workshop %s). Continue? [y/N] ",
		target, plan.Request.Branch, plan.Config.Agent.Name(), plan.Config.Workshop)
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

// workflowName is the bridge's workflow argument: the selected name, or "" for an
// ad-hoc run.
func (s runSelection) workflowName() string {
	if s.adhoc {
		return ""
	}
	return s.label
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
	path, found := taboo.FindConfig(wd)
	if !found {
		return "", nil, fmt.Errorf("no taboo.yaml found from %s — run `taboo init` first", wd)
	}
	cfg, err := taboo.LoadConfig(path)
	if err != nil {
		return "", nil, err
	}
	return path, cfg, nil
}

// effectiveAgent applies the agent precedence chain (top-level config -> workflow
// -> --agent flag) to one workflow block. The ad-hoc gate in selectRun passes the
// zero Workflow (an ad-hoc run has none), so it reduces to flag-then-top-level
// there, while mapPlanError passes the selected block to name the agent the
// bridge rejected. Sharing it keeps the ad-hoc gate and that message aligned with
// the bridge's own agent precedence.
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

// resolvePromptFilePath resolves a config-relative file path: absolute paths are
// used verbatim, relative ones resolve against base (the config file's
// directory). Run's resolveVars (for --vars-file) and validate (promptFileChecks)
// share this so a relative path resolves identically in both.
func resolvePromptFilePath(path, base string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(base, path)
}

// runPreflight gathers the lightweight host probe (is workshop callable) plus
// the run-scoped config-correctness checks and refuses the run when any errors.
// The checks are run-scoped (runConfigChecks, not the full validateChecks): they
// skip prompt-file existence, which cfg.Plan already proved for the one file
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

// executeRun drives a resolved *taboo.Plan end-to-end via the bridge's Run. Live
// agent output (the Plan already routes both streams to env.Stderr via the
// overrides) keeps the machine result clean on env.Stdout; a brief start line goes
// to stderr too so an interactive caller sees the run begin. On success the
// machine result is written to stdout; a failure inside the run is printed to
// stderr and returned (exit 1), mirroring init.
func executeRun(ctx context.Context, env Env, asJSON bool, plan *taboo.Plan) error {
	target := fmt.Sprintf("workflow %q", plan.Workflow)
	if plan.Workflow == "" {
		target = "ad-hoc prompt"
	}
	_, _ = fmt.Fprintf(env.Stderr, "Running %s on branch %q (agent %s)…\n", target, plan.Request.Branch, plan.Config.Agent.Name())
	res, err := plan.Run(ctx, env.Cmd)
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
func printPlan(env Env, plan *taboo.Plan) {
	_, _ = fmt.Fprintln(env.Stdout, "taboo run (dry run) — resolved plan:")
	planLabel, planTarget := "workflow:", plan.Workflow
	if plan.Workflow == "" {
		planLabel, planTarget = "run:", "ad-hoc (--prompt)"
	}
	_, _ = fmt.Fprintf(env.Stdout, "  %-18s %s\n", planLabel, planTarget)
	_, _ = fmt.Fprintf(env.Stdout, "  %-18s %s\n", "branch:", plan.Request.Branch)
	_, _ = fmt.Fprintf(env.Stdout, "  %-18s %s\n", "agent:", plan.Config.Agent.Name())
	_, _ = fmt.Fprintf(env.Stdout, "  %-18s %s\n", "model:", plan.Model)
	_, _ = fmt.Fprintf(env.Stdout, "  %-18s %s\n", "workshop:", plan.Config.Workshop)
	_, _ = fmt.Fprintf(env.Stdout, "  %-18s %s\n", "repo:", plan.Config.RepoPath)
	if plan.Config.SourceDefinition != "" {
		_, _ = fmt.Fprintf(env.Stdout, "  %-18s %s\n", "source-definition:", plan.Config.SourceDefinition)
	}
	_, _ = fmt.Fprintf(env.Stdout, "  %-18s %s\n", "timeout:", plan.Request.Timeout)
	_, _ = fmt.Fprintf(env.Stdout, "  %-18s %d\n", "max-iterations:", plan.Request.MaxIterations)
	_, _ = fmt.Fprintf(env.Stdout, "  %-18s %s\n", "completion-signal:", plan.Request.CompletionSignal)
	_, _ = fmt.Fprintf(env.Stdout, "  %-18s %s\n", "prompt:", promptSummary(plan.Request.Prompt))
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
