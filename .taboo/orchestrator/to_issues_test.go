package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"afk/internal/ghio"

	taboo "github.com/josecabralf/taboo/pkg"
)

// fakeToIssuesGH records the order and arguments of the gh calls toIssues makes
// and returns canned values (or injected errors) without shelling out.
type fakeToIssuesGH struct {
	calls []string

	issue    ghio.Issue
	issueArg int
	issueErr error

	createTitles  []string
	createBodies  []string
	createLabels  [][]string
	createBaseURL string
	createStart   int
	createErr     error
	createN       int
}

func (f *fakeToIssuesGH) IssueView(_ context.Context, number int) (ghio.Issue, error) {
	f.calls = append(f.calls, "IssueView")
	f.issueArg = number
	return f.issue, f.issueErr
}

func (f *fakeToIssuesGH) CreateIssue(_ context.Context, title, body string, labels []string) (string, error) {
	f.calls = append(f.calls, "CreateIssue")
	f.createTitles = append(f.createTitles, title)
	f.createBodies = append(f.createBodies, body)
	f.createLabels = append(f.createLabels, labels)
	if f.createErr != nil {
		return "", f.createErr
	}
	f.createN++
	return fmt.Sprintf("%s%d", f.createBaseURL, f.createStart+f.createN), nil
}

// fakeToIssuesRunner returns a toIssuesRunner that captures the vars and hands
// back a canned, already-decoded []childIssue (or an error). The bridge threads
// the JSONResult extractor into the run loop, so the fake returns a typed slice
// directly — no hand-encoded <result> string.
func fakeToIssuesRunner(capturedVars *map[string]string, children []childIssue, err error) toIssuesRunner {
	return func(_ context.Context, _, _ string, vars map[string]string, _ taboo.PlanOverrides, _ taboo.Commander) ([]childIssue, taboo.OrchestratedResult, error) {
		*capturedVars = vars
		return children, taboo.OrchestratedResult{}, err
	}
}

func TestToIssuesCreatesChildrenWithReadyLabel(t *testing.T) {
	t.Parallel()

	gh := &fakeToIssuesGH{
		issue:         ghio.Issue{Number: 5, Title: "Parent PRD", Body: "PRD body"},
		createBaseURL: "https://github.com/o/r/issues/",
	}
	var captured map[string]string
	children := []childIssue{
		{Title: "Child A", Body: "Do A"},
		{Title: "Child B", Body: "Do B"},
	}
	run := fakeToIssuesRunner(&captured, children, nil)

	var buf bytes.Buffer
	if err := toIssues(context.Background(), t.TempDir(), 5, &buf, gh, run); err != nil {
		t.Fatalf("toIssues returned error: %v", err)
	}

	wantCalls := []string{"IssueView", "CreateIssue", "CreateIssue"}
	if strings.Join(gh.calls, ",") != strings.Join(wantCalls, ",") {
		t.Errorf("gh call order = %v, want %v", gh.calls, wantCalls)
	}
	if gh.issueArg != 5 {
		t.Errorf("fetched issue #%d, want #5", gh.issueArg)
	}

	if captured["ISSUE_NUMBER"] != "5" {
		t.Errorf("ISSUE_NUMBER var = %q, want %q", captured["ISSUE_NUMBER"], "5")
	}
	if captured["ISSUE_TITLE"] != "Parent PRD" {
		t.Errorf("ISSUE_TITLE var = %q, want %q", captured["ISSUE_TITLE"], "Parent PRD")
	}
	if captured["ISSUE_BODY"] != "PRD body" {
		t.Errorf("ISSUE_BODY var = %q, want %q", captured["ISSUE_BODY"], "PRD body")
	}

	if len(gh.createTitles) != 2 {
		t.Fatalf("CreateIssue called %d times, want 2", len(gh.createTitles))
	}
	for i, labels := range gh.createLabels {
		want := []string{"ready-for-agent"}
		if strings.Join(labels, ",") != strings.Join(want, ",") {
			t.Errorf("CreateIssue[%d] labels = %v, want %v", i, labels, want)
		}
	}
	if gh.createTitles[0] != "Child A" || gh.createTitles[1] != "Child B" {
		t.Errorf("created titles = %v, want [Child A, Child B]", gh.createTitles)
	}
	for i, want := range []string{"Do A", "Do B"} {
		if !strings.Contains(gh.createBodies[i], want) {
			t.Errorf("created body[%d] = %q, want it to contain the agent body %q", i, gh.createBodies[i], want)
		}
		if !strings.Contains(gh.createBodies[i], "Part of #5.") {
			t.Errorf("created body[%d] = %q, want it to contain the back-link %q", i, gh.createBodies[i], "Part of #5.")
		}
	}

	want := "https://github.com/o/r/issues/1\nhttps://github.com/o/r/issues/2\n"
	if got := buf.String(); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestToIssuesWritesBlockedByWithRealNumbers(t *testing.T) {
	t.Parallel()

	gh := &fakeToIssuesGH{
		issue:         ghio.Issue{Number: 5, Title: "Parent PRD", Body: "PRD body"},
		createBaseURL: "https://github.com/o/r/issues/",
		createStart:   100, // issues/101, /102, /103
	}
	var captured map[string]string
	children := []childIssue{
		{Title: "A", Body: "do a"},
		{Title: "B", Body: "do b", BlockedBy: []int{0}},
		{Title: "C", Body: "do c", BlockedBy: []int{0, 1}},
	}
	run := fakeToIssuesRunner(&captured, children, nil)

	var buf bytes.Buffer
	if err := toIssues(context.Background(), t.TempDir(), 5, &buf, gh, run); err != nil {
		t.Fatalf("toIssues returned error: %v", err)
	}

	if len(gh.createBodies) != 3 {
		t.Fatalf("CreateIssue called %d times, want 3", len(gh.createBodies))
	}

	// Reuse the exact parser plan/loop use to verify round-tripping.
	if got := parseBlockedBy(gh.createBodies[0]); len(got) != 0 {
		t.Errorf("child A blocked_by = %v, want none", got)
	}
	if strings.Contains(gh.createBodies[0], "Blocked by") {
		t.Errorf("child A body = %q, want no \"Blocked by\" line", gh.createBodies[0])
	}
	if got := parseBlockedBy(gh.createBodies[1]); strings.Join(intsToStr(got), ",") != "101" {
		t.Errorf("child B blocked_by = %v, want [101]", got)
	}
	if got := parseBlockedBy(gh.createBodies[2]); strings.Join(intsToStr(got), ",") != "101,102" {
		t.Errorf("child C blocked_by = %v, want [101 102]", got)
	}

	for i, body := range gh.createBodies {
		if !strings.Contains(body, "Part of #5.") {
			t.Errorf("child body[%d] = %q, want it to contain %q", i, body, "Part of #5.")
		}
	}
}

func TestToIssuesDropsInvalidBlockedByRefs(t *testing.T) {
	t.Parallel()

	gh := &fakeToIssuesGH{
		issue:         ghio.Issue{Number: 9, Title: "Parent PRD", Body: "PRD body"},
		createBaseURL: "https://github.com/o/r/issues/",
		createStart:   199, // issues/200, /201
	}
	var captured map[string]string
	children := []childIssue{
		{Title: "A", Body: "a"},
		// 2 = out-of-range/forward (only 2 children), 1 = self, 5 = out-of-range,
		// 0 = valid earlier ref.
		{Title: "B", Body: "b", BlockedBy: []int{2, 1, 5, 0}},
	}
	run := fakeToIssuesRunner(&captured, children, nil)

	var buf bytes.Buffer
	if err := toIssues(context.Background(), t.TempDir(), 9, &buf, gh, run); err != nil {
		t.Fatalf("toIssues returned error: %v", err)
	}

	if len(gh.createBodies) != 2 {
		t.Fatalf("CreateIssue called %d times, want 2", len(gh.createBodies))
	}
	if got := parseBlockedBy(gh.createBodies[1]); strings.Join(intsToStr(got), ",") != "200" {
		t.Errorf("child B blocked_by = %v, want [200] (invalid refs dropped)", got)
	}
}

func TestToIssuesRejectsEmptyChildTitle(t *testing.T) {
	t.Parallel()

	gh := &fakeToIssuesGH{
		issue:         ghio.Issue{Number: 5, Title: "Parent PRD", Body: "PRD body"},
		createBaseURL: "https://github.com/o/r/issues/",
	}
	var captured map[string]string
	children := []childIssue{
		{Title: "A", Body: "a"},
		{Title: "  ", Body: "b"},
	}
	run := fakeToIssuesRunner(&captured, children, nil)

	err := toIssues(context.Background(), t.TempDir(), 5, io.Discard, gh, run)
	if err == nil {
		t.Fatal("toIssues returned nil, want an error for an empty child title")
	}
	if !strings.Contains(err.Error(), "empty title") {
		t.Errorf("error = %q, want it to mention an empty title", err.Error())
	}
	// The whole batch is rejected before any creation: not even the valid first
	// child must be created when a later one has an empty title.
	if n := countCalls(gh.calls, "CreateIssue"); n != 0 {
		t.Errorf("CreateIssue called %d times, want 0 (batch rejected before any creation)", n)
	}
	if countCalls(gh.calls, "IssueView") != 1 {
		t.Errorf("gh calls = %v, want IssueView to have run", gh.calls)
	}
}

func TestToIssuesProposesNoChildrenIsGracefulNoop(t *testing.T) {
	t.Parallel()

	gh := &fakeToIssuesGH{
		issue:         ghio.Issue{Number: 5, Title: "Parent PRD", Body: "PRD body"},
		createBaseURL: "https://github.com/o/r/issues/",
	}
	var captured map[string]string
	run := fakeToIssuesRunner(&captured, []childIssue{}, nil)

	var buf bytes.Buffer
	if err := toIssues(context.Background(), t.TempDir(), 5, &buf, gh, run); err != nil {
		t.Fatalf("toIssues returned error: %v, want a graceful no-op", err)
	}
	if n := countCalls(gh.calls, "CreateIssue"); n != 0 {
		t.Errorf("CreateIssue called %d times, want 0 when no children are proposed", n)
	}
	if got := buf.String(); got != "" {
		t.Errorf("stdout = %q, want empty when no children are proposed", got)
	}
}

func TestToIssuesSurfacesIssueViewError(t *testing.T) {
	t.Parallel()

	gh := &fakeToIssuesGH{issueErr: errors.New("gh view failed")}
	var captured map[string]string
	run := fakeToIssuesRunner(&captured, []childIssue{{Title: "A", Body: "a"}}, nil)

	err := toIssues(context.Background(), t.TempDir(), 5, io.Discard, gh, run)
	if err == nil {
		t.Fatal("toIssues returned nil, want the issue-fetch error surfaced")
	}
	if !strings.Contains(err.Error(), "fetch issue") {
		t.Errorf("error = %q, want it to wrap the issue-fetch failure", err.Error())
	}
	// The agent must not run and no issue must be created once the fetch fails.
	if captured != nil {
		t.Errorf("agent ran (vars=%v) after a fetch failure, want it skipped", captured)
	}
	if n := countCalls(gh.calls, "CreateIssue"); n != 0 {
		t.Errorf("CreateIssue called %d times, want 0 after a fetch failure", n)
	}
}

func TestToIssuesSurfacesRunFailure(t *testing.T) {
	t.Parallel()

	// When the agent emits no usable <result> block the bridge returns ErrNoResult
	// after the run. to-issues must surface it (wrapped) and create nothing.
	gh := &fakeToIssuesGH{issue: ghio.Issue{Number: 5, Title: "Parent PRD", Body: "PRD body"}}
	var captured map[string]string
	run := fakeToIssuesRunner(&captured, nil, taboo.ErrNoResult)

	err := toIssues(context.Background(), t.TempDir(), 5, io.Discard, gh, run)
	if err == nil {
		t.Fatal("toIssues returned nil, want a run error")
	}
	if !errors.Is(err, taboo.ErrNoResult) {
		t.Errorf("error = %v, want it to wrap taboo.ErrNoResult", err)
	}
	if !strings.HasPrefix(err.Error(), "run to-issues agent: ") {
		t.Errorf("error = %q, want it to start with %q", err.Error(), "run to-issues agent: ")
	}
	if n := countCalls(gh.calls, "CreateIssue"); n != 0 {
		t.Errorf("CreateIssue called %d times, want 0 after a run failure", n)
	}
}

func TestToIssuesSurfacesCreateError(t *testing.T) {
	t.Parallel()

	gh := &fakeToIssuesGH{
		issue:     ghio.Issue{Number: 5, Title: "Parent PRD", Body: "PRD body"},
		createErr: errors.New("gh create failed"),
	}
	var captured map[string]string
	run := fakeToIssuesRunner(&captured, []childIssue{{Title: "Child A", Body: "do a"}}, nil)

	err := toIssues(context.Background(), t.TempDir(), 5, io.Discard, gh, run)
	if err == nil {
		t.Fatal("toIssues returned nil, want the create error surfaced")
	}
	if !strings.Contains(err.Error(), "create child issue") {
		t.Errorf("error = %q, want it to wrap the create failure", err.Error())
	}
	if !strings.Contains(err.Error(), "Child A") {
		t.Errorf("error = %q, want it to name the child whose creation failed", err.Error())
	}
}

// intsToStr renders a []int as []string for stable comparison in assertions.
func intsToStr(ns []int) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = fmt.Sprintf("%d", n)
	}
	return out
}

// Ensure *ghio.Client satisfies toIssuesGH and the typed bridge satisfies
// toIssuesRunner, so the production wiring in runToIssues stays type-correct.
var (
	_ toIssuesGH     = (*ghio.Client)(nil)
	_ toIssuesRunner = taboo.RunWorkflowAs[[]childIssue]
)
