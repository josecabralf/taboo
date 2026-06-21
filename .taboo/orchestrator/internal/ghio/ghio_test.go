package ghio

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

// errBoom is the canned error fakeExec returns so tests can assert a Client
// method propagates the underlying exec failure unchanged.
var errBoom = errors.New("boom")

// fakeExec records the (name, args) of each call and returns canned stdout, so
// tests can assert the exact argv built by the Client and feed it scripted
// output without shelling out.
type fakeExec struct {
	name   string
	args   []string
	stdin  string
	stdout string
	err    error
}

func (f *fakeExec) Run(_ context.Context, name string, args ...string) (string, error) {
	f.name = name
	f.args = args
	return f.stdout, f.err
}

func (f *fakeExec) RunInput(_ context.Context, stdin, name string, args ...string) (string, error) {
	f.name = name
	f.args = args
	f.stdin = stdin
	return f.stdout, f.err
}

func TestIssueViewBuildsArgvAndParsesIssue(t *testing.T) {
	t.Parallel()

	fe := &fakeExec{stdout: `{"number":78,"title":"Some title","body":"Some body"}`}
	c := New(fe)

	got, err := c.IssueView(context.Background(), 78)
	if err != nil {
		t.Fatalf("IssueView returned error: %v", err)
	}

	if fe.name != "gh" {
		t.Errorf("ran %q, want %q", fe.name, "gh")
	}
	wantArgs := []string{"issue", "view", "78", "--json", "number,title,body"}
	if !slices.Equal(fe.args, wantArgs) {
		t.Errorf("args = %q, want %q", fe.args, wantArgs)
	}

	want := Issue{Number: 78, Title: "Some title", Body: "Some body"}
	if got != want {
		t.Errorf("issue = %+v, want %+v", got, want)
	}
}

func TestIssueStateBuildsArgvAndParses(t *testing.T) {
	t.Parallel()

	fe := &fakeExec{stdout: `{"state":"OPEN"}`}
	c := New(fe)

	got, err := c.IssueState(context.Background(), 7)
	if err != nil {
		t.Fatalf("IssueState returned error: %v", err)
	}

	if fe.name != "gh" {
		t.Errorf("ran %q, want %q", fe.name, "gh")
	}
	wantArgs := []string{"issue", "view", "7", "--json", "state"}
	if !slices.Equal(fe.args, wantArgs) {
		t.Errorf("args = %q, want %q", fe.args, wantArgs)
	}

	if got != "OPEN" {
		t.Errorf("state = %q, want %q", got, "OPEN")
	}
}

func TestListOpenIssuesByLabelBuildsArgvAndParses(t *testing.T) {
	t.Parallel()

	fe := &fakeExec{stdout: `[{"number":1,"title":"a","body":"x"},{"number":2,"title":"b","body":"y"}]`}
	c := New(fe)

	got, err := c.ListOpenIssuesByLabel(context.Background(), "ready-for-agent")
	if err != nil {
		t.Fatalf("ListOpenIssuesByLabel returned error: %v", err)
	}

	if fe.name != "gh" {
		t.Errorf("ran %q, want %q", fe.name, "gh")
	}
	wantArgs := []string{"issue", "list", "--label", "ready-for-agent", "--state", "open", "--limit", "100", "--json", "number,title,body"}
	if !slices.Equal(fe.args, wantArgs) {
		t.Errorf("args = %q, want %q", fe.args, wantArgs)
	}

	want := []Issue{{Number: 1, Title: "a", Body: "x"}, {Number: 2, Title: "b", Body: "y"}}
	if !slices.Equal(got, want) {
		t.Errorf("issues = %+v, want %+v", got, want)
	}
}

func TestPushBranchForcePushesToOrigin(t *testing.T) {
	t.Parallel()

	fe := &fakeExec{}
	c := New(fe)

	if err := c.PushBranch(context.Background(), "afk/issue-78"); err != nil {
		t.Fatalf("PushBranch returned error: %v", err)
	}

	if fe.name != "git" {
		t.Errorf("ran %q, want %q", fe.name, "git")
	}
	wantArgs := []string{"push", "--force", "origin", "afk/issue-78"}
	if !slices.Equal(fe.args, wantArgs) {
		t.Errorf("args = %q, want %q", fe.args, wantArgs)
	}
}

func TestCreateDraftPRBuildsArgvAndReturnsURL(t *testing.T) {
	t.Parallel()

	fe := &fakeExec{stdout: "https://github.com/o/r/pull/42\n"}
	c := New(fe)

	got, err := c.CreateDraftPR(context.Background(), "afk/issue-78", "AFK: issue 78", "line one\nline two")
	if err != nil {
		t.Fatalf("CreateDraftPR returned error: %v", err)
	}

	if fe.name != "gh" {
		t.Errorf("ran %q, want %q", fe.name, "gh")
	}
	wantArgs := []string{
		"pr", "create", "--draft",
		"--base", "main",
		"--head", "afk/issue-78",
		"--title", "AFK: issue 78",
		"--body", "line one\nline two",
	}
	if !slices.Equal(fe.args, wantArgs) {
		t.Errorf("args = %q, want %q", fe.args, wantArgs)
	}

	want := "https://github.com/o/r/pull/42"
	if got != want {
		t.Errorf("url = %q, want %q", got, want)
	}
}

func TestPRDiffBuildsArgvAndReturnsDiff(t *testing.T) {
	t.Parallel()

	diff := "diff --git a/x b/x\n@@ -1 +1 @@\n-a\n+b\n"
	fe := &fakeExec{stdout: diff}
	c := New(fe)

	got, err := c.PRDiff(context.Background(), 42)
	if err != nil {
		t.Fatalf("PRDiff returned error: %v", err)
	}

	if fe.name != "gh" {
		t.Errorf("ran %q, want %q", fe.name, "gh")
	}
	wantArgs := []string{"pr", "diff", "42"}
	if !slices.Equal(fe.args, wantArgs) {
		t.Errorf("args = %q, want %q", fe.args, wantArgs)
	}
	if got != diff {
		t.Errorf("diff = %q, want %q", got, diff)
	}
}

func TestPostReviewBuildsArgvAndJSONPayload(t *testing.T) {
	t.Parallel()

	fe := &fakeExec{}
	c := New(fe)

	comments := []ReviewComment{
		{Path: "foo.go", Line: 12, Body: "nit: rename this"},
		{Path: "bar.go", Line: 3, Body: "off by one"},
	}
	if err := c.PostReview(context.Background(), 42, "looks good overall", comments); err != nil {
		t.Fatalf("PostReview returned error: %v", err)
	}

	if fe.name != "gh" {
		t.Errorf("ran %q, want %q", fe.name, "gh")
	}
	wantArgs := []string{
		"api", "--method", "POST",
		"repos/{owner}/{repo}/pulls/42/reviews",
		"--input", "-",
	}
	if !slices.Equal(fe.args, wantArgs) {
		t.Errorf("args = %q, want %q", fe.args, wantArgs)
	}

	// The body is JSON on stdin: one COMMENT review carrying the summary as the
	// top-level body and each comment anchored to the diff's RIGHT side.
	var got struct {
		Event    string `json:"event"`
		Body     string `json:"body"`
		Comments []struct {
			Path string `json:"path"`
			Line int    `json:"line"`
			Side string `json:"side"`
			Body string `json:"body"`
		} `json:"comments"`
	}
	if err := json.Unmarshal([]byte(fe.stdin), &got); err != nil {
		t.Fatalf("stdin is not valid JSON: %v\n%s", err, fe.stdin)
	}
	if got.Event != "COMMENT" {
		t.Errorf("event = %q, want %q", got.Event, "COMMENT")
	}
	if got.Body != "looks good overall" {
		t.Errorf("body = %q, want the summary", got.Body)
	}
	if len(got.Comments) != 2 {
		t.Fatalf("comments = %d, want 2", len(got.Comments))
	}
	if c0 := got.Comments[0]; c0.Path != "foo.go" || c0.Line != 12 || c0.Side != "RIGHT" || c0.Body != "nit: rename this" {
		t.Errorf("comment[0] = %+v, want foo.go:12 RIGHT", c0)
	}
}

func TestPostReviewEmptyCommentsEmitsJSONArrayNotNull(t *testing.T) {
	t.Parallel()

	fe := &fakeExec{}
	c := New(fe)

	if err := c.PostReview(context.Background(), 1, "bodied review, no inline comments", nil); err != nil {
		t.Fatalf("PostReview returned error: %v", err)
	}

	// GitHub rejects "comments": null; an empty review must serialize as [].
	if !strings.Contains(fe.stdin, `"comments":[]`) {
		t.Errorf("stdin = %s, want an empty comments array, not null", fe.stdin)
	}
}

func TestAddLabelBuildsArgv(t *testing.T) {
	t.Parallel()

	fe := &fakeExec{}
	c := New(fe)

	if err := c.AddLabel(context.Background(), "https://github.com/o/r/pull/42", "afk"); err != nil {
		t.Fatalf("AddLabel returned error: %v", err)
	}

	if fe.name != "gh" {
		t.Errorf("ran %q, want %q", fe.name, "gh")
	}
	wantArgs := []string{"pr", "edit", "https://github.com/o/r/pull/42", "--add-label", "afk"}
	if !slices.Equal(fe.args, wantArgs) {
		t.Errorf("args = %q, want %q", fe.args, wantArgs)
	}
}

func TestAddIssueLabelBuildsArgv(t *testing.T) {
	t.Parallel()

	fe := &fakeExec{}
	c := New(fe)

	if err := c.AddIssueLabel(context.Background(), 78, "in-progress"); err != nil {
		t.Fatalf("AddIssueLabel returned error: %v", err)
	}

	if fe.name != "gh" {
		t.Errorf("ran %q, want %q", fe.name, "gh")
	}
	wantArgs := []string{"issue", "edit", "78", "--add-label", "in-progress"}
	if !slices.Equal(fe.args, wantArgs) {
		t.Errorf("args = %q, want %q", fe.args, wantArgs)
	}
}

func TestAddIssueLabelPropagatesError(t *testing.T) {
	t.Parallel()

	fe := &fakeExec{err: errBoom}
	c := New(fe)

	if err := c.AddIssueLabel(context.Background(), 78, "in-progress"); !errors.Is(err, errBoom) {
		t.Errorf("err = %v, want %v", err, errBoom)
	}
}

func TestRemoveIssueLabelBuildsArgv(t *testing.T) {
	t.Parallel()

	fe := &fakeExec{}
	c := New(fe)

	if err := c.RemoveIssueLabel(context.Background(), 78, "ready-for-agent"); err != nil {
		t.Fatalf("RemoveIssueLabel returned error: %v", err)
	}

	if fe.name != "gh" {
		t.Errorf("ran %q, want %q", fe.name, "gh")
	}
	wantArgs := []string{"issue", "edit", "78", "--remove-label", "ready-for-agent"}
	if !slices.Equal(fe.args, wantArgs) {
		t.Errorf("args = %q, want %q", fe.args, wantArgs)
	}
}

func TestRemoveIssueLabelPropagatesError(t *testing.T) {
	t.Parallel()

	fe := &fakeExec{err: errBoom}
	c := New(fe)

	if err := c.RemoveIssueLabel(context.Background(), 78, "ready-for-agent"); !errors.Is(err, errBoom) {
		t.Errorf("err = %v, want %v", err, errBoom)
	}
}

func TestCommentIssueBuildsArgv(t *testing.T) {
	t.Parallel()

	fe := &fakeExec{}
	c := New(fe)

	if err := c.CommentIssue(context.Background(), 78, "agent picked up this issue"); err != nil {
		t.Fatalf("CommentIssue returned error: %v", err)
	}

	if fe.name != "gh" {
		t.Errorf("ran %q, want %q", fe.name, "gh")
	}
	wantArgs := []string{"issue", "comment", "78", "--body", "agent picked up this issue"}
	if !slices.Equal(fe.args, wantArgs) {
		t.Errorf("args = %q, want %q", fe.args, wantArgs)
	}
}

func TestCommentIssuePropagatesError(t *testing.T) {
	t.Parallel()

	fe := &fakeExec{err: errBoom}
	c := New(fe)

	if err := c.CommentIssue(context.Background(), 78, "agent picked up this issue"); !errors.Is(err, errBoom) {
		t.Errorf("err = %v, want %v", err, errBoom)
	}
}

func TestCurrentBranchBuildsArgvAndTrims(t *testing.T) {
	t.Parallel()

	fe := &fakeExec{stdout: "issue-82-afk-write-pr\n"}
	c := New(fe)

	got, err := c.CurrentBranch(context.Background())
	if err != nil {
		t.Fatalf("CurrentBranch returned error: %v", err)
	}

	if fe.name != "git" {
		t.Errorf("ran %q, want %q", fe.name, "git")
	}
	wantArgs := []string{"rev-parse", "--abbrev-ref", "HEAD"}
	if !slices.Equal(fe.args, wantArgs) {
		t.Errorf("args = %q, want %q", fe.args, wantArgs)
	}

	want := "issue-82-afk-write-pr"
	if got != want {
		t.Errorf("branch = %q, want %q", got, want)
	}
}

func TestBranchDiffBuildsArgvAndReturnsDiff(t *testing.T) {
	t.Parallel()

	diff := "diff --git a/x b/x\n@@ -1 +1 @@\n-a\n+b\n"
	fe := &fakeExec{stdout: diff}
	c := New(fe)

	got, err := c.BranchDiff(context.Background(), "issue-82-afk-write-pr")
	if err != nil {
		t.Fatalf("BranchDiff returned error: %v", err)
	}

	if fe.name != "git" {
		t.Errorf("ran %q, want %q", fe.name, "git")
	}
	wantArgs := []string{"diff", "main...issue-82-afk-write-pr"}
	if !slices.Equal(fe.args, wantArgs) {
		t.Errorf("args = %q, want %q", fe.args, wantArgs)
	}
	if got != diff {
		t.Errorf("diff = %q, want %q", got, diff)
	}
}

func TestPRForBranchReturnsExistingPR(t *testing.T) {
	t.Parallel()

	fe := &fakeExec{stdout: `[{"number":7,"url":"https://github.com/o/r/pull/7"}]`}
	c := New(fe)

	got, found, err := c.PRForBranch(context.Background(), "issue-82-afk-write-pr")
	if err != nil {
		t.Fatalf("PRForBranch returned error: %v", err)
	}

	if fe.name != "gh" {
		t.Errorf("ran %q, want %q", fe.name, "gh")
	}
	wantArgs := []string{"pr", "list", "--head", "issue-82-afk-write-pr", "--state", "open", "--limit", "1", "--json", "number,url"}
	if !slices.Equal(fe.args, wantArgs) {
		t.Errorf("args = %q, want %q", fe.args, wantArgs)
	}

	if !found {
		t.Errorf("found = false, want true")
	}
	want := PR{Number: 7, URL: "https://github.com/o/r/pull/7"}
	if got != want {
		t.Errorf("pr = %+v, want %+v", got, want)
	}
}

func TestPRForBranchReturnsFalseWhenNone(t *testing.T) {
	t.Parallel()

	fe := &fakeExec{stdout: "[]"}
	c := New(fe)

	got, found, err := c.PRForBranch(context.Background(), "issue-82-afk-write-pr")
	if err != nil {
		t.Fatalf("PRForBranch returned error: %v", err)
	}

	if found {
		t.Errorf("found = true, want false")
	}
	if got != (PR{}) {
		t.Errorf("pr = %+v, want zero value", got)
	}
}

func TestCreatePRBuildsArgvAndReturnsURL(t *testing.T) {
	t.Parallel()

	fe := &fakeExec{stdout: "https://github.com/o/r/pull/9\n"}
	c := New(fe)

	got, err := c.CreatePR(context.Background(), "issue-82-afk-write-pr", "Add write-pr", "body line")
	if err != nil {
		t.Fatalf("CreatePR returned error: %v", err)
	}

	if fe.name != "gh" {
		t.Errorf("ran %q, want %q", fe.name, "gh")
	}
	wantArgs := []string{
		"pr", "create",
		"--base", "main",
		"--head", "issue-82-afk-write-pr",
		"--title", "Add write-pr",
		"--body", "body line",
	}
	if !slices.Equal(fe.args, wantArgs) {
		t.Errorf("args = %q, want %q", fe.args, wantArgs)
	}

	want := "https://github.com/o/r/pull/9"
	if got != want {
		t.Errorf("url = %q, want %q", got, want)
	}
}

func TestEditPRBuildsArgv(t *testing.T) {
	t.Parallel()

	fe := &fakeExec{}
	c := New(fe)

	if err := c.EditPR(context.Background(), "https://github.com/o/r/pull/7", "New title", "New body"); err != nil {
		t.Fatalf("EditPR returned error: %v", err)
	}

	if fe.name != "gh" {
		t.Errorf("ran %q, want %q", fe.name, "gh")
	}
	wantArgs := []string{"pr", "edit", "https://github.com/o/r/pull/7", "--title", "New title", "--body", "New body"}
	if !slices.Equal(fe.args, wantArgs) {
		t.Errorf("args = %q, want %q", fe.args, wantArgs)
	}
}
