package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"afk/internal/ghio"

	taboo "github.com/josecabralf/taboo/pkg"
)

// prContent is the structured output the write-pr agent emits in its <result>
// block: the title and body for the pull request. It is the T the write-pr run is
// decoded into by taboo.RunWorkflowAs[prContent].
type prContent struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// writePRGH is the subset of GitHub/git I/O the write-pr sequence uses.
// *ghio.Client satisfies it; tests substitute a fake to record calls and args.
type writePRGH interface {
	CurrentBranch(ctx context.Context) (string, error)
	BranchDiff(ctx context.Context, branch string) (string, error)
	PRForBranch(ctx context.Context, branch string) (ghio.PR, bool, error)
	CreatePR(ctx context.Context, branch, title, body string) (string, error)
	EditPR(ctx context.Context, prRef, title, body string) error
}

// writePRRunner runs the write-pr workflow discovered at or above startDir and
// returns its structured result already decoded as a prContent. The typed bridge
// taboo.RunWorkflowAs[prContent] satisfies it; tests substitute a fake that
// returns a canned prContent directly, without provisioning a workshop or
// hand-encoding a <result> block.
type writePRRunner func(ctx context.Context, startDir, workflow string, vars map[string]string, ov taboo.PlanOverrides, cmd taboo.Commander) (prContent, taboo.OrchestratedResult, error)

// writePR is the testable core of the write-pr subcommand: resolve the branch
// (defaulting to the one currently checked out), compute its diff against main,
// run the write-pr workflow (which decodes the agent's <result> block into a
// prContent in-loop via the bridge), then open a PR for the branch — or, when the
// branch already has an open PR, update that one in place so a re-run refreshes the
// same-repo branch's PR instead of opening a duplicate — carrying the agent's title
// and body, and print the PR URL to out. An empty diff and an empty agent title are
// surfaced as errors before any PR is touched. The branch must already exist on the
// remote: write-pr opens or edits the PR but does not push it (the implement flow
// and the workflow layer own pushing). The gh and taboo seams are injected so tests
// drive the full sequence with fakes.
func writePR(ctx context.Context, startDir, branch string, out io.Writer, gh writePRGH, runWritePR writePRRunner) error {
	if branch == "" {
		cur, err := gh.CurrentBranch(ctx)
		if err != nil {
			return fmt.Errorf("resolve current branch: %w", err)
		}
		branch = cur
	}
	if branch == "" || branch == "HEAD" {
		return fmt.Errorf("could not resolve a branch to open a PR for (detached HEAD?); pass --branch")
	}

	diff, err := gh.BranchDiff(ctx, branch)
	if err != nil {
		return fmt.Errorf("diff branch against main: %w", err)
	}
	if strings.TrimSpace(diff) == "" {
		return fmt.Errorf("branch %q has no changes against main", branch)
	}

	vars := map[string]string{
		"BRANCH": branch,
		"DIFF":   diff,
	}

	content, _, err := runWritePR(ctx, startDir, "write-pr", vars, taboo.PlanOverrides{
		Stdout: os.Stderr,
		Stderr: os.Stderr,
	}, taboo.NewExecCommander())
	if err != nil {
		return fmt.Errorf("run write-pr agent: %w", err)
	}
	if strings.TrimSpace(content.Title) == "" {
		return fmt.Errorf("write-pr agent produced an empty title")
	}

	existing, found, err := gh.PRForBranch(ctx, branch)
	if err != nil {
		return fmt.Errorf("look up existing PR: %w", err)
	}

	var url string
	if found {
		if err := gh.EditPR(ctx, existing.URL, content.Title, content.Body); err != nil {
			return fmt.Errorf("update PR: %w", err)
		}
		url = existing.URL
	} else {
		url, err = gh.CreatePR(ctx, branch, content.Title, content.Body)
		if err != nil {
			return fmt.Errorf("create PR: %w", err)
		}
	}

	if _, err := fmt.Fprintln(out, url); err != nil {
		return fmt.Errorf("write PR url: %w", err)
	}
	return nil
}
