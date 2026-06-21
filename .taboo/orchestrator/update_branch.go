package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"

	taboo "github.com/josecabralf/taboo/pkg"
)

// updateBranchResult is the structured output the update-branch agent emits in its
// <result> block: whether a merge commit was created, whether in-workshop
// validation passed, and a human-readable summary.
type updateBranchResult struct {
	Updated   bool   `json:"updated"`   // a merge commit was created
	Validated bool   `json:"validated"` // make lint test build passed
	Summary   string `json:"summary"`
}

// updateBranchGH is the subset of GitHub/git I/O the update-branch sequence uses.
// *ghio.Client satisfies it; tests substitute a fake to record calls and args.
type updateBranchGH interface {
	PRHeadBranch(ctx context.Context, number int) (string, error)
	Fetch(ctx context.Context) error
	UpToDateWithMain(ctx context.Context, branch string) (bool, error)
	Push(ctx context.Context, branch string) error
	AddLabel(ctx context.Context, prRef, label string) error
	CommentPR(ctx context.Context, number int, body string) error
}

// updateBranchRunner runs the update-branch workflow discovered at or above
// startDir and returns its structured result already decoded as an
// updateBranchResult. The typed bridge taboo.RunWorkflowAs[updateBranchResult]
// satisfies it; tests substitute a fake that returns a canned result directly,
// without provisioning a workshop or hand-encoding a <result> block.
type updateBranchRunner func(ctx context.Context, startDir, workflow string, vars map[string]string, ov taboo.PlanOverrides, cmd taboo.Commander) (updateBranchResult, taboo.OrchestratedResult, error)

// updateBranch is the testable core of the update-branch subcommand: resolve PR
// number's head branch, fetch origin, then run the update-branch workflow with its
// worktree started on the PR branch (the agent merges origin/main, resolves
// conflicts, and validates in-workshop), and push the advanced branch. The gh and
// taboo seams are injected so tests drive the full sequence with fakes.
func updateBranch(ctx context.Context, startDir string, pr int, out io.Writer, gh updateBranchGH, run updateBranchRunner) error {
	branch, err := gh.PRHeadBranch(ctx, pr)
	if err != nil {
		return fmt.Errorf("resolve PR #%d head branch: %w", pr, err)
	}

	if err := gh.Fetch(ctx); err != nil {
		return fmt.Errorf("fetch origin: %w", err)
	}

	upToDate, err := gh.UpToDateWithMain(ctx, branch)
	if err != nil {
		return fmt.Errorf("check whether %q is up to date with main: %w", branch, err)
	}
	if upToDate {
		// origin/main is already contained in the branch: a clean no-op — no
		// workshop, no commit, no push.
		return report(out, "PR #%d: branch %q is already up to date with main; nothing to do\n", pr, branch)
	}

	vars := map[string]string{
		"PR_NUMBER": strconv.Itoa(pr),
		"BRANCH":    branch,
	}
	res, _, err := run(ctx, startDir, "update-branch", vars, taboo.PlanOverrides{
		Branch:  branch,
		BaseRef: "origin/" + branch,
		Stdout:  os.Stderr,
		Stderr:  os.Stderr,
	}, taboo.NewExecCommander())
	if err != nil {
		return fmt.Errorf("run update-branch agent: %w", err)
	}

	if !res.Updated {
		// Nothing was merged (a race: main landed in the branch between the
		// up-to-date gate and the agent's merge). No new commit ⇒ nothing to
		// validate-gate or push.
		return report(out, "PR #%d: no merge was needed\n", pr)
	}

	if !res.Validated {
		// The merge produced a tree that does not pass in-workshop validation. Mark
		// the PR blocked with a diagnostic comment and do NOT push — the label +
		// comment are the durable signal a human acts on, so this is a handled
		// terminal outcome (returns nil), mirroring loop's blocked state. Label and
		// comment are best-effort: a failure to annotate must not mask the real
		// "validation failed" outcome — but if BOTH fail, the blocked state left no
		// trace, so surface that as an error rather than reporting a handled outcome.
		prRef := strconv.Itoa(pr)
		labelErr := gh.AddLabel(ctx, prRef, blockedLabel)
		if labelErr != nil {
			fmt.Fprintf(os.Stderr, "afk: add %s label on PR #%d: %v\n", blockedLabel, pr, labelErr)
		}
		commentErr := gh.CommentPR(ctx, pr, updateBlockedComment(pr, res.Summary))
		if commentErr != nil {
			fmt.Fprintf(os.Stderr, "afk: comment blocked on PR #%d: %v\n", pr, commentErr)
		}
		if labelErr != nil && commentErr != nil {
			return fmt.Errorf("PR #%d failed validation but could not be annotated (label: %v; comment: %v)", pr, labelErr, commentErr)
		}
		return report(out, "PR #%d: merge validation failed; marked %s (not pushed)\n", pr, blockedLabel)
	}

	if err := gh.Push(ctx, branch); err != nil {
		return fmt.Errorf("push updated branch: %w", err)
	}
	return report(out, "PR #%d: branch %q updated with main and pushed\n", pr, branch)
}

// report writes a terminal status line for the run to out, wrapping any write
// error so a failed write surfaces rather than being silently dropped. The gates
// in updateBranch each end by reporting their handled outcome through it.
func report(out io.Writer, format string, args ...any) error {
	if _, err := fmt.Fprintf(out, format, args...); err != nil {
		return fmt.Errorf("write update-branch status: %w", err)
	}
	return nil
}

// updateBlockedComment composes the explanatory comment update-branch posts when a
// merged tree fails in-workshop validation, naming the agent's summary and how to
// retry.
func updateBlockedComment(pr int, summary string) string {
	return fmt.Sprintf("Updating this branch with `main` failed in-workshop validation, so it was not pushed.\n\nAgent summary:\n\n```\n%s\n```\n\nResolve the issue and re-run `afk update-branch --pr %d` to retry.", summary, pr)
}
