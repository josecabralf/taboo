package ghio

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

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
