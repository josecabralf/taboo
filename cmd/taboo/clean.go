package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	taboo "github.com/josecabralf/taboo/pkg/taboo"
)

// cleanOptions are the parsed flags for the clean subcommand: which lifecycle
// artifacts to tear down (worktrees by default, workshops, branches) and the
// safety rails (force, dry-run, yes).
type cleanOptions struct {
	workshops     bool
	all           bool
	pruneBranches bool
	force         bool
	dryRun        bool
	yes           bool
}

// cleanPlan is the resolved set of taboo-managed artifacts clean will tear down,
// discovered by probing the host before any mutation so a dry-run or confirmation
// can describe exactly what would change.
type cleanPlan struct {
	worktrees  []jsonWorktree
	workshops  []string
	branches   []string
	unmerged   []string
	repo       string
	projectDir string
}

// newCleanCmd builds the `clean` subcommand: it tears down the taboo-managed
// lifecycle artifacts of the current project. By default it removes only the
// project's worktrees; --workshops or --all extend that to the workshops, and
// --prune-branches deletes the run branches under the configured branch-prefix.
func newCleanCmd(env Env) *cobra.Command {
	opts := cleanOptions{}
	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Tear down the project's taboo-managed worktrees, workshops, and branches",
		Long: "clean removes the lifecycle artifacts taboo created for this project. By default it " +
			"removes the project's worktrees; --workshops switches to the workshops instead, and --all does " +
			"both, while --prune-branches deletes the run branches under the configured branch-prefix. " +
			"It probes the host through the same command seam as the rest of taboo; --dry-run prints the " +
			"plan without mutating anything.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runClean(cmd.Context(), env, &opts)
		},
	}
	cmd.Flags().BoolVar(&opts.workshops, "workshops", false, "tear down the project's workshops instead of its worktrees")
	cmd.Flags().BoolVar(&opts.all, "all", false, "tear down both the worktrees and the workshops")
	cmd.Flags().BoolVar(&opts.pruneBranches, "prune-branches", false, "also delete the run branches under the configured branch-prefix")
	cmd.Flags().BoolVar(&opts.force, "force", false, "delete branches even when not merged")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "print the plan without removing anything")
	cmd.Flags().BoolVar(&opts.yes, "yes", false, "skip the interactive confirmation")
	return cmd
}

// runClean discovers the project config, gathers the taboo-managed artifacts in
// scope into a plan, and executes the teardown. The default scope is worktrees;
// --workshops or --all widen it.
func runClean(ctx context.Context, env Env, opts *cleanOptions) error {
	configPath, cfg, err := loadProjectConfig(env)
	if err != nil {
		return err
	}
	projectDir := filepath.Dir(configPath)
	repo, err := filepath.Abs(cfg.Repo)
	if err != nil {
		return fmt.Errorf("resolve repo path %q: %w", cfg.Repo, err)
	}

	prefix := branchPrefix(cfg)
	// An empty branch-prefix makes every branch a match, so pruning would delete
	// the user's own branches. Refuse rather than guess which are taboo's.
	if opts.pruneBranches && prefix == "" {
		return errors.New("--prune-branches needs a configured branch-prefix; without one every branch would match")
	}

	plan, err := buildCleanPlan(ctx, env, cfg, projectDir, repo, prefix, opts)
	if err != nil {
		return err
	}

	// A dry run only describes the plan, so it short-circuits before the refusal
	// gate: it never errors and never mutates anything.
	if opts.dryRun {
		printCleanPlan(env.Stdout, plan)
		return nil
	}

	// Refuse the whole command before any mutation when an unmerged branch would
	// be pruned without --force, so a refused prune mutates nothing.
	if opts.pruneBranches && !opts.force && len(plan.unmerged) > 0 {
		return fmt.Errorf("refusing to prune %d unmerged branch(es) without --force: %s",
			len(plan.unmerged), strings.Join(plan.unmerged, ", "))
	}

	if planEmpty(plan) {
		_, _ = fmt.Fprintln(env.Stdout, "Nothing to clean.")
		return nil
	}

	// At a TTY, confirm before mutating; --yes skips the prompt.
	if isInteractive(env) && !opts.yes && !confirmClean(env, plan) {
		_, _ = fmt.Fprintln(env.Stderr, "Aborted.")
		return nil
	}

	return executeClean(ctx, env, plan)
}

// branchPrefix returns the configured run-branch prefix, or "" when the config
// has no defaults block.
func branchPrefix(cfg *taboo.ProjectConfig) string {
	if cfg.Defaults != nil {
		return cfg.Defaults.BranchPrefix
	}
	return ""
}

// planEmpty reports whether a plan would tear nothing down — the signal to print
// "Nothing to clean." rather than confirm and execute an empty teardown.
func planEmpty(plan cleanPlan) bool {
	return len(plan.worktrees) == 0 && len(plan.workshops) == 0 && len(plan.branches) == 0
}

// buildCleanPlan discovers the taboo-managed artifacts in scope by probing the
// host before any mutation: worktrees by default, workshops under --workshops or
// --all, and the prefix branches (partitioned by merge state) under
// --prune-branches. Every probe is read-only.
func buildCleanPlan(ctx context.Context, env Env, cfg *taboo.ProjectConfig, projectDir, repo, prefix string, opts *cleanOptions) (cleanPlan, error) {
	plan := cleanPlan{repo: repo, projectDir: projectDir}
	doWorktrees := opts.all || !opts.workshops
	doWorkshops := opts.all || opts.workshops
	if doWorktrees {
		worktrees, err := gatherWorktrees(ctx, env, projectDir, repo)
		if err != nil {
			return cleanPlan{}, err
		}
		plan.worktrees = worktrees
	}
	if doWorkshops {
		plan.workshops = provisionedWorkshops(ctx, env, projectDir, cfg)
	}
	if opts.pruneBranches {
		branches, unmerged, err := planBranches(ctx, env, repo, prefix, opts.force)
		if err != nil {
			return cleanPlan{}, err
		}
		plan.branches = branches
		plan.unmerged = unmerged
	}
	return plan, nil
}

// provisionedWorkshops returns the names of the project's derived workshops that
// actually exist on the host, skipping any not provisioned (there is nothing to
// tear down for those).
func provisionedWorkshops(ctx context.Context, env Env, projectDir string, cfg *taboo.ProjectConfig) []string {
	var names []string
	for _, w := range gatherWorkshops(ctx, env, projectDir, cfg) {
		if w.Status == "not provisioned" {
			continue
		}
		names = append(names, w.Name)
	}
	return names
}

// planBranches lists the prefix run branches and partitions them by merge state:
// a branch is queued for deletion when it is already merged or force is set,
// otherwise it is returned as unmerged so the caller can refuse it.
func planBranches(ctx context.Context, env Env, repo, prefix string, force bool) (toDelete, unmerged []string, err error) {
	branches, err := gatherBranches(ctx, env, repo, prefix)
	if err != nil {
		return nil, nil, err
	}
	merged, err := mergedBranches(ctx, env, repo)
	if err != nil {
		return nil, nil, err
	}
	for _, b := range branches {
		if force || merged[b] {
			toDelete = append(toDelete, b)
			continue
		}
		unmerged = append(unmerged, b)
	}
	return toDelete, unmerged, nil
}

// printCleanPlan writes the --dry-run teardown preview: one section per artifact
// kind, plus the branches skipped for being unmerged.
func printCleanPlan(w io.Writer, plan cleanPlan) {
	_, _ = fmt.Fprintln(w, "taboo clean (dry run) — would:")
	worktrees := make([]string, 0, len(plan.worktrees))
	for _, wt := range plan.worktrees {
		worktrees = append(worktrees, wt.Branch+"  "+wt.Path)
	}
	renderSection(w, "remove worktrees:", worktrees)
	renderSection(w, "tear down workshops:", plan.workshops)
	renderSection(w, "delete branches:", plan.branches)
	if len(plan.unmerged) > 0 {
		renderSection(w, "skip unmerged branches (pass --force):", plan.unmerged)
	}
}

// confirmClean prints the teardown summary to stderr and reads a yes/no answer from
// stdin. It returns true only for an affirmative "y"/"yes"; any read error other than
// a clean EOF is treated as a decline.
func confirmClean(env Env, plan cleanPlan) bool {
	var parts []string
	if n := len(plan.worktrees); n > 0 {
		parts = append(parts, fmt.Sprintf("remove %d worktree(s)", n))
	}
	if n := len(plan.workshops); n > 0 {
		parts = append(parts, fmt.Sprintf("tear down %d workshop(s)", n))
	}
	if n := len(plan.branches); n > 0 {
		parts = append(parts, fmt.Sprintf("delete %d branch(es)", n))
	}
	msg := fmt.Sprintf("About to %s. Continue? [y/N] ", strings.Join(parts, ", "))
	ok, err := promptYesNo(env, msg)
	return err == nil && ok
}

// mergedBranches probes `git -C <repo> branch --merged` and returns the set of
// branch names already merged into the current HEAD. The leading "* "/"+ "/"  "
// markers git prints are trimmed off each line.
func mergedBranches(ctx context.Context, env Env, repo string) (map[string]bool, error) {
	out, err := probe(ctx, env, "git", "-C", repo, "branch", "--merged")
	if err != nil {
		return nil, fmt.Errorf("list merged branches in %q: %w", repo, err)
	}
	merged := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimSpace(strings.TrimLeft(line, "*+ "))
		if name == "" {
			continue
		}
		merged[name] = true
	}
	return merged, nil
}

// executeClean tears the planned artifacts down through the host seam, best-effort:
// a failure on one artifact warns to stderr and continues to the next, and every
// failure is joined into the returned error so the command still exits non-zero.
func executeClean(ctx context.Context, env Env, plan cleanPlan) error {
	var errs []error
	for _, wt := range plan.worktrees {
		if err := hostRun(ctx, env, "git", "-C", plan.repo, "worktree", "remove", wt.Path); err != nil {
			_, _ = fmt.Fprintf(env.Stderr, "warning: remove worktree %s: %v\n", wt.Path, err)
			errs = append(errs, err)
			continue
		}
		_, _ = fmt.Fprintf(env.Stderr, "removed worktree %s\n", wt.Path)
	}
	for _, name := range plan.workshops {
		if err := hostRun(ctx, env, "workshop", "--project", plan.projectDir, "remove", name); err != nil {
			_, _ = fmt.Fprintf(env.Stderr, "warning: remove workshop %s: %v\n", name, err)
			errs = append(errs, err)
			continue
		}
		_, _ = fmt.Fprintf(env.Stderr, "tore down workshop %s\n", name)
	}
	for _, b := range plan.branches {
		if err := hostRun(ctx, env, "git", "-C", plan.repo, "branch", "-D", b); err != nil {
			_, _ = fmt.Fprintf(env.Stderr, "warning: delete branch %s: %v\n", b, err)
			errs = append(errs, err)
			continue
		}
		_, _ = fmt.Fprintf(env.Stderr, "deleted branch %s\n", b)
	}
	return errors.Join(errs...)
}

// hostRun runs one mutating host command through the Commander seam, routing both
// streams to stderr so the command's progress and any tool output stay off stdout.
func hostRun(ctx context.Context, env Env, name string, args ...string) error {
	return env.Cmd.Run(ctx, taboo.Cmd{Name: name, Args: args, Stdout: env.Stderr, Stderr: env.Stderr})
}
