package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"afk/internal/diffmap"
	"afk/internal/ghio"

	"github.com/josecabralf/taboo"
)

// reviewGH is the subset of GitHub I/O the review sequence uses. *ghio.Client
// satisfies it; tests substitute a fake to record calls and args.
type reviewGH interface {
	PRDiff(ctx context.Context, number int) (string, error)
	PostReview(ctx context.Context, number int, summary string, comments []ghio.ReviewComment) error
}

// reviewResult is the structured output the review agent emits in its <result>
// block: a top-level summary plus inline comments anchored to the PR diff. It is
// the T the review run is decoded into by taboo.RunWorkflowAs[reviewResult].
type reviewResult struct {
	Summary  string          `json:"summary"`
	Comments []reviewComment `json:"comments"`
}

// reviewComment is one inline comment the agent proposes, anchored to a path and
// new-side line. A comment whose position is not addressable in the diff is
// dropped, never raised as an error.
type reviewComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Body string `json:"body"`
}

// reviewRunner runs the review workflow discovered at or above startDir and
// returns its structured result already decoded as a reviewResult. The typed
// bridge taboo.RunWorkflowAs[reviewResult] satisfies it; tests substitute a fake
// that returns a canned reviewResult directly, without provisioning a workshop or
// hand-encoding a <result> block.
type reviewRunner func(ctx context.Context, startDir, workflow string, vars map[string]string, ov taboo.PlanOverrides, cmd taboo.Commander) (reviewResult, taboo.OrchestratedResult, error)

// review is the testable core of the review subcommand: fetch the PR diff, run
// the review workflow (which decodes the agent's <result> block into a
// reviewResult in-loop via the bridge's JSONResult extractor), drop any inline
// comment whose path:line is not addressable in the diff, and post exactly one PR
// review — skipping the post entirely when nothing survives, to avoid a GitHub
// 422. The gh and taboo seams are injected so tests drive the full sequence with
// fakes.
func review(ctx context.Context, startDir string, pr int, gh reviewGH, runReview reviewRunner) error {
	diff, err := gh.PRDiff(ctx, pr)
	if err != nil {
		return fmt.Errorf("fetch PR diff: %w", err)
	}

	vars := map[string]string{
		"PR_NUMBER": strconv.Itoa(pr),
		"PR_DIFF":   diff,
	}

	rr, _, err := runReview(ctx, startDir, "review", vars, taboo.PlanOverrides{
		Branch: reviewBranch(pr),
		Stdout: os.Stderr,
		Stderr: os.Stderr,
	}, taboo.NewExecCommander())
	if err != nil {
		// A missing/invalid <result> block surfaces here as ErrNoResult /
		// ErrInvalidResult (the bridge threads the extractor into the run loop).
		return fmt.Errorf("run review agent: %w", err)
	}

	// Drop any comment whose path:line is not on the new side of the diff. A
	// phantom position is dropped (with a notice), never an error.
	positions := diffmap.Parse(diff)
	var inDiff []ghio.ReviewComment
	for _, c := range rr.Comments {
		if !positions.Has(c.Path, c.Line) {
			fmt.Fprintf(os.Stderr, "afk: dropping out-of-diff inline comment: %s:%d\n", c.Path, c.Line)
			continue
		}
		inDiff = append(inDiff, ghio.ReviewComment{Path: c.Path, Line: c.Line, Body: c.Body})
	}

	// A COMMENT review with no body and no comments is a GitHub 422. If the agent
	// had nothing to say (an empty or whitespace-only summary) and every inline
	// comment was dropped, skip the post rather than fail — degrade gracefully,
	// never error.
	if strings.TrimSpace(rr.Summary) == "" && len(inDiff) == 0 {
		fmt.Fprintln(os.Stderr, "afk: review is clean (empty summary, no in-diff comments); nothing to post")
		return nil
	}

	if err := gh.PostReview(ctx, pr, rr.Summary, inDiff); err != nil {
		return fmt.Errorf("post review: %w", err)
	}
	return nil
}

// reviewBranch is the throwaway branch the review run creates. The review agent
// makes no code change and the branch is never pushed; the name only needs to be
// deterministic per PR.
func reviewBranch(pr int) string {
	return fmt.Sprintf("agent/review-pr-%d", pr)
}
