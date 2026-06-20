package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"afk/internal/ghio"

	taboo "github.com/josecabralf/taboo/pkg"
)

// fakeWritePRGH records the order and arguments of the gh/git calls writePR makes
// and returns canned values (or injected errors) without shelling out.
type fakeWritePRGH struct {
	calls []string

	currentBranch    string
	currentBranchErr error

	diff       string
	diffBranch string
	diffErr    error

	createBranch string
	createTitle  string
	createBody   string
	createURL    string
	createErr    error

	prForBranch    ghio.PR
	prFound        bool
	prForBranchErr error
	prForBranchArg string

	editRef   string
	editTitle string
	editBody  string
	editErr   error
}

func (f *fakeWritePRGH) CurrentBranch(_ context.Context) (string, error) {
	f.calls = append(f.calls, "CurrentBranch")
	return f.currentBranch, f.currentBranchErr
}

func (f *fakeWritePRGH) BranchDiff(_ context.Context, branch string) (string, error) {
	f.calls = append(f.calls, "BranchDiff")
	f.diffBranch = branch
	return f.diff, f.diffErr
}

func (f *fakeWritePRGH) CreatePR(_ context.Context, branch, title, body string) (string, error) {
	f.calls = append(f.calls, "CreatePR")
	f.createBranch, f.createTitle, f.createBody = branch, title, body
	return f.createURL, f.createErr
}

func (f *fakeWritePRGH) PRForBranch(_ context.Context, branch string) (ghio.PR, bool, error) {
	f.calls = append(f.calls, "PRForBranch")
	f.prForBranchArg = branch
	return f.prForBranch, f.prFound, f.prForBranchErr
}

func (f *fakeWritePRGH) EditPR(_ context.Context, prRef, title, body string) error {
	f.calls = append(f.calls, "EditPR")
	f.editRef, f.editTitle, f.editBody = prRef, title, body
	return f.editErr
}

// fakeWritePRRunner returns a writePRRunner that captures the vars and hands back
// a canned, already-decoded prContent (or an error). The bridge threads the
// JSONResult extractor into the run loop, so the fake returns a typed prContent
// directly — no hand-encoded <result> string.
func fakeWritePRRunner(capturedVars *map[string]string, content prContent, err error) writePRRunner {
	return func(_ context.Context, _, _ string, vars map[string]string, _ taboo.PlanOverrides, _ taboo.Commander) (prContent, taboo.OrchestratedResult, error) {
		*capturedVars = vars
		return content, taboo.OrchestratedResult{}, err
	}
}

func TestWritePRCreatesPRFromAgentContent(t *testing.T) {
	t.Parallel()

	gh := &fakeWritePRGH{diff: "diff --git a/x b/x\n", createURL: "https://github.com/o/r/pull/9"}
	var captured map[string]string
	run := fakeWritePRRunner(&captured, prContent{Title: "Add write-pr", Body: "Body text"}, nil)

	var buf bytes.Buffer
	if err := writePR(context.Background(), t.TempDir(), "issue-82-afk-write-pr", &buf, gh, run); err != nil {
		t.Fatalf("writePR returned error: %v", err)
	}

	wantCalls := []string{"BranchDiff", "PRForBranch", "CreatePR"}
	if strings.Join(gh.calls, ",") != strings.Join(wantCalls, ",") {
		t.Errorf("gh call order = %v, want %v", gh.calls, wantCalls)
	}
	if gh.diffBranch != "issue-82-afk-write-pr" {
		t.Errorf("diffed branch %q, want %q", gh.diffBranch, "issue-82-afk-write-pr")
	}
	if captured["BRANCH"] != "issue-82-afk-write-pr" || captured["DIFF"] != "diff --git a/x b/x\n" {
		t.Errorf("vars = %v, want BRANCH and DIFF injected", captured)
	}
	if gh.createBranch != "issue-82-afk-write-pr" || gh.createTitle != "Add write-pr" || gh.createBody != "Body text" {
		t.Errorf("CreatePR got (%q, %q, %q), want the branch + agent title/body", gh.createBranch, gh.createTitle, gh.createBody)
	}
	if got, want := buf.String(), "https://github.com/o/r/pull/9\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestWritePRUpdatesExistingOpenPR(t *testing.T) {
	t.Parallel()

	gh := &fakeWritePRGH{
		diff:        "diff --git a/x b/x\n",
		prForBranch: ghio.PR{Number: 7, URL: "https://github.com/o/r/pull/7"},
		prFound:     true,
	}
	var captured map[string]string
	run := fakeWritePRRunner(&captured, prContent{Title: "Updated title", Body: "Updated body"}, nil)

	var buf bytes.Buffer
	if err := writePR(context.Background(), t.TempDir(), "issue-82-afk-write-pr", &buf, gh, run); err != nil {
		t.Fatalf("writePR returned error: %v", err)
	}

	// A branch that already has an open PR must be updated in place, never duplicated.
	wantCalls := []string{"BranchDiff", "PRForBranch", "EditPR"}
	if strings.Join(gh.calls, ",") != strings.Join(wantCalls, ",") {
		t.Errorf("gh call order = %v, want %v (re-run must update, not create)", gh.calls, wantCalls)
	}
	if gh.prForBranchArg != "issue-82-afk-write-pr" {
		t.Errorf("looked up PR for branch %q, want %q", gh.prForBranchArg, "issue-82-afk-write-pr")
	}
	if gh.editRef != "https://github.com/o/r/pull/7" || gh.editTitle != "Updated title" || gh.editBody != "Updated body" {
		t.Errorf("EditPR got (%q, %q, %q), want the existing PR URL + agent title/body", gh.editRef, gh.editTitle, gh.editBody)
	}
	if got, want := buf.String(), "https://github.com/o/r/pull/7\n"; got != want {
		t.Errorf("stdout = %q, want the existing PR URL %q", got, want)
	}
}

func TestWritePRDefaultsToCurrentBranch(t *testing.T) {
	t.Parallel()

	gh := &fakeWritePRGH{
		currentBranch: "issue-82-afk-write-pr",
		diff:          "diff --git a/x b/x\n",
		createURL:     "https://github.com/o/r/pull/9",
	}
	var captured map[string]string
	run := fakeWritePRRunner(&captured, prContent{Title: "Add write-pr", Body: "Body text"}, nil)

	var buf bytes.Buffer
	// An empty branch argument means "use the branch currently checked out".
	if err := writePR(context.Background(), t.TempDir(), "", &buf, gh, run); err != nil {
		t.Fatalf("writePR returned error: %v", err)
	}

	wantCalls := []string{"CurrentBranch", "BranchDiff", "PRForBranch", "CreatePR"}
	if strings.Join(gh.calls, ",") != strings.Join(wantCalls, ",") {
		t.Errorf("gh call order = %v, want %v", gh.calls, wantCalls)
	}
	if gh.diffBranch != "issue-82-afk-write-pr" || gh.createBranch != "issue-82-afk-write-pr" {
		t.Errorf("resolved branch threaded as diff=%q create=%q, want the current branch", gh.diffBranch, gh.createBranch)
	}
	if captured["BRANCH"] != "issue-82-afk-write-pr" {
		t.Errorf("BRANCH var = %q, want the resolved current branch", captured["BRANCH"])
	}
}

func TestWritePRErrorsOnEmptyDiff(t *testing.T) {
	t.Parallel()

	gh := &fakeWritePRGH{diff: "   \n"} // whitespace only: no real changes
	var captured map[string]string
	run := fakeWritePRRunner(&captured, prContent{Title: "x"}, nil)

	err := writePR(context.Background(), t.TempDir(), "issue-82-afk-write-pr", io.Discard, gh, run)
	if err == nil {
		t.Fatal("writePR returned nil, want an error for an empty diff")
	}
	if !strings.Contains(err.Error(), "no changes") {
		t.Errorf("error = %q, want it to mention there are no changes", err.Error())
	}
	// The agent must not run and no PR must be touched when there is nothing to propose.
	if captured != nil {
		t.Errorf("agent ran (vars=%v) on an empty diff, want it skipped", captured)
	}
	if strings.Join(gh.calls, ",") != "BranchDiff" {
		t.Errorf("gh calls = %v, want only BranchDiff", gh.calls)
	}
}

func TestWritePRErrorsOnEmptyTitle(t *testing.T) {
	t.Parallel()

	gh := &fakeWritePRGH{diff: "diff --git a/x b/x\n"}
	var captured map[string]string
	run := fakeWritePRRunner(&captured, prContent{Title: "  ", Body: "has a body but no title"}, nil)

	err := writePR(context.Background(), t.TempDir(), "issue-82-afk-write-pr", io.Discard, gh, run)
	if err == nil {
		t.Fatal("writePR returned nil, want an error for an empty title")
	}
	if !strings.Contains(err.Error(), "empty title") {
		t.Errorf("error = %q, want it to mention an empty title", err.Error())
	}
	// No PR lookup/create/edit once the title is rejected.
	if strings.Join(gh.calls, ",") != "BranchDiff" {
		t.Errorf("gh calls = %v, want only BranchDiff (no PR touched)", gh.calls)
	}
}

func TestWritePRSurfacesRunFailure(t *testing.T) {
	t.Parallel()

	// When the agent emits no usable <result> block the bridge returns ErrNoResult
	// after the run. write-pr must surface it (wrapped) and touch no PR.
	gh := &fakeWritePRGH{diff: "diff --git a/x b/x\n"}
	var captured map[string]string
	run := fakeWritePRRunner(&captured, prContent{}, taboo.ErrNoResult)

	err := writePR(context.Background(), t.TempDir(), "issue-82-afk-write-pr", io.Discard, gh, run)
	if err == nil {
		t.Fatal("writePR returned nil, want a run error")
	}
	if !errors.Is(err, taboo.ErrNoResult) {
		t.Errorf("error = %v, want it to wrap taboo.ErrNoResult", err)
	}
	if !strings.HasPrefix(err.Error(), "run write-pr agent: ") {
		t.Errorf("error = %q, want it to start with %q", err.Error(), "run write-pr agent: ")
	}
	if strings.Join(gh.calls, ",") != "BranchDiff" {
		t.Errorf("gh calls = %v, want only BranchDiff after a run failure", gh.calls)
	}
}

func TestWritePRRejectsUnresolvableBranch(t *testing.T) {
	t.Parallel()

	// A detached HEAD makes `git rev-parse --abbrev-ref HEAD` report "HEAD",
	// which is not a real branch to open a PR for.
	gh := &fakeWritePRGH{currentBranch: "HEAD"}
	var captured map[string]string
	run := fakeWritePRRunner(&captured, prContent{Title: "x"}, nil)

	err := writePR(context.Background(), t.TempDir(), "", io.Discard, gh, run)
	if err == nil {
		t.Fatal("writePR returned nil, want an error for an unresolvable branch")
	}
	if !strings.Contains(err.Error(), "branch") {
		t.Errorf("error = %q, want it to mention the branch could not be resolved", err.Error())
	}
	// Nothing past branch resolution runs: no diff, no agent, no PR.
	if strings.Join(gh.calls, ",") != "CurrentBranch" {
		t.Errorf("gh calls = %v, want only CurrentBranch", gh.calls)
	}
	if captured != nil {
		t.Errorf("agent ran (vars=%v) on an unresolvable branch, want it skipped", captured)
	}
}

func TestWritePRSurfacesDiffError(t *testing.T) {
	t.Parallel()

	gh := &fakeWritePRGH{diffErr: errors.New("git exploded")}
	var captured map[string]string
	run := fakeWritePRRunner(&captured, prContent{Title: "x"}, nil)

	err := writePR(context.Background(), t.TempDir(), "issue-82-afk-write-pr", io.Discard, gh, run)
	if err == nil {
		t.Fatal("writePR returned nil, want the diff error surfaced")
	}
	if !strings.Contains(err.Error(), "diff branch against main") {
		t.Errorf("error = %q, want it to wrap the diff failure", err.Error())
	}
	if captured != nil {
		t.Errorf("agent ran (vars=%v) after a diff failure, want it skipped", captured)
	}
}

func TestWritePRSurfacesPRLookupError(t *testing.T) {
	t.Parallel()

	gh := &fakeWritePRGH{diff: "diff --git a/x b/x\n", prForBranchErr: errors.New("gh list failed")}
	var captured map[string]string
	run := fakeWritePRRunner(&captured, prContent{Title: "Add write-pr", Body: "Body"}, nil)

	err := writePR(context.Background(), t.TempDir(), "issue-82-afk-write-pr", io.Discard, gh, run)
	if err == nil {
		t.Fatal("writePR returned nil, want the PR-lookup error surfaced")
	}
	if !strings.Contains(err.Error(), "look up existing PR") {
		t.Errorf("error = %q, want it to wrap the PR-lookup failure", err.Error())
	}
	// The agent ran and the title passed, but no PR is created or edited.
	if strings.Join(gh.calls, ",") != "BranchDiff,PRForBranch" {
		t.Errorf("gh calls = %v, want BranchDiff,PRForBranch", gh.calls)
	}
}

func TestWritePRSurfacesCreateError(t *testing.T) {
	t.Parallel()

	gh := &fakeWritePRGH{diff: "diff --git a/x b/x\n", createErr: errors.New("create failed")}
	var captured map[string]string
	run := fakeWritePRRunner(&captured, prContent{Title: "Add write-pr", Body: "Body"}, nil)

	err := writePR(context.Background(), t.TempDir(), "issue-82-afk-write-pr", io.Discard, gh, run)
	if err == nil {
		t.Fatal("writePR returned nil, want the create error surfaced")
	}
	if !strings.Contains(err.Error(), "create PR") {
		t.Errorf("error = %q, want it to wrap the create failure", err.Error())
	}
}

func TestWritePRSurfacesEditError(t *testing.T) {
	t.Parallel()

	gh := &fakeWritePRGH{
		diff:        "diff --git a/x b/x\n",
		prForBranch: ghio.PR{Number: 7, URL: "https://github.com/o/r/pull/7"},
		prFound:     true,
		editErr:     errors.New("edit failed"),
	}
	var captured map[string]string
	run := fakeWritePRRunner(&captured, prContent{Title: "Updated", Body: "Body"}, nil)

	err := writePR(context.Background(), t.TempDir(), "issue-82-afk-write-pr", io.Discard, gh, run)
	if err == nil {
		t.Fatal("writePR returned nil, want the edit error surfaced")
	}
	if !strings.Contains(err.Error(), "update PR") {
		t.Errorf("error = %q, want it to wrap the edit failure", err.Error())
	}
}

// Ensure *ghio.Client satisfies writePRGH and the typed bridge satisfies
// writePRRunner, so the production wiring in runWritePR stays type-correct.
var (
	_ writePRGH     = (*ghio.Client)(nil)
	_ writePRRunner = taboo.RunWorkflowAs[prContent]
)
