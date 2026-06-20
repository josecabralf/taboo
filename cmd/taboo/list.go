package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	taboo "github.com/josecabralf/taboo/pkg"
)

// newListCmd builds the `list` subcommand: a read-only, per-.taboo lifecycle
// view of the project's workshops, worktrees, and branches. It loads the
// project config and probes the host through the Commander seam to report
// current state, mutating nothing.
func newListCmd(env Env) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the project's workshops, worktrees, and branches",
		Long: "list reports the lifecycle state of the current taboo project: each configured " +
			"workshop and its state, the repo's worktrees, and its branches. It reads the host " +
			"through the same command seam as the rest of taboo and never mutates anything.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runList(cmd.Context(), env, asJSON)
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

// jsonListResult is the machine shape `list --json` emits: the same three
// sections the human view renders.
type jsonListResult struct {
	Workshops []jsonWorkshop `json:"workshops"`
	Worktrees []jsonWorktree `json:"worktrees"`
	Branches  []string       `json:"branches"`
}

// runList discovers and loads the project config, gathers the three lifecycle
// sections (workshops, worktrees, branches) by probing the host once, then
// emits them — as a JSON document when asJSON, otherwise as the human view.
// A workshop-info probe error means that workshop is not provisioned (normal,
// not fatal); a git probe error is fatal.
func runList(ctx context.Context, env Env, asJSON bool) error {
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

	result := jsonListResult{Workshops: workshops, Worktrees: worktrees, Branches: branches}
	if asJSON {
		// Coerce nil sections to empty slices so they marshal as the
		// conventional machine shape [] rather than null.
		if result.Workshops == nil {
			result.Workshops = []jsonWorkshop{}
		}
		if result.Worktrees == nil {
			result.Worktrees = []jsonWorktree{}
		}
		if result.Branches == nil {
			result.Branches = []string{}
		}
		enc := json.NewEncoder(env.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}
	renderListResult(env, result)
	return nil
}

// renderListResult writes the human view of the gathered listing to env.Stdout:
// a header followed by the workshops, worktrees, and branches sections, each
// falling back to "  (none)" when empty.
func renderListResult(env Env, r jsonListResult) {
	_, _ = fmt.Fprintln(env.Stdout, "taboo list — workshops, worktrees, branches")

	workshops := make([]string, 0, len(r.Workshops))
	for _, w := range r.Workshops {
		workshops = append(workshops, w.Name+"  "+w.Status)
	}
	renderSection(env.Stdout, "workshops:", workshops)

	worktrees := make([]string, 0, len(r.Worktrees))
	for _, w := range r.Worktrees {
		worktrees = append(worktrees, w.Branch+"  "+w.Path)
	}
	renderSection(env.Stdout, "worktrees:", worktrees)

	renderSection(env.Stdout, "branches:", r.Branches)
}

// renderSection writes one section of the human view: the header line, then the
// pre-formatted lines indented two spaces, falling back to "  (none)" when there
// are none.
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

// gatherWorkshops probes each project workshop's lifecycle state and returns one
// entry per configured workshop.
func gatherWorkshops(ctx context.Context, env Env, projectDir string, cfg *taboo.ProjectConfig) []jsonWorkshop {
	var out []jsonWorkshop
	for _, name := range projectWorkshops(cfg) {
		out = append(out, jsonWorkshop{Name: name, Status: workshopState(ctx, env, projectDir, name)})
	}
	return out
}

// gatherBranches probes `git -C <repo> for-each-ref --format=%(refname:short)
// refs/heads/` and returns only the branches under the configured branch-prefix
// (taboo's own run branches). An empty prefix returns every branch, since
// taboo's branches are then indistinguishable from the user's. A git probe error
// is fatal.
func gatherBranches(ctx context.Context, env Env, repo, prefix string) ([]string, error) {
	out, err := probe(ctx, env, "git", "-C", repo, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if err != nil {
		return nil, fmt.Errorf("list branches in %q: %w", repo, err)
	}
	var branches []string
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimSpace(line)
		if name == "" || !strings.HasPrefix(name, prefix) {
			continue
		}
		branches = append(branches, name)
	}
	return branches, nil
}

// gatherWorktrees probes `git -C <repo> worktree list --porcelain` and returns
// only the worktrees taboo manages for this project (those under
// <projectDir>/worktrees/). A git probe error is fatal: enumerating worktrees
// requires a working repo.
func gatherWorktrees(ctx context.Context, env Env, projectDir, repo string) ([]jsonWorktree, error) {
	out, err := probe(ctx, env, "git", "-C", repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("list worktrees in %q: %w", repo, err)
	}
	managedRoot := filepath.Join(projectDir, "worktrees")
	var wts []jsonWorktree
	for _, wt := range parseWorktrees(out) {
		if !underDir(wt.Path, managedRoot) {
			continue
		}
		wts = append(wts, wt)
	}
	return wts, nil
}

// parseWorktrees splits porcelain output (blank-line-separated entries) into
// jsonWorktree entries, reading the "worktree <path>" and "branch
// refs/heads/<name>" lines into the Path and Branch fields. An entry with no
// branch line (detached HEAD) gets branch "(detached)"; an entry with no path is
// skipped.
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
// cleaned paths with a separator boundary so "/a/worktrees-x" is not treated as
// being under "/a/worktrees".
func underDir(path, dir string) bool {
	path = filepath.Clean(path)
	dir = filepath.Clean(dir)
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

// workshopState probes a single workshop's lifecycle state via `workshop
// --project <projectDir> info <name>`. On success the captured YAML's status
// field is the state; a probe error means the workshop is not provisioned yet.
func workshopState(ctx context.Context, env Env, projectDir, name string) string {
	out, err := probe(ctx, env, "workshop", "--project", projectDir, "info", name)
	if err != nil {
		return "not provisioned"
	}
	return parseWorkshopStatus(out)
}

// parseWorkshopStatus pulls the status field out of `workshop info` YAML. An
// unparseable or empty status falls back to "unknown" rather than failing the
// listing.
func parseWorkshopStatus(out string) string {
	var info struct {
		Status string `yaml:"status"`
	}
	if err := yaml.Unmarshal([]byte(out), &info); err != nil || info.Status == "" {
		return "unknown"
	}
	return info.Status
}

// projectWorkshops returns the workshop names taboo provisions for this
// project: one per distinct agent referenced by the config, each derived as
// <workshop>-<agent> (workshopName) — matching what `run` launches, so
// the listing reflects the workshops that actually exist. Deterministic order
// follows distinctProfiles (sorted by agent name).
func projectWorkshops(cfg *taboo.ProjectConfig) []string {
	if cfg.Workshop == "" {
		return nil
	}
	var names []string
	for _, p := range distinctProfiles(cfg) {
		names = append(names, workshopName(cfg.Workshop, p.Name()))
	}
	return names
}
