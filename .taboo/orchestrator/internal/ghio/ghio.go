// Package ghio performs the orchestrator's GitHub and git I/O by shelling out to
// the gh and git CLIs. All side effects funnel through the Exec seam, so tests
// substitute a fake instead of running real processes.
package ghio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Exec runs a command and returns its stdout. It is the single side-effecting
// seam in ghio: production shells out via os/exec; tests substitute a fake.
type Exec interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

// execRunner is the production Exec: it shells out via os/exec.
type execRunner struct{}

// NewExec returns the production Exec that runs real host processes, inheriting
// the current environment (so gh reads GH_REPO / GH_TOKEN from it).
func NewExec() Exec { return execRunner{} }

func (execRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	// The command and args originate from the orchestrator, not end users; gh and
	// git inherit this process's environment by default.
	out, err := exec.CommandContext(ctx, name, args...).Output() // #nosec G204
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

// Issue holds the fields the orchestrator injects into the implement prompt.
type Issue struct {
	Number int
	Title  string
	Body   string
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

// AddLabel adds a label to a PR (or issue) referenced by URL or number.
func (c *Client) AddLabel(ctx context.Context, prRef, label string) error {
	_, err := c.exec.Run(ctx, "gh", "pr", "edit", prRef, "--add-label", label)
	return err
}
