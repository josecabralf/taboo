package app

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/josecabralf/taboo"
)

// newListCmd builds the `list` subcommand.
func newListCmd(env Env) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the project's workshops, worktrees, branches, and workflows",
		Long: "list reports the lifecycle state of the current taboo project: each configured " +
			"workshop and its state, the repo's worktrees, its branches, and the configured " +
			"workflows with their effective agent, model, and prompt. It reads the host " +
			"through the same command seam as the rest of taboo and never mutates anything.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runList(cmd.Context(), env, asJSON, statFileExists)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the listing as JSON")
	return cmd
}

// jsonWorkshop is one workshop entry in the --json document.
type jsonWorkshop struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// jsonWorktree is one taboo-managed worktree entry in the --json document.
type jsonWorktree struct {
	Branch string `json:"branch"`
	Path   string `json:"path"`
}

// jsonWorkflow is one configured workflow entry in the --json document. The
// effective agent and model fall back to the top level, mirroring
// referencedAgents/referencedModels precedence.
type jsonWorkflow struct {
	Name            string   `json:"name"`
	Default         bool     `json:"default"`
	Agent           string   `json:"agent"`
	Model           string   `json:"model"`
	Prompt          string   `json:"prompt"`
	PromptAvailable bool     `json:"promptAvailable"`
	Placeholders    []string `json:"placeholders"`
}

// jsonListResult is the machine shape `list --json` emits. Workflows is declared
// last so the three pre-existing keys marshal byte-identically to before the
// section existed.
type jsonListResult struct {
	Workshops []jsonWorkshop `json:"workshops"`
	Worktrees []jsonWorktree `json:"worktrees"`
	Branches  []string       `json:"branches"`
	Workflows []jsonWorkflow `json:"workflows"`
}

// runList loads the project config, gathers the workshops/worktrees/branches
// sections by probing the host, plus the workflows section from the config alone,
// then emits them as JSON or the human view. A workshop-info probe error means
// that workshop is not provisioned (not fatal); a git probe error is fatal.
func runList(ctx context.Context, env Env, asJSON bool, statFile func(string) bool) error {
	configPath, cfg, err := loadProjectConfig(env)
	if err != nil {
		return err
	}
	projectDir := filepath.Dir(configPath)

	workshops := gatherWorkshops(ctx, env, projectDir, cfg)

	repo, err := filepath.Abs(cfg.Repo)
	if err != nil {
		return fmt.Errorf("resolve repo path %q: %w", cfg.Repo, err)
	}
	worktrees, err := gatherWorktrees(ctx, env, projectDir, repo)
	if err != nil {
		return err
	}

	prefix := branchPrefix(cfg)
	branches, err := gatherBranches(ctx, env, repo, prefix)
	if err != nil {
		return err
	}

	workflows := gatherWorkflows(cfg, projectDir, statFile)

	result := jsonListResult{Workshops: workshops, Worktrees: worktrees, Branches: branches, Workflows: workflows}
	if asJSON {
		// The gather helpers return empty (never nil) slices, so each section
		// marshals as the conventional machine shape [] rather than null.
		return writeIndentedJSON(env.Stdout, result)
	}
	renderListResult(env, result)
	return nil
}

// renderListResult writes the human view of the gathered listing to env.Stdout,
// each section falling back to `  (none)` when empty.
func renderListResult(env Env, r jsonListResult) {
	_, _ = fmt.Fprintln(env.Stdout, "taboo list — workshops, worktrees, branches, workflows")

	workshops := make([]string, 0, len(r.Workshops))
	for _, w := range r.Workshops {
		workshops = append(workshops, w.Name+"  "+w.Status)
	}
	renderSection(env.Stdout, "workshops:", workshops)

	renderSection(env.Stdout, "worktrees:", worktreeLines(r.Worktrees))

	renderSection(env.Stdout, "branches:", r.Branches)

	renderSection(env.Stdout, "workflows:", workflowLines(r.Workflows))
}

// workflowLines formats workflows as human section lines: name (with a
// `(default)` marker), effective agent and model, the one-line prompt preview
// (`(unavailable)` when it did not resolve), and any {{VAR}} placeholder names.
func workflowLines(wfs []jsonWorkflow) []string {
	lines := make([]string, 0, len(wfs))
	for _, wf := range wfs {
		name := wf.Name
		if wf.Default {
			name += " (default)"
		}
		prompt := "(unavailable)"
		if wf.PromptAvailable {
			prompt = wf.Prompt
		}
		line := name + "  agent: " + wf.Agent + "  model: " + wf.Model + "  prompt: " + prompt
		if len(wf.Placeholders) > 0 {
			line += "  vars: " + strings.Join(wf.Placeholders, ", ")
		}
		lines = append(lines, line)
	}
	return lines
}

// gatherWorkflows computes the workflows section from the loaded config alone (no
// host probes): one entry per configured workflow, sorted by name. An absent or
// unreadable prompt-file degrades to PromptAvailable=false rather than failing
// the listing; existence policing stays validate's job.
func gatherWorkflows(cfg *taboo.ProjectConfig, base string, statFile func(string) bool) []jsonWorkflow {
	out := []jsonWorkflow{}
	for _, name := range sortedWorkflowNames(*cfg) {
		wf := cfg.Workflows[name]
		entry := jsonWorkflow{
			Name:         name,
			Default:      cfg.DefaultWorkflow != "" && name == cfg.DefaultWorkflow,
			Agent:        string(cmp.Or(wf.Agent, cfg.Agent)),
			Model:        cmp.Or(wf.Model, cfg.Model),
			Placeholders: []string{},
		}
		if text, found := effectivePrompt(*cfg, wf, base, statFile); found {
			entry.Prompt = promptSummary(text)
			entry.PromptAvailable = true
			entry.Placeholders = emptyIfNil(taboo.Placeholders(text))
		}
		out = append(out, entry)
	}
	return out
}

// worktreeLines formats worktrees as `<branch>  <path>` section lines, shared by
// the list view and clean's dry-run plan.
func worktreeLines(wts []jsonWorktree) []string {
	lines := make([]string, 0, len(wts))
	for _, wt := range wts {
		lines = append(lines, wt.Branch+"  "+wt.Path)
	}
	return lines
}

// renderSection writes one section of the human view: the header line, then the
// lines indented two spaces, falling back to `  (none)` when there are none.
func renderSection(w io.Writer, header string, lines []string) {
	_, _ = fmt.Fprintln(w, header)
	if len(lines) == 0 {
		_, _ = fmt.Fprintln(w, "  (none)")
		return
	}
	for _, line := range lines {
		_, _ = fmt.Fprintf(w, "  %s\n", line)
	}
}

// gatherWorkshops probes each project workshop's lifecycle state.
func gatherWorkshops(ctx context.Context, env Env, projectDir string, cfg *taboo.ProjectConfig) []jsonWorkshop {
	out := []jsonWorkshop{}
	for _, name := range projectWorkshops(cfg) {
		out = append(out, jsonWorkshop{Name: name, Status: workshopState(ctx, env, projectDir, name)})
	}
	return out
}

// gatherBranches returns the repo's branches under the configured branch-prefix
// (taboo's own run branches). An empty prefix returns every branch, since taboo's
// branches are then indistinguishable from the user's. A git probe error is fatal.
func gatherBranches(ctx context.Context, env Env, repo, prefix string) ([]string, error) {
	out, err := probe(ctx, env, "git", "-C", repo, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if err != nil {
		return nil, fmt.Errorf("list branches in %q: %w", repo, err)
	}
	branches := []string{}
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimSpace(line)
		if name == "" || !strings.HasPrefix(name, prefix) {
			continue
		}
		branches = append(branches, name)
	}
	return branches, nil
}

// gatherWorktrees returns only the worktrees taboo manages for this project
// (those under <projectDir>/worktrees/). A git probe error is fatal.
func gatherWorktrees(ctx context.Context, env Env, projectDir, repo string) ([]jsonWorktree, error) {
	out, err := probe(ctx, env, "git", "-C", repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("list worktrees in %q: %w", repo, err)
	}
	managedRoot := filepath.Join(projectDir, "worktrees")
	wts := []jsonWorktree{}
	for _, wt := range parseWorktrees(out) {
		if !underDir(wt.Path, managedRoot) {
			continue
		}
		wts = append(wts, wt)
	}
	return wts, nil
}

// parseWorktrees splits porcelain output (blank-line-separated entries) into
// jsonWorktree entries. An entry with no branch line (detached HEAD) gets branch
// `(detached)`; an entry with no path is skipped.
func parseWorktrees(out string) []jsonWorktree {
	var wts []jsonWorktree
	for _, block := range strings.Split(out, "\n\n") {
		var wt jsonWorktree
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				wt.Path = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
			case strings.HasPrefix(line, "branch refs/heads/"):
				wt.Branch = strings.TrimSpace(strings.TrimPrefix(line, "branch refs/heads/"))
			}
		}
		if wt.Path == "" {
			continue
		}
		if wt.Branch == "" {
			wt.Branch = "(detached)"
		}
		wts = append(wts, wt)
	}
	return wts
}

// underDir reports whether path is dir itself or nested under it, comparing
// cleaned paths with a separator boundary so `/a/worktrees-x` is not treated as
// being under `/a/worktrees`.
func underDir(path, dir string) bool {
	path = filepath.Clean(path)
	dir = filepath.Clean(dir)
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

// workshopState probes a single workshop's lifecycle state. A probe error means
// the workshop is not provisioned yet.
func workshopState(ctx context.Context, env Env, projectDir, name string) string {
	out, err := probe(ctx, env, "workshop", "--project", projectDir, "info", name)
	if err != nil {
		return "not provisioned"
	}
	return parseWorkshopStatus(out)
}

// parseWorkshopStatus pulls the status field out of `workshop info` YAML,
// falling back to `unknown` when it is unparseable or empty.
func parseWorkshopStatus(out string) string {
	var info struct {
		Status string `yaml:"status"`
	}
	if err := yaml.Unmarshal([]byte(out), &info); err != nil || info.Status == "" {
		return "unknown"
	}
	return info.Status
}

// projectWorkshops returns the workshop names taboo provisions for this project:
// one per distinct referenced agent, derived as <workshop>-<agent>, matching what
// `run` launches. Order follows distinctProfiles (sorted by agent name).
func projectWorkshops(cfg *taboo.ProjectConfig) []string {
	if cfg.Workshop == "" {
		return nil
	}
	var names []string
	for _, p := range distinctProfiles(cfg) {
		names = append(names, workshopName(cfg.Workshop, string(p.Name())))
	}
	return names
}
