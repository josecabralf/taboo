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

// fakeUpdateBranchGH records the order and arguments of the gh/git calls
// updateBranch makes and returns canned values (or injected errors) without
// shelling out.
type fakeUpdateBranchGH struct {
	calls []string

	headBranch    string
	headBranchErr error

	fetchErr error

	upToDate    bool
	upToDateErr error

	pushBranch string
	pushErr    error

	labelRef string
	label    string
	labelErr error

	commentNum  int
	commentBody string
	commentErr  error
}

func (f *fakeUpdateBranchGH) PRHeadBranch(_ context.Context, _ int) (string, error) {
	f.calls = append(f.calls, "PRHeadBranch")
	return f.headBranch, f.headBranchErr
}

func (f *fakeUpdateBranchGH) Fetch(_ context.Context) error {
	f.calls = append(f.calls, "Fetch")
	return f.fetchErr
}

func (f *fakeUpdateBranchGH) UpToDateWithMain(_ context.Context, _ string) (bool, error) {
	f.calls = append(f.calls, "UpToDateWithMain")
	return f.upToDate, f.upToDateErr
}

func (f *fakeUpdateBranchGH) Push(_ context.Context, branch string) error {
	f.calls = append(f.calls, "Push")
	f.pushBranch = branch
	return f.pushErr
}

func (f *fakeUpdateBranchGH) AddLabel(_ context.Context, prRef, label string) error {
	f.calls = append(f.calls, "AddLabel")
	f.labelRef, f.label = prRef, label
	return f.labelErr
}

func (f *fakeUpdateBranchGH) CommentPR(_ context.Context, number int, body string) error {
	f.calls = append(f.calls, "CommentPR")
	f.commentNum, f.commentBody = number, body
	return f.commentErr
}

// fakeUpdateBranchRunner returns an updateBranchRunner that captures the vars and
// overrides and hands back a canned, already-decoded updateBranchResult (or an
// error). The bridge threads the JSONResult extractor into the run loop, so the
// fake returns a typed result directly — no hand-encoded <result> string. A nil
// capturedVars after the call proves the agent never ran (a gate short-circuited).
func fakeUpdateBranchRunner(capturedVars *map[string]string, capturedOv *taboo.PlanOverrides, res updateBranchResult, err error) updateBranchRunner {
	return func(_ context.Context, _, _ string, vars map[string]string, ov taboo.PlanOverrides, _ taboo.Commander) (updateBranchResult, taboo.OrchestratedResult, error) {
		*capturedVars = vars
		*capturedOv = ov
		return res, taboo.OrchestratedResult{}, err
	}
}

func TestUpdateBranchMergesAndPushes(t *testing.T) {
	t.Parallel()

	gh := &fakeUpdateBranchGH{headBranch: "agent/update-pr-12"} // upToDate defaults to false
	var captured map[string]string
	var capturedOv taboo.PlanOverrides
	run := fakeUpdateBranchRunner(&captured, &capturedOv, updateBranchResult{Updated: true, Validated: true}, nil)

	var buf bytes.Buffer
	if err := updateBranch(context.Background(), t.TempDir(), 12, &buf, gh, run); err != nil {
		t.Fatalf("updateBranch returned error: %v", err)
	}

	// Resolve the branch, fetch, gate on up-to-date, run the agent, then push.
	wantCalls := []string{"PRHeadBranch", "Fetch", "UpToDateWithMain", "Push"}
	if strings.Join(gh.calls, ",") != strings.Join(wantCalls, ",") {
		t.Errorf("gh call order = %v, want %v", gh.calls, wantCalls)
	}
	if captured["PR_NUMBER"] != "12" || captured["BRANCH"] != "agent/update-pr-12" {
		t.Errorf("vars = %v, want PR_NUMBER=12 and BRANCH=agent/update-pr-12", captured)
	}
	// The run starts its worktree on the PR branch, fetched from origin.
	if capturedOv.Branch != "agent/update-pr-12" || capturedOv.BaseRef != "origin/agent/update-pr-12" {
		t.Errorf("overrides = {Branch:%q BaseRef:%q}, want the PR branch + its origin ref", capturedOv.Branch, capturedOv.BaseRef)
	}
	if gh.pushBranch != "agent/update-pr-12" {
		t.Errorf("pushed branch = %q, want %q", gh.pushBranch, "agent/update-pr-12")
	}
	if !strings.Contains(buf.String(), "agent/update-pr-12") {
		t.Errorf("stdout = %q, want it to name the updated branch", buf.String())
	}
}

func TestUpdateBranchNoOpWhenUpToDate(t *testing.T) {
	t.Parallel()

	// origin/main is already contained in the branch: merging would be a no-op, so
	// the agent must not run and nothing must be pushed.
	gh := &fakeUpdateBranchGH{headBranch: "agent/update-pr-12", upToDate: true}
	var captured map[string]string
	var capturedOv taboo.PlanOverrides
	run := fakeUpdateBranchRunner(&captured, &capturedOv, updateBranchResult{Updated: true, Validated: true}, nil)

	var buf bytes.Buffer
	if err := updateBranch(context.Background(), t.TempDir(), 12, &buf, gh, run); err != nil {
		t.Fatalf("updateBranch returned error: %v", err)
	}

	// The gate short-circuits after the up-to-date check: no agent, no push.
	wantCalls := []string{"PRHeadBranch", "Fetch", "UpToDateWithMain"}
	if strings.Join(gh.calls, ",") != strings.Join(wantCalls, ",") {
		t.Errorf("gh call order = %v, want %v (no push)", gh.calls, wantCalls)
	}
	if captured != nil {
		t.Errorf("agent ran (vars=%v) on an up-to-date branch, want it skipped", captured)
	}
	if !strings.Contains(buf.String(), "up to date") {
		t.Errorf("stdout = %q, want it to say the branch is already up to date", buf.String())
	}
}

func TestUpdateBranchBlocksOnFailedValidation(t *testing.T) {
	t.Parallel()

	// The merge happened but in-workshop validation failed: the branch must be
	// marked agent:blocked with a diagnostic comment, and must NOT be pushed.
	gh := &fakeUpdateBranchGH{headBranch: "agent/update-pr-12"}
	var captured map[string]string
	var capturedOv taboo.PlanOverrides
	run := fakeUpdateBranchRunner(&captured, &capturedOv, updateBranchResult{
		Updated:   true,
		Validated: false,
		Summary:   "merged origin/main but `make test` failed in pkg",
	}, nil)

	var buf bytes.Buffer
	if err := updateBranch(context.Background(), t.TempDir(), 12, &buf, gh, run); err != nil {
		t.Fatalf("updateBranch returned error: %v", err)
	}

	// Label + comment, but no push.
	wantCalls := []string{"PRHeadBranch", "Fetch", "UpToDateWithMain", "AddLabel", "CommentPR"}
	if strings.Join(gh.calls, ",") != strings.Join(wantCalls, ",") {
		t.Errorf("gh call order = %v, want %v (blocked, not pushed)", gh.calls, wantCalls)
	}
	if gh.label != blockedLabel {
		t.Errorf("label = %q, want %q", gh.label, blockedLabel)
	}
	if gh.labelRef != "12" {
		t.Errorf("labelRef = %q, want the PR number %q", gh.labelRef, "12")
	}
	if gh.commentNum != 12 {
		t.Errorf("commented on PR %d, want 12", gh.commentNum)
	}
	if !strings.Contains(gh.commentBody, "make test") {
		t.Errorf("comment = %q, want it to carry the agent's failure summary", gh.commentBody)
	}
	if gh.pushBranch != "" {
		t.Errorf("pushed branch %q on failed validation, want no push", gh.pushBranch)
	}
}

func TestUpdateBranchBlockedReturnsNilWhenLabelFails(t *testing.T) {
	t.Parallel()

	// On the blocked path the label and comment are best-effort: if the label fails
	// but the comment lands, the blocked state still left a durable trace, so
	// updateBranch reports the handled outcome (nil) and does not push.
	gh := &fakeUpdateBranchGH{headBranch: "agent/update-pr-12", labelErr: errors.New("label boom")}
	var captured map[string]string
	var capturedOv taboo.PlanOverrides
	run := fakeUpdateBranchRunner(&captured, &capturedOv, updateBranchResult{
		Updated: true, Validated: false, Summary: "validation failed",
	}, nil)

	var buf bytes.Buffer
	if err := updateBranch(context.Background(), t.TempDir(), 12, &buf, gh, run); err != nil {
		t.Fatalf("updateBranch returned error, want nil (the comment landed): %v", err)
	}
	if gh.commentNum != 12 {
		t.Errorf("comment was not attempted (commentNum=%d), want it tried on PR 12", gh.commentNum)
	}
	if gh.pushBranch != "" {
		t.Errorf("pushed branch %q on failed validation, want no push", gh.pushBranch)
	}
}

func TestUpdateBranchBlockedReturnsNilWhenCommentFails(t *testing.T) {
	t.Parallel()

	// Symmetric to the label-failure case: if the comment fails but the label lands,
	// the blocked state is still visible, so the outcome is handled (nil), no push.
	gh := &fakeUpdateBranchGH{headBranch: "agent/update-pr-12", commentErr: errors.New("comment boom")}
	var captured map[string]string
	var capturedOv taboo.PlanOverrides
	run := fakeUpdateBranchRunner(&captured, &capturedOv, updateBranchResult{
		Updated: true, Validated: false, Summary: "validation failed",
	}, nil)

	var buf bytes.Buffer
	if err := updateBranch(context.Background(), t.TempDir(), 12, &buf, gh, run); err != nil {
		t.Fatalf("updateBranch returned error, want nil (the label landed): %v", err)
	}
	if gh.label != blockedLabel {
		t.Errorf("label = %q, want %q applied even when the comment fails", gh.label, blockedLabel)
	}
	if gh.pushBranch != "" {
		t.Errorf("pushed branch %q on failed validation, want no push", gh.pushBranch)
	}
}

func TestUpdateBranchBlockedErrorsWhenBothAnnotationsFail(t *testing.T) {
	t.Parallel()

	// If BOTH the label and the comment fail, the blocked state left no durable
	// trace, so updateBranch surfaces an error rather than reporting a handled
	// outcome — and it still must not push the unvalidated merge.
	gh := &fakeUpdateBranchGH{
		headBranch: "agent/update-pr-12",
		labelErr:   errors.New("label boom"),
		commentErr: errors.New("comment boom"),
	}
	var captured map[string]string
	var capturedOv taboo.PlanOverrides
	run := fakeUpdateBranchRunner(&captured, &capturedOv, updateBranchResult{
		Updated: true, Validated: false, Summary: "validation failed",
	}, nil)

	var buf bytes.Buffer
	err := updateBranch(context.Background(), t.TempDir(), 12, &buf, gh, run)
	if err == nil {
		t.Fatal("updateBranch returned nil, want an error when both label and comment fail")
	}
	if gh.pushBranch != "" {
		t.Errorf("pushed branch %q despite failed validation, want no push", gh.pushBranch)
	}
}

func TestUpdateBranchNoPushWhenNothingMerged(t *testing.T) {
	t.Parallel()

	// Race: the up-to-date gate said the branch was behind, but by the time the
	// agent merged there was nothing to merge (main landed in the branch
	// meanwhile). The agent reports updated=false; with no new commit there is
	// nothing to push or block on — report and exit cleanly.
	gh := &fakeUpdateBranchGH{headBranch: "agent/update-pr-12"}
	var captured map[string]string
	var capturedOv taboo.PlanOverrides
	run := fakeUpdateBranchRunner(&captured, &capturedOv, updateBranchResult{Updated: false, Validated: true}, nil)

	var buf bytes.Buffer
	if err := updateBranch(context.Background(), t.TempDir(), 12, &buf, gh, run); err != nil {
		t.Fatalf("updateBranch returned error: %v", err)
	}

	// The agent ran, but nothing past it: no push, no label, no comment.
	wantCalls := []string{"PRHeadBranch", "Fetch", "UpToDateWithMain"}
	if strings.Join(gh.calls, ",") != strings.Join(wantCalls, ",") {
		t.Errorf("gh call order = %v, want %v (nothing merged ⇒ no push/label)", gh.calls, wantCalls)
	}
	if captured == nil {
		t.Error("agent did not run, want it to run before reporting nothing merged")
	}
	if gh.pushBranch != "" {
		t.Errorf("pushed branch %q when nothing was merged, want no push", gh.pushBranch)
	}
}

func TestUpdateBranchSurfacesHeadBranchError(t *testing.T) {
	t.Parallel()

	gh := &fakeUpdateBranchGH{headBranchErr: errors.New("gh view failed")}
	var captured map[string]string
	var capturedOv taboo.PlanOverrides
	run := fakeUpdateBranchRunner(&captured, &capturedOv, updateBranchResult{}, nil)

	err := updateBranch(context.Background(), t.TempDir(), 12, io.Discard, gh, run)
	if err == nil {
		t.Fatal("updateBranch returned nil, want the head-branch error surfaced")
	}
	if !strings.Contains(err.Error(), "head branch") {
		t.Errorf("error = %q, want it to wrap the head-branch resolution failure", err.Error())
	}
	if strings.Join(gh.calls, ",") != "PRHeadBranch" {
		t.Errorf("gh calls = %v, want only PRHeadBranch", gh.calls)
	}
	if captured != nil {
		t.Errorf("agent ran (vars=%v) after a head-branch failure, want it skipped", captured)
	}
}

func TestUpdateBranchSurfacesFetchError(t *testing.T) {
	t.Parallel()

	gh := &fakeUpdateBranchGH{headBranch: "agent/update-pr-12", fetchErr: errors.New("git fetch failed")}
	var captured map[string]string
	var capturedOv taboo.PlanOverrides
	run := fakeUpdateBranchRunner(&captured, &capturedOv, updateBranchResult{}, nil)

	err := updateBranch(context.Background(), t.TempDir(), 12, io.Discard, gh, run)
	if err == nil {
		t.Fatal("updateBranch returned nil, want the fetch error surfaced")
	}
	if !strings.Contains(err.Error(), "fetch origin") {
		t.Errorf("error = %q, want it to wrap the fetch failure", err.Error())
	}
	if strings.Join(gh.calls, ",") != "PRHeadBranch,Fetch" {
		t.Errorf("gh calls = %v, want PRHeadBranch,Fetch", gh.calls)
	}
	if captured != nil {
		t.Errorf("agent ran (vars=%v) after a fetch failure, want it skipped", captured)
	}
}

func TestUpdateBranchSurfacesUpToDateError(t *testing.T) {
	t.Parallel()

	gh := &fakeUpdateBranchGH{headBranch: "agent/update-pr-12", upToDateErr: errors.New("merge-base failed")}
	var captured map[string]string
	var capturedOv taboo.PlanOverrides
	run := fakeUpdateBranchRunner(&captured, &capturedOv, updateBranchResult{}, nil)

	err := updateBranch(context.Background(), t.TempDir(), 12, io.Discard, gh, run)
	if err == nil {
		t.Fatal("updateBranch returned nil, want the up-to-date check error surfaced")
	}
	if !strings.Contains(err.Error(), "up to date with main") {
		t.Errorf("error = %q, want it to wrap the up-to-date check failure", err.Error())
	}
	if captured != nil {
		t.Errorf("agent ran (vars=%v) after an up-to-date check failure, want it skipped", captured)
	}
}

func TestUpdateBranchSurfacesRunFailure(t *testing.T) {
	t.Parallel()

	// When the agent emits no usable <result> block the bridge returns ErrNoResult
	// after the run. update-branch must surface it (wrapped) and push nothing.
	gh := &fakeUpdateBranchGH{headBranch: "agent/update-pr-12"}
	var captured map[string]string
	var capturedOv taboo.PlanOverrides
	run := fakeUpdateBranchRunner(&captured, &capturedOv, updateBranchResult{}, taboo.ErrNoResult)

	err := updateBranch(context.Background(), t.TempDir(), 12, io.Discard, gh, run)
	if err == nil {
		t.Fatal("updateBranch returned nil, want a run error")
	}
	if !errors.Is(err, taboo.ErrNoResult) {
		t.Errorf("error = %v, want it to wrap taboo.ErrNoResult", err)
	}
	if !strings.HasPrefix(err.Error(), "run update-branch agent: ") {
		t.Errorf("error = %q, want it to start with %q", err.Error(), "run update-branch agent: ")
	}
	if gh.pushBranch != "" {
		t.Errorf("pushed branch %q after a run failure, want no push", gh.pushBranch)
	}
}

func TestUpdateBranchSurfacesPushError(t *testing.T) {
	t.Parallel()

	gh := &fakeUpdateBranchGH{headBranch: "agent/update-pr-12", pushErr: errors.New("push rejected")}
	var captured map[string]string
	var capturedOv taboo.PlanOverrides
	run := fakeUpdateBranchRunner(&captured, &capturedOv, updateBranchResult{Updated: true, Validated: true}, nil)

	err := updateBranch(context.Background(), t.TempDir(), 12, io.Discard, gh, run)
	if err == nil {
		t.Fatal("updateBranch returned nil, want the push error surfaced")
	}
	if !strings.Contains(err.Error(), "push updated branch") {
		t.Errorf("error = %q, want it to wrap the push failure", err.Error())
	}
}

// Ensure *ghio.Client satisfies updateBranchGH and the typed bridge satisfies
// updateBranchRunner, so the production wiring in runUpdateBranch stays type-correct.
var (
	_ updateBranchGH     = (*ghio.Client)(nil)
	_ updateBranchRunner = taboo.RunWorkflowAs[updateBranchResult]
)
