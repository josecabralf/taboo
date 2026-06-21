// Package ghio performs the orchestrator's GitHub and git I/O by shelling out to
// the gh and git CLIs. All side effects funnel through taboo's Commander seam
// (via taboo.Output), so tests substitute a fake Commander instead of running
// real processes.
package ghio

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/josecabralf/taboo"
)

// Client performs the orchestrator's GitHub/git I/O through gh and git.
type Client struct {
	cmd taboo.Commander
}

// New returns a Client that drives gh/git through the given Commander seam.
func New(cmd taboo.Commander) *Client { return &Client{cmd: cmd} }

// run shells out via the Commander seam and returns stdout. On failure the
// error carries the command's stderr (taboo.Output folds it in).
func (c *Client) run(ctx context.Context, name string, args ...string) (string, error) {
	return taboo.Output(ctx, c.cmd, taboo.Cmd{Name: name, Args: args})
}

// runInput is run with stdin fed to the child — used to POST a JSON body to gh api.
func (c *Client) runInput(ctx context.Context, stdin, name string, args ...string) (string, error) {
	return taboo.Output(ctx, c.cmd, taboo.Cmd{Name: name, Args: args, Stdin: strings.NewReader(stdin)})
}

// Issue holds the issue fields the orchestrator passes to the agents — injected
// into the implement prompt and marshaled into the plan prompt's candidate list.
// The JSON tags let it both decode `gh`'s output and marshal cleanly into that
// candidate list; it stays comparable (no slice fields) so tests can assert
// equality directly.
type Issue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
}

// IssueView fetches an issue's number, title and body via `gh issue view`.
func (c *Client) IssueView(ctx context.Context, number int) (Issue, error) {
	out, err := c.run(ctx, "gh", "issue", "view", strconv.Itoa(number), "--json", "number,title,body")
	if err != nil {
		return Issue{}, err
	}
	var i Issue
	if err := json.Unmarshal([]byte(out), &i); err != nil {
		return Issue{}, fmt.Errorf("parsing gh issue view output: %w", err)
	}
	return i, nil
}

// IssueState returns an issue's state ("OPEN" or "CLOSED") via `gh issue view`.
// The plan subcommand uses it to tell whether a "Blocked by #N" dependency has
// been resolved.
func (c *Client) IssueState(ctx context.Context, number int) (string, error) {
	out, err := c.run(ctx, "gh", "issue", "view", strconv.Itoa(number), "--json", "state")
	if err != nil {
		return "", err
	}
	var s struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		return "", fmt.Errorf("parsing gh issue view output: %w", err)
	}
	return s.State, nil
}

// ListOpenIssuesByLabel returns the open issues carrying the given label via
// `gh issue list`, parsing each issue's number, title and body.
func (c *Client) ListOpenIssuesByLabel(ctx context.Context, label string) ([]Issue, error) {
	out, err := c.run(ctx, "gh", "issue", "list", "--label", label, "--state", "open", "--limit", "100", "--json", "number,title,body")
	if err != nil {
		return nil, err
	}
	var issues []Issue
	if err := json.Unmarshal([]byte(out), &issues); err != nil {
		return nil, fmt.Errorf("parsing gh issue list output: %w", err)
	}
	return issues, nil
}

// PushBranch force-pushes the run's branch to origin. The branch name is
// deterministic per issue, so a retrigger overwrites the stale agent branch.
func (c *Client) PushBranch(ctx context.Context, branch string) error {
	_, err := c.run(ctx, "git", "push", "--force", "origin", branch)
	return err
}

// CreateDraftPR opens a draft PR for the run's branch against main and returns
// its URL.
func (c *Client) CreateDraftPR(ctx context.Context, branch, title, body string) (string, error) {
	out, err := c.run(ctx, "gh", "pr", "create", "--draft",
		"--base", "main",
		"--head", branch,
		"--title", title,
		"--body", body)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// PRDiff returns the PR's unified diff via `gh pr diff`. It is the input the
// review workflow reasons over and the source of the addressable positions a
// review comment may target.
func (c *Client) PRDiff(ctx context.Context, number int) (string, error) {
	return c.run(ctx, "gh", "pr", "diff", strconv.Itoa(number))
}

// ReviewComment is one inline review comment, anchored to a path and new-side
// line of the PR diff. Callers are responsible for ensuring the position is
// addressable (see internal/diffmap); GitHub rejects out-of-diff positions.
type ReviewComment struct {
	Path string
	Line int
	Body string
}

// PostReview posts exactly one PR review via `gh api`: a single COMMENT review
// whose top-level body is summary and whose inline comments are anchored to the
// new (RIGHT) side of the diff. The JSON request body is fed on stdin. The
// commit_id field is omitted, so GitHub anchors the review to the PR's latest
// commit — the same commit `gh pr diff` rendered. Callers must avoid posting an
// empty review (no body and no comments), which GitHub rejects with a 422.
func (c *Client) PostReview(ctx context.Context, number int, summary string, comments []ReviewComment) error {
	type apiComment struct {
		Path string `json:"path"`
		Line int    `json:"line"`
		Side string `json:"side"`
		Body string `json:"body"`
	}
	// A non-nil slice serializes as [] rather than null; GitHub rejects null.
	apiComments := make([]apiComment, 0, len(comments))
	for _, c := range comments {
		apiComments = append(apiComments, apiComment{Path: c.Path, Line: c.Line, Side: "RIGHT", Body: c.Body})
	}
	payload := struct {
		Event    string       `json:"event"`
		Body     string       `json:"body"`
		Comments []apiComment `json:"comments"`
	}{Event: "COMMENT", Body: summary, Comments: apiComments}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal review payload: %w", err)
	}

	path := fmt.Sprintf("repos/{owner}/{repo}/pulls/%d/reviews", number)
	_, err = c.runInput(ctx, string(body), "gh", "api", "--method", "POST", path, "--input", "-")
	return err
}

// AddLabel adds a label to a PR (or issue) referenced by URL or number.
func (c *Client) AddLabel(ctx context.Context, prRef, label string) error {
	_, err := c.run(ctx, "gh", "pr", "edit", prRef, "--add-label", label)
	return err
}

// AddIssueLabel adds a label to the issue via `gh issue edit`. The loop
// subcommand uses it to advance an issue through its label state machine.
func (c *Client) AddIssueLabel(ctx context.Context, number int, label string) error {
	_, err := c.run(ctx, "gh", "issue", "edit", strconv.Itoa(number), "--add-label", label)
	return err
}

// RemoveIssueLabel removes a label from the issue via `gh issue edit`. The loop
// subcommand uses it to clear a prior state as the issue advances.
func (c *Client) RemoveIssueLabel(ctx context.Context, number int, label string) error {
	_, err := c.run(ctx, "gh", "issue", "edit", strconv.Itoa(number), "--remove-label", label)
	return err
}

// CommentIssue posts a comment on the issue via `gh issue comment`. The loop
// subcommand uses it to record state transitions in the issue thread.
func (c *Client) CommentIssue(ctx context.Context, number int, body string) error {
	_, err := c.run(ctx, "gh", "issue", "comment", strconv.Itoa(number), "--body", body)
	return err
}

// CurrentBranch returns the name of the branch currently checked out in the
// working tree via `git rev-parse --abbrev-ref HEAD`. The write-pr subcommand
// uses it to default --branch to the run's own branch.
func (c *Client) CurrentBranch(ctx context.Context) (string, error) {
	out, err := c.run(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// BranchDiff returns the unified diff of branch against main using the three-dot
// range `main...<branch>` — the changes introduced on the branch since it
// diverged from main, the same set GitHub renders for a PR. It is the input the
// write-pr workflow reasons over when composing the PR title and body.
func (c *Client) BranchDiff(ctx context.Context, branch string) (string, error) {
	return c.run(ctx, "git", "diff", "main..."+branch)
}

// PR identifies an open pull request the orchestrator found for a branch: its
// number and URL. The write-pr subcommand uses it to decide whether to update an
// existing PR (an idempotent re-run) or open a new one.
type PR struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

// PRForBranch returns the open PR whose head is branch, if one exists, via
// `gh pr list --head <branch>`. The bool is false when no open PR targets the
// branch (the JSON array is empty), so write-pr can choose between create and
// update without treating "none" as an error.
func (c *Client) PRForBranch(ctx context.Context, branch string) (PR, bool, error) {
	out, err := c.run(ctx, "gh", "pr", "list", "--head", branch, "--state", "open", "--limit", "1", "--json", "number,url")
	if err != nil {
		return PR{}, false, err
	}
	var prs []PR
	if err := json.Unmarshal([]byte(out), &prs); err != nil {
		return PR{}, false, fmt.Errorf("parsing gh pr list output: %w", err)
	}
	if len(prs) == 0 {
		return PR{}, false, nil
	}
	return prs[0], true, nil
}

// CreatePR opens a ready (non-draft) PR for branch against main and returns its
// URL. Unlike CreateDraftPR (the implement flow's work-in-progress draft),
// write-pr produces a finished PR from an agent-authored title and body.
func (c *Client) CreatePR(ctx context.Context, branch, title, body string) (string, error) {
	out, err := c.run(ctx, "gh", "pr", "create",
		"--base", "main",
		"--head", branch,
		"--title", title,
		"--body", body)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CreateIssue opens an issue with the given title, body and labels via
// `gh issue create`, returning the new issue's URL. Each label is applied with a
// repeated --label flag and must already exist in the repo. The to-issues
// subcommand uses it to materialize a PRD issue's child issues.
func (c *Client) CreateIssue(ctx context.Context, title, body string, labels []string) (string, error) {
	args := []string{"issue", "create", "--title", title, "--body", body}
	for _, l := range labels {
		args = append(args, "--label", l)
	}
	out, err := c.run(ctx, "gh", args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// PRHeadBranch returns PR number's head branch name via `gh pr view`. The
// update-branch subcommand uses it to resolve which branch to merge main into.
func (c *Client) PRHeadBranch(ctx context.Context, number int) (string, error) {
	out, err := c.run(ctx, "gh", "pr", "view", strconv.Itoa(number), "--json", "headRefName")
	if err != nil {
		return "", err
	}
	var v struct {
		HeadRefName string `json:"headRefName"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return "", fmt.Errorf("parsing gh pr view output: %w", err)
	}
	return v.HeadRefName, nil
}

// Fetch updates the remote-tracking refs from origin via `git fetch origin`, so
// origin/main and origin/<branch> are current before the update-branch run reads
// or merges them.
func (c *Client) Fetch(ctx context.Context) error {
	_, err := c.run(ctx, "git", "fetch", "origin")
	return err
}

// UpToDateWithMain reports whether origin/<branch> already contains origin/main —
// i.e. merging main into the branch would be a no-op. It compares
// merge-base(origin/main, origin/<branch>) against origin/main's tip: equal means
// main is an ancestor of the branch. Both shell-outs succeed-or-error (no
// exit-code-1 "false" signal), so the method composes cleanly with run()'s error
// handling — unlike `git merge-base --is-ancestor`, whose exit code 1 is a valid
// answer that run() would surface as an error. Call Fetch first so the refs are
// current.
func (c *Client) UpToDateWithMain(ctx context.Context, branch string) (bool, error) {
	base, err := c.run(ctx, "git", "merge-base", "origin/main", "origin/"+branch)
	if err != nil {
		return false, err
	}
	mainTip, err := c.run(ctx, "git", "rev-parse", "origin/main")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(base) == strings.TrimSpace(mainTip), nil
}

// Push pushes branch to origin WITHOUT --force. The update-branch flow advances
// the branch by merging main, so its new tip descends from the remote branch and
// the push is a fast-forward; a non-force push then fails safely (rather than
// clobbering) if the remote branch moved under us. This is deliberately distinct
// from PushBranch, which force-pushes the per-issue agent branch implement owns.
func (c *Client) Push(ctx context.Context, branch string) error {
	_, err := c.run(ctx, "git", "push", "origin", branch)
	return err
}

// CommentPR posts a comment on a PR via `gh pr comment`. The update-branch flow
// uses it to record why a merge was blocked, alongside the agent:blocked label.
func (c *Client) CommentPR(ctx context.Context, number int, body string) error {
	_, err := c.run(ctx, "gh", "pr", "comment", strconv.Itoa(number), "--body", body)
	return err
}

// MarkPRReady transitions a PR from draft to ready-for-review via
// `gh pr ready`. The prRef argument is the PR's number, URL or head branch (the
// finalize stage passes the URL PRForBranch returned). Re-running on an
// already-ready PR is harmless: gh treats it as a no-op and exits 0. Any gh
// failure is returned verbatim.
func (c *Client) MarkPRReady(ctx context.Context, prRef string) error {
	_, err := c.run(ctx, "gh", "pr", "ready", prRef)
	return err
}

// EditPR updates an existing PR's title and body via `gh pr edit`. The prRef
// argument is the PR's number, URL or head branch (write-pr passes the URL
// PRForBranch returned). It is how a re-run refreshes a PR in place instead of
// opening a duplicate.
func (c *Client) EditPR(ctx context.Context, prRef, title, body string) error {
	_, err := c.run(ctx, "gh", "pr", "edit", prRef,
		"--title", title,
		"--body", body)
	return err
}
