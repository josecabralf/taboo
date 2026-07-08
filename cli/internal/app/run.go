package app

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

	"github.com/josecabralf/taboo"
)

// errRunFailed is the sentinel run returns when its preflight finds an error. It
// is run-specific (unlike doctor's errChecksFailed) so a caller can distinguish a
// preflight refusal from a failure inside the run itself.
var errRunFailed = errors.New("run: preflight failed")

// runOptions are the parsed flags for the run subcommand: the highest-precedence
// layer of run-param resolution (top-level config -> workflow -> these flags).
type runOptions struct {
	prompt string
	// promptFile takes effect only when prompt is empty.
	promptFile string
	agent      string
	model      string
	// timeout of zero leaves the config layers in charge.
	timeout time.Duration
	// iterations of zero or less leaves the config layers in charge.
	iterations int
	signal     string
	// stopOnNoChange is enable-only: false leaves the config layers in charge and
	// cannot disable a config-level enable.
	stopOnNoChange bool
	branch         string
	from           string
	dryRun         bool
	yes            bool
	asJSON         bool
	// varsFile is a JSON file of {"VAR":"value"} pairs substituted literally into
	// {{VAR}} placeholders (no shell expansion of the values).
	varsFile string
	// vars are repeatable KEY=VALUE variables that override matching --vars-file keys.
	vars []string
}

// newRunCmd builds the `run` subcommand.
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
	cmd.Flags().BoolVar(&opts.stopOnNoChange, "stop-on-no-change", false, "stop the iteration loop early when an iteration produces no new commit")
	cmd.Flags().StringVar(&opts.branch, "branch", "", "branch name for this run (default: auto-generated from the workflow name — or \"adhoc\" for a --prompt run — and a timestamp)")
	cmd.Flags().StringVar(&opts.from, "from", "", "the workshop definition to derive the agent workshop from; overrides taboo.yaml source-definition")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "resolve and print the plan without running anything")
	cmd.Flags().BoolVar(&opts.yes, "yes", false, "skip the interactive pre-run confirmation")
	cmd.Flags().BoolVar(&opts.asJSON, "json", false, "emit the run result (or, with --dry-run, the resolved plan) as JSON")
	return cmd
}

// runRun is the run command's select-resolve-preflight-execute flow. The dry-run
// branch returns before warnPromptVars and the preflight, so it stays host-free
// and warning-free on stderr; its JSON document carries the vars state itself.
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
		if opts.asJSON {
			return writeIndentedJSON(env.Stdout, planToJSON(plan, vars))
		}
		printPlan(env, plan, vars)
		return nil
	}

	warnPromptVars(env, plan, vars)
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

// mapPlanError translates the bridge's neutral sentinels into the CLI's
// user-facing wording (the selection-scoped no-prompt/no-agent hints and the
// fuzzy unknown-agent suggestion). Non-sentinel errors pass through verbatim.
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

// planOverrides packs the CLI flags into the bridge's PlanOverrides. Both output
// sinks point at env.Stderr so the machine result on stdout stays clean.
func planOverrides(env Env, opts *runOptions) taboo.PlanOverrides {
	return taboo.PlanOverrides{
		Agent: taboo.AgentName(opts.agent), Model: opts.model,
		Timeout: opts.timeout, MaxIterations: opts.iterations,
		CompletionSignal: opts.signal, StopOnNoChange: opts.stopOnNoChange,
		Branch: opts.branch, From: opts.from,
		Prompt: opts.prompt, PromptFile: opts.promptFile,
		Stdout: env.Stderr, Stderr: env.Stderr,
	}
}

// confirmRun gates a real run behind an interactive confirmation so a user is
// never surprised by the multi-minute workshop launch. A non-interactive caller
// or --yes proceeds without prompting.
func confirmRun(env Env, opts *runOptions, plan *taboo.Plan) (bool, error) {
	if !isInteractive(env) || opts.yes {
		return true, nil
	}
	return promptConfirm(env, plan)
}

// promptConfirm prints a one-line run summary to stderr and reads a y/N answer,
// returning true only on an explicit yes so an accidental Enter never launches a
// run.
func promptConfirm(env Env, plan *taboo.Plan) (bool, error) {
	target := fmt.Sprintf("workflow %q", plan.Workflow)
	if plan.Workflow == "" {
		target = "an ad-hoc prompt"
	}
	msg := fmt.Sprintf("About to run %s on branch %q (agent %s, workshop %s). Continue? [y/N] ",
		target, plan.Request.Branch, plan.Config.Agent.Name(), plan.Config.Workshop)
	return promptYesNo(env, msg)
}

// promptYesNo prints message to stderr and reads a y/N answer, returning true only
// on an explicit `y`/`yes`. A non-EOF read error is returned so the caller can
// decide.
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

// adhocLabel slugs an ad-hoc run in its branch name and the dry-run plan.
const adhocLabel = "adhoc"

// runSelection is the outcome of choosing what a run targets: a named workflow or
// an ad-hoc run off the top-level defaults; wf is the zero Workflow for an ad-hoc
// run, whose params come entirely from the top level and the flags.
type runSelection struct {
	label string
	wf    taboo.Workflow
	adhoc bool
}

// describe names the selection for an error message: a quoted workflow name, or a
// plain `ad-hoc run`.
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

// selectRun decides what a run invocation targets by precedence: a positional
// workflow, else an ad-hoc run when a prompt flag is set (gated on a resolvable
// agent), else the configured default-workflow, else an error. It never guesses:
// a bare run with no default-workflow refuses rather than pick a workflow.
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
		// top level.
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
// naming the configured workflows.
func unknownWorkflowError(cfg *taboo.ProjectConfig, name string) error {
	if len(cfg.Workflows) == 0 {
		return fmt.Errorf("unknown workflow %q — no workflows are configured in taboo.yaml", name)
	}
	return fmt.Errorf("unknown workflow %q (configured workflows: %s)", name, availableWorkflows(cfg))
}

// noSelectionError reports a bare `taboo run` with nothing to select, listing the
// available workflows so the user knows what to name.
func noSelectionError(cfg *taboo.ProjectConfig) error {
	if len(cfg.Workflows) == 0 {
		return errors.New("no workflow given and none configured — add a workflows: block, or pass --prompt for an ad-hoc run")
	}
	return fmt.Errorf("no workflow given and no default-workflow set (available workflows: %s); "+
		"name one, set default-workflow, or pass --prompt", availableWorkflows(cfg))
}

// loadProjectConfig discovers the project's taboo.yaml from the working directory
// and loads it. A missing config is an actionable `run init` error rather than an
// opaque not-found.
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

// effectiveAgent applies the agent precedence chain (--agent flag -> workflow ->
// top-level config) to one workflow block. Sharing it keeps the ad-hoc gate and
// mapPlanError's message aligned with the bridge's own agent precedence.
func effectiveAgent(cfg *taboo.ProjectConfig, wf taboo.Workflow, opts *runOptions) string {
	if opts.agent != "" {
		return opts.agent
	}
	if wf.Agent != "" {
		return string(wf.Agent)
	}
	return string(cfg.Agent)
}

// unknownAgentError turns NewProfile's wrapped ErrUnknownAgent into a CLI message
// with a fuzzy suggestion when a registered agent is close enough. When nothing is
// close, the original wrapped error is returned so callers can still errors.Is it.
func unknownAgentError(name string, err error) error {
	if msg, ok := unknownAgentMessage(name, taboo.AgentNames()); ok {
		return errors.New(msg)
	}
	return err
}

// unknownAgentMessage builds the `unknown agent X` report and reports whether a
// candidate was close enough to append a `did you mean Y?` hint. It is the single
// source of that message, shared by validate's agentChecks and run's
// unknownAgentError so the two surface an identical message.
func unknownAgentMessage(name string, candidates []string) (string, bool) {
	msg := fmt.Sprintf("unknown agent %q", name)
	suggestion, ok := suggestAgent(name, candidates)
	if ok {
		msg += fmt.Sprintf("; did you mean %q?", suggestion)
	}
	return msg, ok
}

// availableWorkflows lists the config's workflow names, sorted.
func availableWorkflows(cfg *taboo.ProjectConfig) string {
	names := make([]string, 0, len(cfg.Workflows))
	for name := range cfg.Workflows {
		names = append(names, name)
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

// resolveVars gathers the run's template variables, layering the --var KEY=VALUE
// flags over the --vars-file JSON so a --var overrides a matching file key. A
// missing file or malformed JSON fails fast so a half-formed injection never
// reaches the agent.
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

// resolvePromptFilePath resolves a config-relative file path: absolute paths
// verbatim, relative ones against base; run and validate share it so a relative
// path resolves identically in both.
func resolvePromptFilePath(path, base string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(base, path)
}

// runPreflight gathers the workshop probe plus the run-scoped config-correctness
// checks (runConfigChecks, which skip prompt-file existence cfg.Plan already
// proved) and refuses the run when any errors. The report goes to stderr so a
// refusal does not pollute the machine result stream on stdout.
func runPreflight(ctx context.Context, env Env) error {
	checks := []check{checkWorkshop(ctx, env)}
	checks = append(checks, runConfigChecks(ctx, env, statFileExists)...)
	if anyError(checks) {
		writeHuman(env.Stderr, "taboo run — preflight checks", checks)
		return errRunFailed
	}
	return nil
}

// executeRun drives a resolved *taboo.Plan end-to-end via the bridge's Run. The
// Plan routes agent output to env.Stderr, keeping the machine result clean on
// env.Stdout. On success the result is written to stdout; a run failure is
// returned and printed once by executeRoot.
func executeRun(ctx context.Context, env Env, asJSON bool, plan *taboo.Plan) error {
	target := fmt.Sprintf("workflow %q", plan.Workflow)
	if plan.Workflow == "" {
		target = "ad-hoc prompt"
	}
	_, _ = fmt.Fprintf(env.Stderr, "Running %s on branch %q (agent %s)…\n", target, plan.Request.Branch, plan.Config.Agent.Name())
	res, err := plan.Run(ctx, env.Cmd)
	if err != nil {
		return err
	}
	return writeRunResult(env, asJSON, res)
}

// jsonRunResult is the --json machine result shape, a flat projection of
// OrchestratedResult. The first five keys are frozen (#134); baseCommit and
// changed are additive (#141), appended after them so existing consumers keep
// parsing unchanged.
type jsonRunResult struct {
	Branch     string `json:"branch"`
	Commit     string `json:"commit"`
	Output     string `json:"output"`
	Iterations int    `json:"iterations"`
	StopReason string `json:"stopReason"`
	BaseCommit string `json:"baseCommit"`
	Changed    bool   `json:"changed"`
}

// writeRunResult writes the run's machine result to stdout. The plain form is just
// branch + commit; it omits the captured agent output, which already streamed to
// stderr, to keep the clean-stdout contract. The JSON form keeps `output`.
//
// A run that produced no new commits gets an advisory note on stderr (the plain
// path only), so the two-line branch/commit contract on stdout stays
// byte-identical. JSON consumers read the `changed` field instead.
func writeRunResult(env Env, asJSON bool, res taboo.OrchestratedResult) error {
	if asJSON {
		return writeIndentedJSON(env.Stdout, jsonRunResult{
			Branch:     res.Branch,
			Commit:     res.Commit,
			Output:     res.Output,
			Iterations: res.Iterations,
			StopReason: string(res.StopReason),
			BaseCommit: res.BaseCommit,
			Changed:    res.Changed(),
		})
	}
	_, _ = fmt.Fprintf(env.Stdout, "branch: %s\n", res.Branch)
	_, _ = fmt.Fprintf(env.Stdout, "commit: %s\n", res.Commit)
	if !res.Changed() {
		_, _ = fmt.Fprintln(env.Stderr, "note: the agent produced no new commits — branch tip unchanged")
	}
	return nil
}

// jsonPlanVars is the dry-run plan's vars object, a structured mirror of
// varsSummary's three states: supplied (sorted keys), unused (supplied keys
// matching no {{VAR}} placeholder), and unfilled (placeholders exist but no vars
// were supplied, so they reach the agent literally).
type jsonPlanVars struct {
	Supplied []string `json:"supplied"`
	Unused   []string `json:"unused"`
	Unfilled bool     `json:"unfilled"`
}

// jsonPlan is the --dry-run --json machine shape: printPlan's fields as one flat
// object. The prompt key carries the one-line promptSummary preview, never the
// full prompt; sourceDefinition is "" when unset (the JSON key is always present,
// unlike the human plan's omitted line); placeholders marshals as [] never null.
type jsonPlan struct {
	Workflow         string       `json:"workflow"`
	Adhoc            bool         `json:"adhoc"`
	Branch           string       `json:"branch"`
	Agent            string       `json:"agent"`
	Model            string       `json:"model"`
	Workshop         string       `json:"workshop"`
	Repo             string       `json:"repo"`
	SourceDefinition string       `json:"sourceDefinition"`
	Timeout          string       `json:"timeout"`
	MaxIterations    int          `json:"maxIterations"`
	CompletionSignal string       `json:"completionSignal"`
	StopOnNoChange   bool         `json:"stopOnNoChange"`
	Prompt           string       `json:"prompt"`
	Placeholders     []string     `json:"placeholders"`
	Vars             jsonPlanVars `json:"vars"`
}

// planToJSON projects a resolved plan and the caller-supplied vars into the
// jsonPlan machine shape. The adhoc field mirrors printPlan's label switch: true
// exactly when the human plan prints `run: ad-hoc (--prompt)`.
func planToJSON(plan *taboo.Plan, vars map[string]string) jsonPlan {
	supplied := make([]string, 0, len(vars))
	for key := range vars {
		supplied = append(supplied, key)
	}
	slices.Sort(supplied)
	return jsonPlan{
		Workflow:         plan.Workflow,
		Adhoc:            plan.Workflow == "",
		Branch:           plan.Request.Branch,
		Agent:            string(plan.Config.Agent.Name()),
		Model:            plan.Model,
		Workshop:         plan.Config.Workshop,
		Repo:             plan.Config.RepoPath,
		SourceDefinition: plan.Config.SourceDefinition,
		Timeout:          plan.Request.Timeout.String(),
		MaxIterations:    plan.Request.MaxIterations,
		CompletionSignal: plan.Request.CompletionSignal,
		StopOnNoChange:   plan.Request.StopOnNoChange,
		Prompt:           promptSummary(plan.Request.Prompt),
		Placeholders:     emptyIfNil(plan.Placeholders),
		Vars: jsonPlanVars{
			Supplied: supplied,
			Unused:   emptyIfNil(unusedVarKeys(plan.Placeholders, vars)),
			Unfilled: len(vars) == 0 && len(plan.Placeholders) > 0,
		},
	}
}

// printPlan renders the resolved plan to stdout for --dry-run. Every label is
// padded to one width so the values line up; the longest label
// (`completion-signal:`) sets that width.
func printPlan(env Env, plan *taboo.Plan, vars map[string]string) {
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
	_, _ = fmt.Fprintf(env.Stdout, "  %-18s %t\n", "stop-on-no-change:", plan.Request.StopOnNoChange)
	_, _ = fmt.Fprintf(env.Stdout, "  %-18s %s\n", "prompt:", promptSummary(plan.Request.Prompt))
	_, _ = fmt.Fprintf(env.Stdout, "  %-18s %s\n", "vars:", varsSummary(plan.Placeholders, vars))
}

// warnPromptVars surfaces the two silent vars footguns on stderr before a real
// run: supplied keys that match no {{VAR}} placeholder, and a no-vars run whose
// prompt carries placeholders (the pass-through sends them to the agent
// literally). Warnings only, on stderr before the confirmRun prompt so an
// interactive user can still abort.
func warnPromptVars(env Env, plan *taboo.Plan, vars map[string]string) {
	if unused := unusedVarKeys(plan.Placeholders, vars); len(unused) > 0 {
		_, _ = fmt.Fprintf(env.Stderr, "warning: supplied var(s) match no {{VAR}} placeholder in the prompt: %s\n",
			strings.Join(unused, ", "))
	}
	if len(vars) == 0 && len(plan.Placeholders) > 0 {
		_, _ = fmt.Fprintf(env.Stderr, "warning: the prompt references {{VAR}} placeholder(s) that will reach the agent literally: %s (supply --var/--vars-file)\n",
			strings.Join(plan.Placeholders, ", "))
	}
}

// varsSummary renders the dry-run plan's vars: line from the prompt's placeholder
// set and the supplied variables. A rendered plan has three base states: no
// placeholders; placeholders with vars supplied (all filled by construction); and
// placeholders with no vars, which pass through literally. Any supplied-but-unused
// keys are appended.
func varsSummary(placeholders []string, vars map[string]string) string {
	unused := unusedVarKeys(placeholders, vars)
	unusedSuffix := ""
	if len(unused) > 0 {
		unusedSuffix = " — unused: " + strings.Join(unused, ", ")
	}
	switch {
	case len(placeholders) == 0:
		return "(none)" + unusedSuffix
	case len(vars) > 0:
		return strings.Join(placeholders, ", ") + " (supplied)" + unusedSuffix
	default:
		return strings.Join(placeholders, ", ") + " (unfilled — will pass through literally; supply --var/--vars-file)"
	}
}

// unusedVarKeys returns the sorted supplied variable keys that match no
// placeholder, the keys Substitute silently ignores, which would otherwise vanish
// without a trace.
func unusedVarKeys(placeholders []string, vars map[string]string) []string {
	var out []string
	for key := range vars {
		if !slices.Contains(placeholders, key) {
			out = append(out, key)
		}
	}
	slices.Sort(out)
	return out
}

// promptSummary renders a prompt on one line so a multi-line prompt cannot
// shatter printPlan's aligned column. It shows the first line, truncated to 60
// runes, and appends a line count when the prompt is multi-line or truncated.
func promptSummary(prompt string) string {
	first, _, multiline := strings.Cut(prompt, "\n")

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
