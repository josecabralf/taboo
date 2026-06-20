package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"afk/internal/ghio"

	taboo "github.com/josecabralf/taboo/pkg"
)

// fakeGH records the order and arguments of the gh/git calls implement makes and
// returns canned values (or injected errors) without shelling out. A non-nil
// *Err field forces the matching method to fail so the short-circuit and
// error-wrapping behavior can be asserted.
type fakeGH struct {
	calls []string

	issue      ghio.Issue
	issueErr   error
	pushErr    error
	prURL      string
	prErr      error
	labelErr   error
	prBranch   string // CreateDraftPR's branch arg
	prTitle    string // CreateDraftPR's title arg
	prBody     string // CreateDraftPR's body arg
	pushBranch string // PushBranch's branch arg
	labelRef   string // AddLabel's prRef arg
	labelName  string // AddLabel's label arg
}

func (f *fakeGH) IssueView(_ context.Context, _ int) (ghio.Issue, error) {
	f.calls = append(f.calls, "IssueView")
	return f.issue, f.issueErr
}

func (f *fakeGH) PushBranch(_ context.Context, branch string) error {
	f.calls = append(f.calls, "PushBranch")
	f.pushBranch = branch
	return f.pushErr
}

func (f *fakeGH) CreateDraftPR(_ context.Context, branch, title, body string) (string, error) {
	f.calls = append(f.calls, "CreateDraftPR")
	f.prBranch, f.prTitle, f.prBody = branch, title, body
	return f.prURL, f.prErr
}

func (f *fakeGH) AddLabel(_ context.Context, prRef, label string) error {
	f.calls = append(f.calls, "AddLabel")
	f.labelRef, f.labelName = prRef, label
	return f.labelErr
}

// fakeRunner records that the taboo run was invoked and returns a canned
// OrchestratedResult (or an error). The worktree arg, when set, becomes the
// result's WorktreePath so the test can stage a plan file there.
func fakeRunner(calls *[]string, worktree string, err error) workflowRunner {
	return func(_ context.Context, _, _ string, _ map[string]string, _ taboo.PlanOverrides, _ taboo.Commander) (taboo.OrchestratedResult, error) {
		*calls = append(*calls, "runTabo")
		return taboo.OrchestratedResult{RunResult: taboo.RunResult{WorktreePath: worktree}}, err
	}
}

func TestImplementHappyPathSequenceAndArgs(t *testing.T) {
	t.Parallel()

	worktree := t.TempDir()
	plan := "## Plan\n\n- do the thing\n"
	if err := os.WriteFile(filepath.Join(worktree, planFile), []byte(plan), 0o600); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	gh := &fakeGH{
		issue: ghio.Issue{Number: 42, Title: "Add the Foo!", Body: "the body"},
		prURL: "https://github.com/o/r/pull/99",
	}
	var runCalls []string
	run := fakeRunner(&runCalls, worktree, nil)

	if err := implement(context.Background(), t.TempDir(), 42, gh, run); err != nil {
		t.Fatalf("implement returned error: %v", err)
	}

	// IssueView -> runTabo -> PushBranch -> CreateDraftPR -> AddLabel, interleaving
	// the recorded gh calls with the single runTabo call between IssueView and Push.
	wantGH := []string{"IssueView", "PushBranch", "CreateDraftPR", "AddLabel"}
	if strings.Join(gh.calls, ",") != strings.Join(wantGH, ",") {
		t.Errorf("gh call order = %v, want %v", gh.calls, wantGH)
	}
	if len(runCalls) != 1 {
		t.Errorf("runTabo called %d times, want 1", len(runCalls))
	}

	branch := slugBranch(42, "Add the Foo!")
	if gh.pushBranch != branch {
		t.Errorf("push branch = %q, want %q", gh.pushBranch, branch)
	}
	if gh.prBranch != branch {
		t.Errorf("PR branch = %q, want %q", gh.prBranch, branch)
	}
	if want := prTitle("Add the Foo!"); gh.prTitle != want {
		t.Errorf("PR title = %q, want %q", gh.prTitle, want)
	}
	if !strings.Contains(gh.prBody, "Closes #42") {
		t.Errorf("PR body = %q, want it to contain %q", gh.prBody, "Closes #42")
	}
	if !strings.Contains(gh.prBody, plan) {
		t.Errorf("PR body = %q, want it to contain the plan %q", gh.prBody, plan)
	}
	if gh.labelName != reviewLabel {
		t.Errorf("label = %q, want %q", gh.labelName, reviewLabel)
	}
	if gh.labelRef != gh.prURL {
		t.Errorf("label ref = %q, want the PR URL %q", gh.labelRef, gh.prURL)
	}
}

func TestImplementNoPlanFileUsesFallbackBody(t *testing.T) {
	t.Parallel()

	// An empty worktree (no plan file) must drive the fallback PR body.
	gh := &fakeGH{
		issue: ghio.Issue{Number: 7, Title: "Fix bug", Body: "b"},
		prURL: "https://github.com/o/r/pull/1",
	}
	var runCalls []string
	run := fakeRunner(&runCalls, t.TempDir(), nil)

	if err := implement(context.Background(), t.TempDir(), 7, gh, run); err != nil {
		t.Fatalf("implement returned error: %v", err)
	}

	if !strings.Contains(gh.prBody, "No plan file was produced") {
		t.Errorf("PR body = %q, want the no-plan fallback", gh.prBody)
	}
}

func TestImplementPushErrorWrapsAndShortCircuits(t *testing.T) {
	t.Parallel()

	gh := &fakeGH{
		issue:   ghio.Issue{Number: 1, Title: "T", Body: "b"},
		pushErr: errors.New("boom"),
	}
	var runCalls []string
	run := fakeRunner(&runCalls, t.TempDir(), nil)

	err := implement(context.Background(), t.TempDir(), 1, gh, run)
	if err == nil {
		t.Fatal("implement returned nil, want a push-branch error")
	}
	if !strings.HasPrefix(err.Error(), "push branch: ") {
		t.Errorf("error = %q, want it to start with %q", err.Error(), "push branch: ")
	}
	// CreateDraftPR and AddLabel must not be reached after a push failure.
	for _, c := range gh.calls {
		if c == "CreateDraftPR" || c == "AddLabel" {
			t.Errorf("call %q reached after push failure; calls = %v", c, gh.calls)
		}
	}
}

func TestImplementCreatePRErrorWrapsAndSkipsLabel(t *testing.T) {
	t.Parallel()

	gh := &fakeGH{
		issue: ghio.Issue{Number: 1, Title: "T", Body: "b"},
		prErr: errors.New("boom"),
	}
	var runCalls []string
	run := fakeRunner(&runCalls, t.TempDir(), nil)

	err := implement(context.Background(), t.TempDir(), 1, gh, run)
	if err == nil {
		t.Fatal("implement returned nil, want an open-draft-PR error")
	}
	if !strings.HasPrefix(err.Error(), "open draft PR: ") {
		t.Errorf("error = %q, want it to start with %q", err.Error(), "open draft PR: ")
	}
	for _, c := range gh.calls {
		if c == "AddLabel" {
			t.Errorf("AddLabel reached after CreateDraftPR failure; calls = %v", gh.calls)
		}
	}
}

// Ensure *ghio.Client still satisfies ghClient and taboo.RunWorkflow satisfies
// workflowRunner, so the production wiring in runImplement stays type-correct.
var (
	_ ghClient       = (*ghio.Client)(nil)
	_ workflowRunner = taboo.RunWorkflow
)
