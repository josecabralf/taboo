// Package ghio performs the orchestrator's GitHub and git I/O by shelling out to
// the gh and git CLIs. All side effects funnel through the Exec seam, so tests
// substitute a fake instead of running real processes.
package ghio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

// Exec runs a command and returns its stdout. It is the single side-effecting
// seam in ghio: production shells out via os/exec; tests substitute a fake.
// RunInput is the same, but feeds stdin to the command — used to POST a JSON
// request body to gh api.
type Exec interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
	RunInput(ctx context.Context, stdin, name string, args ...string) (string, error)
}

// execRunner is the production Exec: it shells out via os/exec.
type execRunner struct{}

// NewExec returns the production Exec that runs real host processes, inheriting
// the current environment (so gh reads GH_REPO / GH_TOKEN from it).
func NewExec() Exec { return execRunner{} }

func (execRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	return run(ctx, nil, name, args...)
}

func (execRunner) RunInput(ctx context.Context, stdin, name string, args ...string) (string, error) {
	return run(ctx, strings.NewReader(stdin), name, args...)
}

// run shells out, optionally feeding stdin, and returns stdout. A non-nil stdin
// is wired to the child's standard input.
func run(ctx context.Context, stdin io.Reader, name string, args ...string) (string, error) {
	// The command and args originate from the orchestrator, not end users; gh and
	// git inherit this process's environment by default.
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204
	cmd.Stdin = stdin
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("%s %v: %w: %s", name, args, err, ee.Stderr)
		}
		return "", fmt.Errorf("%s %v: %w", name, args, err)
	}
	return string(out), nil
}

// Client performs the orchestrator's GitHub/git I/O through gh and git.
type Client struct {
	exec Exec
}

// New returns a Client that drives gh/git through the given Exec.
func New(exec Exec) *Client { return &Client{exec: exec} }

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
	out, err := c.exec.Run(ctx, "gh", "issue", "view", strconv.Itoa(number), "--json", "number,title,body")
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
	out, err := c.exec.Run(ctx, "gh", "issue", "view", strconv.Itoa(number), "--json", "state")
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
	out, err := c.exec.Run(ctx, "gh", "issue", "list", "--label", label, "--state", "open", "--limit", "100", "--json", "number,title,body")
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
	_, err := c.exec.Run(ctx, "git", "push", "--force", "origin", branch)
	return err
}

// CreateDraftPR opens a draft PR for the run's branch against main and returns
// its URL.
func (c *Client) CreateDraftPR(ctx context.Context, branch, title, body string) (string, error) {
	out, err := c.exec.Run(ctx, "gh", "pr", "create", "--draft",
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
	return c.exec.Run(ctx, "gh", "pr", "diff", strconv.Itoa(number))
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
	_, err = c.exec.RunInput(ctx, string(body), "gh", "api", "--method", "POST", path, "--input", "-")
	return err
}

// AddLabel adds a label to a PR (or issue) referenced by URL or number.
func (c *Client) AddLabel(ctx context.Context, prRef, label string) error {
	_, err := c.exec.Run(ctx, "gh", "pr", "edit", prRef, "--add-label", label)
	return err
}

// AddIssueLabel adds a label to the issue via `gh issue edit`. The loop
// subcommand uses it to advance an issue through its label state machine.
func (c *Client) AddIssueLabel(ctx context.Context, number int, label string) error {
	_, err := c.exec.Run(ctx, "gh", "issue", "edit", strconv.Itoa(number), "--add-label", label)
	return err
}

// RemoveIssueLabel removes a label from the issue via `gh issue edit`. The loop
// subcommand uses it to clear a prior state as the issue advances.
func (c *Client) RemoveIssueLabel(ctx context.Context, number int, label string) error {
	_, err := c.exec.Run(ctx, "gh", "issue", "edit", strconv.Itoa(number), "--remove-label", label)
	return err
}

// CommentIssue posts a comment on the issue via `gh issue comment`. The loop
// subcommand uses it to record state transitions in the issue thread.
func (c *Client) CommentIssue(ctx context.Context, number int, body string) error {
	_, err := c.exec.Run(ctx, "gh", "issue", "comment", strconv.Itoa(number), "--body", body)
	return err
}

// CurrentBranch returns the name of the branch currently checked out in the
// working tree via `git rev-parse --abbrev-ref HEAD`. The write-pr subcommand
// uses it to default --branch to the run's own branch.
func (c *Client) CurrentBranch(ctx context.Context) (string, error) {
	out, err := c.exec.Run(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
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
	return c.exec.Run(ctx, "git", "diff", "main..."+branch)
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
	out, err := c.exec.Run(ctx, "gh", "pr", "list", "--head", branch, "--state", "open", "--limit", "1", "--json", "number,url")
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
	out, err := c.exec.Run(ctx, "gh", "pr", "create",
		"--base", "main",
		"--head", branch,
		"--title", title,
		"--body", body)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// EditPR updates an existing PR's title and body via `gh pr edit`. The prRef
// argument is the PR's number, URL or head branch (write-pr passes the URL
// PRForBranch returned). It is how a re-run refreshes a PR in place instead of
// opening a duplicate.
func (c *Client) EditPR(ctx context.Context, prRef, title, body string) error {
	_, err := c.exec.Run(ctx, "gh", "pr", "edit", prRef,
		"--title", title,
		"--body", body)
	return err
}
