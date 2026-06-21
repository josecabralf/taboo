package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"afk/internal/ghio"

	taboo "github.com/josecabralf/taboo/pkg"
)

// childIssue is one vertical-slice child the to-issues agent emits in its
// <result> JSON array, decoded by taboo.RunWorkflowAs[[]childIssue]. BlockedBy
// holds 0-based indices into that array (earlier entries only); toIssues resolves
// them to the real created issue numbers and writes them into the child's body as
// a "Blocked by #N" line.
type childIssue struct {
	Title     string `json:"title"`
	Body      string `json:"body"`
	BlockedBy []int  `json:"blocked_by"`
}

// toIssuesGH is the subset of GitHub I/O the to-issues sequence uses.
// *ghio.Client satisfies it; tests substitute a fake to record calls and args.
type toIssuesGH interface {
	IssueView(ctx context.Context, number int) (ghio.Issue, error)
	CreateIssue(ctx context.Context, title, body string, labels []string) (string, error)
}

// toIssuesRunner runs the to-issues workflow discovered at or above startDir and
// returns its structured result already decoded as a []childIssue. The typed
// bridge taboo.RunWorkflowAs[[]childIssue] satisfies it; tests substitute a fake
// returning a canned slice directly.
type toIssuesRunner func(ctx context.Context, startDir, workflow string, vars map[string]string, ov taboo.PlanOverrides, cmd taboo.Commander) ([]childIssue, taboo.OrchestratedResult, error)

// toIssues is the testable core of the to-issues subcommand: fetch the parent
// PRD issue, run the to-issues workflow (which decodes the agent's <result> block
// into a []childIssue in-loop via the bridge), then create each child issue
// carrying the ready-for-agent label and a back-link to the parent, printing each
// new issue's URL to out. The gh and taboo seams are injected so tests drive the
// full sequence with fakes.
func toIssues(ctx context.Context, startDir string, parent int, out io.Writer, gh toIssuesGH, run toIssuesRunner) error {
	iss, err := gh.IssueView(ctx, parent)
	if err != nil {
		return fmt.Errorf("fetch issue: %w", err)
	}

	vars := map[string]string{
		"ISSUE_NUMBER": strconv.Itoa(iss.Number),
		"ISSUE_TITLE":  iss.Title,
		"ISSUE_BODY":   iss.Body,
	}

	children, _, err := run(ctx, startDir, "to-issues", vars, taboo.PlanOverrides{
		Stdout: os.Stderr,
		Stderr: os.Stderr,
	}, taboo.NewExecCommander())
	if err != nil {
		return fmt.Errorf("run to-issues agent: %w", err)
	}

	if len(children) == 0 {
		fmt.Fprintln(os.Stderr, "afk: to-issues agent proposed no child issues")
		return nil
	}

	for i, child := range children {
		if strings.TrimSpace(child.Title) == "" {
			return fmt.Errorf("to-issues agent produced a child with an empty title (index %d)", i)
		}
	}

	created := make([]int, len(children))
	for i, child := range children {
		body := childBody(child.Body, parent, resolveBlockedBy(child.BlockedBy, created, i))
		url, err := gh.CreateIssue(ctx, child.Title, body, []string{readyLabel})
		if err != nil {
			return fmt.Errorf("create child issue %q: %w", child.Title, err)
		}
		created[i] = issueNumberFromURL(url)
		if _, err := fmt.Fprintln(out, url); err != nil {
			return fmt.Errorf("write issue url: %w", err)
		}
	}
	return nil
}

// childBody assembles a child issue's body: the agent-authored body, a
// "Part of #<parent>." back-link to the PRD issue it was decomposed from, and,
// when the child has resolved dependencies, a "Blocked by #N1, #N2" line that
// plan/loop's parseBlockedBy reads to honor the declared ordering.
func childBody(body string, parent int, blockedBy []int) string {
	out := fmt.Sprintf("%s\n\nPart of #%d.", body, parent)
	if len(blockedBy) > 0 {
		refs := make([]string, len(blockedBy))
		for i, n := range blockedBy {
			refs[i] = fmt.Sprintf("#%d", n)
		}
		out += "\n\nBlocked by " + strings.Join(refs, ", ")
	}
	return out
}

// issueNumberFromURL extracts the issue number from a gh-issue-create URL such as
// https://github.com/owner/repo/issues/42 by parsing its final path segment. It
// returns 0 when the trailing segment is not a positive integer; a 0 simply means
// a later child's blocked_by reference to this one cannot be resolved (and is
// dropped with a notice) rather than producing a bogus "Blocked by #0".
func issueNumberFromURL(url string) int {
	seg := url
	if i := strings.LastIndex(seg, "/"); i >= 0 {
		seg = seg[i+1:]
	}
	n, err := strconv.Atoi(strings.TrimSpace(seg))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// resolveBlockedBy maps a child's blocked_by indices — 0-based positions into the
// emitted batch — to the real GitHub numbers of the children already created. The
// created slice holds each batch index's number (0 until that child is created),
// and self is the index of the child being resolved: only earlier indices
// (0 <= ref < self) with a known number are honored; a forward, self,
// out-of-range or still-unknown reference is dropped with a stderr notice. The
// result is deduplicated in first-seen order so the rendered "Blocked by" line
// never repeats a number.
func resolveBlockedBy(refs []int, created []int, self int) []int {
	var nums []int
	seen := make(map[int]bool)
	for _, ref := range refs {
		if ref < 0 || ref >= self || created[ref] <= 0 {
			fmt.Fprintf(os.Stderr, "afk: child #%d: dropping blocked_by reference %d (not an already-created earlier child)\n", self, ref)
			continue
		}
		n := created[ref]
		if seen[n] {
			continue
		}
		seen[n] = true
		nums = append(nums, n)
	}
	return nums
}
