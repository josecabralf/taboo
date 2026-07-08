package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/josecabralf/taboo"
)

// defaultBase is the workshop base image init assumes when none is supplied.
const defaultBase = "ubuntu@24.04"

// initOptions are the resolved-or-flag values init scaffolds from.
type initOptions struct {
	agent string
	model string
	base  string
	// repo is the host repository path (absolute after finalize).
	repo string
	// workshop is the workshop name (derived from repo when unset).
	workshop string
	// sourceDefinition names which .workshop/*.yaml the project derives from;
	// required (non-interactively) only when the repo has multiple definitions.
	sourceDefinition string
	// workflows selects which example workflows to seed; `none` opts out.
	workflows string
	// template selects the optional Go scaffold: `none`, `single`, or `fanout`.
	template string
	force    bool
	dryRun   bool
}

// newInitCmd builds the `init` subcommand.
func newInitCmd(env Env) *cobra.Command {
	opts := initOptions{}
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Scaffold a .taboo/ project into a repository",
		Long: "init scaffolds a .taboo/ directory (taboo.yaml, .gitignore, .env.example) into a " +
			"target repository. Run it interactively to be prompted for agent, model, base, and " +
			"repo, or pass the equivalent flags to scaffold non-interactively.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runInitCmd(env, &opts)
		},
	}
	cmd.Flags().StringVar(&opts.agent, "agent", "", "agent to scaffold for (e.g. opencode, claude-code, copilot)")
	cmd.Flags().StringVar(&opts.model, "model", "", "model passed to the chosen agent")
	cmd.Flags().StringVar(&opts.base, "base", "", "workshop base image (default: "+defaultBase+")")
	cmd.Flags().StringVar(&opts.repo, "repo", "", "host repository path to scaffold into (default: current directory)")
	cmd.Flags().StringVar(&opts.workshop, "workshop", "", "workshop name (default: derived from the repo directory name)")
	cmd.Flags().StringVar(&opts.sourceDefinition, "source-definition", "", "named .workshop/*.yaml definition to derive from (required when the project has multiple)")
	cmd.Flags().StringVar(&opts.workflows, "workflows", "", "seed the example fix and refactor workflows (default); pass \"none\" to skip")
	cmd.Flags().StringVar(&opts.template, "template", "none", "scaffold a Go main.go: none (default), single, or fanout")
	cmd.Flags().BoolVar(&opts.force, "force", false, "regenerate the scaffold files in an existing .taboo directory")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "list the files init would write without writing them")
	return cmd
}

// runInitCmd orchestrates init's resolve-then-scaffold flow.
func runInitCmd(env Env, opts *initOptions) error {
	// Reject an unknown --template before any side effects (wizard, writes).
	if err := validateTemplate(opts.template); err != nil {
		return err
	}
	if err := applyDefaults(env, opts); err != nil {
		return err
	}
	// Gate on the repo being a workshop project BEFORE any prompting or writes, so
	// a non-workshop repo fails fast rather than after the wizard. finalize is
	// side-effect-free path resolution, safe to run here.
	if err := finalize(env, opts); err != nil {
		return err
	}
	if err := requireWorkshopProject(opts.repo); err != nil {
		return err
	}
	if err := collectValues(env, opts); err != nil {
		return err
	}
	// Re-resolve in case the wizard changed repo (its prefill is editable): a
	// relative path typed at the prompt must still be cleaned to absolute before
	// the write. finalize is idempotent on an already-absolute path.
	if err := finalize(env, opts); err != nil {
		return err
	}

	profile, err := resolveProfile(opts.agent, opts.model)
	if err != nil {
		return err
	}

	projectDir := filepath.Join(opts.repo, ".taboo")
	if err := ensureWritable(projectDir, opts.force); err != nil {
		return err
	}

	in := scaffoldInputs{
		Workshop:         opts.workshop,
		Base:             opts.base,
		Repo:             opts.repo,
		Agent:            taboo.AgentName(opts.agent),
		Model:            opts.model,
		Profile:          profile,
		SeedWorkflows:    opts.workflows != "none",
		Template:         opts.template,
		SourceDefinition: opts.sourceDefinition,
	}
	files, err := in.plan()
	if err != nil {
		return err
	}
	if opts.dryRun {
		printDryRun(env, projectDir, files)
		return nil
	}
	if err := writeScaffold(projectDir, files); err != nil {
		return err
	}
	printNextSteps(env, opts, projectDir)
	return nil
}

// collectValues fills agent, model, and the source-definition selection. At a
// TTY it prompts via the wizard for any unset required value; non-interactively
// it fails fast naming the missing flag.
func collectValues(env Env, opts *initOptions) error {
	pending, err := pendingSourceDefinitions(opts)
	if err != nil {
		return err
	}
	if isInteractive(env) {
		if opts.agent == "" || opts.model == "" || len(pending) > 0 {
			return runWizard(env, opts)
		}
		return nil
	}
	if err := requireValues(opts); err != nil {
		return err
	}
	if len(pending) > 0 {
		return fmt.Errorf("multiple workshop definitions (%s): pass --source-definition to pick one",
			strings.Join(pending, ", "))
	}
	return nil
}

// pendingSourceDefinitions returns the candidate names when the repo has several
// named definitions and none was selected; nil once the selection is settled. An
// explicit selection is validated here so a typo'd --source-definition fails fast
// at init rather than later at run.
func pendingSourceDefinitions(opts *initOptions) ([]string, error) {
	named, err := taboo.SourceDefinitions(opts.repo)
	if err != nil {
		return nil, err
	}
	if opts.sourceDefinition != "" {
		if err := taboo.ValidateSourceDefinition(named, opts.sourceDefinition); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if len(named) < 2 {
		return nil, nil
	}
	return named, nil
}

// validTemplates are the accepted --template values; `none` scaffolds no Go.
var validTemplates = []string{"none", "single", "fanout"}

// validateTemplate rejects an unknown --template value.
func validateTemplate(t string) error {
	if slices.Contains(validTemplates, t) {
		return nil
	}
	return fmt.Errorf("unknown template %q; valid templates: %s", t, strings.Join(validTemplates, ", "))
}

// applyDefaults fills the pre-wizard defaults: base to defaultBase, repo to the
// working directory.
func applyDefaults(env Env, opts *initOptions) error {
	if opts.base == "" {
		opts.base = defaultBase
	}
	if opts.repo == "" {
		wd, err := env.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		opts.repo = wd
	}
	return nil
}

// requireValues enforces the non-interactive contract: agent and model must be
// supplied. It names every missing flag in one error.
func requireValues(opts *initOptions) error {
	var missing []string
	if opts.agent == "" {
		missing = append(missing, "--agent")
	}
	if opts.model == "" {
		missing = append(missing, "--model")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required flags: %s (pass them or run init interactively)",
			strings.Join(missing, ", "))
	}
	return nil
}

// finalize resolves repo to an absolute path, derives the workshop name when
// unset, and guarantees a non-empty base (the wizard's base field has no required
// validator, so an interactive user can clear it).
func finalize(env Env, opts *initOptions) error {
	if !filepath.IsAbs(opts.repo) {
		wd, err := env.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		opts.repo = filepath.Join(wd, opts.repo)
	}
	opts.repo = filepath.Clean(opts.repo)
	if opts.workshop == "" {
		opts.workshop = deriveWorkshopName(opts.repo)
	}
	if opts.base == "" {
		opts.base = defaultBase
	}
	return nil
}

// requireWorkshopProject enforces ADR 0009's scope: taboo derives the agent's
// workshop from the project's own definition (a root workshop.yaml or a named
// .workshop/*.yaml), so a project with neither is a hard early error, not a
// fallback.
func requireWorkshopProject(repo string) error {
	if _, err := os.Stat(filepath.Join(repo, "workshop.yaml")); err == nil {
		return nil
	}
	// A multi-definition project records its source in .workshop/*.yaml rather
	// than a root workshop.yaml, so a named definition also satisfies the gate.
	if named, err := taboo.SourceDefinitions(repo); err == nil && len(named) > 0 {
		return nil
	}
	return fmt.Errorf("no workshop definition found in %s: taboo derives the agent's workshop from "+
		"the project's own definition and supports only workshop-using projects; "+
		"add a workshop.yaml (or a named .workshop/*.yaml), then re-run", repo)
}

// resolveProfile maps the chosen agent/model to an AgentProfile, turning an
// unknown agent into an error listing the valid agent names.
func resolveProfile(agent, model string) (taboo.AgentProfile, error) {
	profile, err := taboo.NewProfile(taboo.AgentName(agent), model)
	if err != nil {
		if errors.Is(err, taboo.ErrUnknownAgent) {
			return nil, fmt.Errorf("unknown agent %q; valid agents: %s",
				agent, strings.Join(taboo.AgentNames(), ", "))
		}
		return nil, err
	}
	return profile, nil
}

// ensureWritable refuses to clobber an existing .taboo directory unless force is
// set. It rejects a non-directory at that path for a clear error rather than an
// opaque MkdirAll failure, and runs before the dry-run branch so a preview is
// honest about the refusal.
func ensureWritable(projectDir string, force bool) error {
	info, err := os.Stat(projectDir)
	if err != nil {
		return nil // absent or unstattable: writeScaffold creates it or surfaces the error.
	}
	if !info.IsDir() {
		return fmt.Errorf("%s exists but is not a directory; remove it and re-run init", projectDir)
	}
	if !force {
		return fmt.Errorf(".taboo already exists at %s; pass --force to regenerate its scaffold files", projectDir)
	}
	return nil
}

// printDryRun lists the absolute path of every file init would write, touching
// nothing on disk.
func printDryRun(env Env, projectDir string, files []scaffoldFile) {
	_, _ = fmt.Fprintln(env.Stdout, "taboo init (dry run) — would write:")
	for _, f := range files {
		_, _ = fmt.Fprintf(env.Stdout, "  %s\n", filepath.Join(projectDir, f.Path))
	}
}

// printNextSteps confirms the scaffold and prints the suggested follow-ups.
func printNextSteps(env Env, opts *initOptions, projectDir string) {
	envExample := filepath.Join(projectDir, ".env.example")
	envFile := filepath.Join(projectDir, ".env")
	_, _ = fmt.Fprintf(env.Stdout, "Scaffolded taboo project at %s\n", projectDir)
	_, _ = fmt.Fprintln(env.Stdout, "")
	_, _ = fmt.Fprintln(env.Stdout, "Next steps:")
	_, _ = fmt.Fprintf(env.Stdout, "  1. Copy %s to %s and fill in your %s credential(s),\n",
		envExample, envFile, opts.agent)
	_, _ = fmt.Fprintf(env.Stdout, "     then load them into your shell so taboo can forward them (e.g. `set -a; source %s; set +a`).\n", envFile)
	_, _ = fmt.Fprintf(env.Stdout, "  2. Review %s and adjust as needed.\n",
		filepath.Join(projectDir, "taboo.yaml"))
	_, _ = fmt.Fprintln(env.Stdout, "  3. Run `taboo doctor` to verify your host is ready.")
	// The trailing steps are conditional; number them sequentially after the three
	// fixed steps so there are no skipped numbers.
	var extra []string
	if opts.workflows != "none" {
		extra = append(extra, "Try `taboo run fix` (or edit prompts/ and taboo.yaml).")
	}
	if opts.template != "none" {
		extra = append(extra, fmt.Sprintf("Build the Go scaffold: cd %s && go mod tidy && go run .", projectDir))
	}
	for i, step := range extra {
		_, _ = fmt.Fprintf(env.Stdout, "  %d. %s\n", i+4, step)
	}
}

// isInteractive reports whether stdin is a real terminal. It uses a real isatty
// probe rather than an os.ModeCharDevice check, since /dev/null is a character
// device too, so a redirected < /dev/null still reads as non-interactive.
func isInteractive(env Env) bool {
	if env.Interactive != nil {
		return env.Interactive()
	}
	f, ok := env.Stdin.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(f.Fd())
}

// deriveWorkshopName slugifies the repo's base directory name: lowercased, every
// run of non-[a-z0-9] collapsed to a single dash, ends trimmed. It falls back to
// `taboo` when the result is empty.
func deriveWorkshopName(repo string) string {
	base := strings.ToLower(filepath.Base(filepath.Clean(repo)))
	var b strings.Builder
	prevDash := false
	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "taboo"
	}
	return slug
}
