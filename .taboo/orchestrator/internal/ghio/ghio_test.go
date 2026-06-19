package ghio

import (
	"context"
	"slices"
	"testing"
)

// fakeExec records the (name, args) of each call and returns canned stdout, so
// tests can assert the exact argv built by the Client and feed it scripted
// output without shelling out.
type fakeExec struct {
	name   string
	args   []string
	stdout string
	err    error
}

func (f *fakeExec) Run(_ context.Context, name string, args ...string) (string, error) {
	f.name = name
	f.args = args
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
