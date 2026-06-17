package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// runOptions are the parsed flags for the run subcommand.
type runOptions struct {
	// branch overrides the auto-generated per-run branch verbatim.
	branch string
	// dryRun resolves the plan and prints it without touching the host.
	dryRun bool
	// asJSON emits the machine result as a JSON object instead of the plain form.
	asJSON bool
}

// newRunCmd builds the `run` subcommand: it selects one named workflow
// positionally, resolves its prompt and run params from taboo.yaml into an execution
// plan, runs a host preflight, and drives the plan end-to-end through pkg/taboo's
// Orchestrator on a fresh per-run branch. Live agent output streams to stderr so
// the machine result stays clean on stdout.
func newRunCmd(env Env) *cobra.Command {
	opts := runOptions{}
	cmd := &cobra.Command{
		Use:   "run <workflow>",
		Short: "Run a named workflow end-to-end on a fresh branch",
		Long: "run selects one workflow from taboo.yaml, resolves its prompt, timeout, " +
			"iterations, and branch from the config, and executes it through a workshop on a " +
			"new per-run branch. Agent progress streams to stderr; the run result (branch, " +
			"commit, output) is written to stdout.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRun(cmd.Context(), env, &opts, args[0])
		},
	}
	cmd.Flags().StringVar(&opts.branch, "branch", "", "branch name for this run (default: auto-generated from the workflow and a timestamp)")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "resolve and print the plan without running anything")
	cmd.Flags().BoolVar(&opts.asJSON, "json", false, "emit the run result as JSON")
	return cmd
}

// runPlan is the fully resolved description of one run: everything translated
// from the loaded config + selected workflow into the values pkg/taboo consumes.
// It is the seam between resolution (pure, testable, side-effect free) and
// execution, so --dry-run can print it without running anything.
type runPlan struct {
	workflow         string
	runnerConfig     taboo.Config
	branch           string
	prompt           string
	timeout          time.Duration
	maxIterations    int
	completionSignal string
}

// runRun is the run command's resolve-preflight-execute flow. It discovers and
// loads the config, resolves the selected workflow into a plan, and then either
// prints the plan (--dry-run) or runs a host preflight and executes it. Each
// stage's failure is surfaced before the next, so a misconfigured project never
// reaches the workshop.
func runRun(ctx context.Context, env Env, opts *runOptions, workflow string) error {
	configPath, cfg, err := loadProjectConfig(env)
	if err != nil {
		return err
	}
	plan, err := resolvePlan(cfg, configPath, workflow, opts.branch)
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
	return executePlan(ctx, env, opts.asJSON, plan)
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

// resolvePlan translates the loaded config + selected workflow into a runPlan.
// It bridges two shapes: the library consumes a flat Config +
// OrchestratedRequest, while the config layers workflow over defaults over top
// level. Every precedence rule and required-field refusal lives here so both
// --dry-run and a real run resolve identically.
func resolvePlan(cfg *taboo.ProjectConfig, configPath, workflow, branchOverride string) (runPlan, error) {
	wf, ok := cfg.Workflows[workflow]
	if !ok {
		if len(cfg.Workflows) == 0 {
			return runPlan{}, fmt.Errorf("unknown workflow %q — no workflows are configured in taboo.yaml", workflow)
		}
		return runPlan{}, fmt.Errorf("unknown workflow %q (configured workflows: %s)", workflow, availableWorkflows(cfg))
	}

	// A non-nil defaults lets the resolvers drop their nil guards: the config may
	// omit the defaults block entirely, so substitute an empty one here.
	defaults := cfg.Defaults
	if defaults == nil {
		defaults = &taboo.RunDefaults{}
	}

	// wf.Profile already carries the top-level→workflow agent fallback resolved by
	// LoadConfig (resolveProfiles bakes orElse(wf.Agent, cfg.Agent) in), so it is
	// nil only when no agent is configured anywhere.
	profile := wf.Profile
	if profile == nil {
		return runPlan{}, fmt.Errorf("workflow %q has no agent configured (set agent: on the workflow or a top-level agent:)", workflow)
	}

	base := filepath.Dir(configPath)
	prompt, err := resolvePrompt(wf, defaults, base)
	if errors.Is(err, errNoPrompt) {
		return runPlan{}, fmt.Errorf("workflow %q has no prompt (set prompt or prompt-file)", workflow)
	}
	if err != nil {
		return runPlan{}, fmt.Errorf("workflow %q: read prompt-file: %w", workflow, err)
	}

	repoPath, err := filepath.Abs(cfg.Repo)
	if err != nil {
		return runPlan{}, fmt.Errorf("resolve repo path %q: %w", cfg.Repo, err)
	}

	plan := runPlan{
		workflow: workflow,
		runnerConfig: taboo.Config{
			Workshop:   cfg.Workshop,
			Base:       cfg.Base,
			Agent:      profile,
			RepoPath:   repoPath,
			ProjectDir: base,
		},
		prompt:           prompt,
		timeout:          resolveTimeout(wf, defaults),
		maxIterations:    resolveMaxIterations(wf, defaults),
		completionSignal: defaults.CompletionSignal,
	}
	plan.branch = resolveBranch(branchOverride, defaults.BranchPrefix, workflow)
	return plan, nil
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

// resolvePrompt applies the prompt precedence (workflow inline → workflow file →
// defaults inline → defaults file), returning the first non-empty resolution. A
// prompt-file path is read relative to the config file's directory (the same
// base validate uses), so a relative path resolves identically here and in the
// preflight. No prompt anywhere is an error — there is nothing for the agent to do.
func resolvePrompt(wf taboo.Workflow, defaults *taboo.RunDefaults, base string) (string, error) {
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

// resolveTimeout applies workflow-then-defaults precedence for the per-exec
// timeout (zero means no timeout).
func resolveTimeout(wf taboo.Workflow, defaults *taboo.RunDefaults) time.Duration {
	if wf.Timeout != 0 {
		return time.Duration(wf.Timeout)
	}
	return time.Duration(defaults.Timeout)
}

// resolveMaxIterations applies workflow-then-defaults precedence for the
// iteration cap (zero or negative means a single run; the Orchestrator floors it).
func resolveMaxIterations(wf taboo.Workflow, defaults *taboo.RunDefaults) int {
	if wf.MaxIterations > 0 {
		return wf.MaxIterations
	}
	return defaults.MaxIterations
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
// the full config-correctness checks and refuses the run when any errors. The
// report goes to stderr (not stdout) so a refusal does not pollute the machine
// result stream a successful run writes there. It returns errRunFailed so main
// exits non-zero without echoing cobra noise.
func runPreflight(ctx context.Context, env Env) error {
	checks := []check{checkWorkshop(ctx, env)}
	checks = append(checks, validateChecks(ctx, env, statFileExists)...)
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
	_, _ = fmt.Fprintf(env.Stderr, "Running workflow %q on branch %q (agent %s)…\n", plan.workflow, plan.branch, plan.runnerConfig.Agent.Name())

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
	_, _ = fmt.Fprintf(env.Stdout, "  %-18s %s\n", "workflow:", plan.workflow)
	_, _ = fmt.Fprintf(env.Stdout, "  %-18s %s\n", "branch:", plan.branch)
	_, _ = fmt.Fprintf(env.Stdout, "  %-18s %s\n", "agent:", plan.runnerConfig.Agent.Name())
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
