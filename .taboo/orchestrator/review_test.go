package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"afk/internal/ghio"

	"github.com/josecabralf/taboo"
)

// fakeReviewGH records the order and arguments of the gh calls review makes and
// returns canned values (or injected errors) without shelling out.
type fakeReviewGH struct {
	calls []string

	diff    string
	diffErr error
	postErr error

	postNumber   int
	postSummary  string
	postComments []ghio.ReviewComment
}

func (f *fakeReviewGH) PRDiff(_ context.Context, _ int) (string, error) {
	f.calls = append(f.calls, "PRDiff")
	return f.diff, f.diffErr
}

func (f *fakeReviewGH) PostReview(_ context.Context, number int, summary string, comments []ghio.ReviewComment) error {
	f.calls = append(f.calls, "PostReview")
	f.postNumber, f.postSummary, f.postComments = number, summary, comments
	return f.postErr
}

// fakeReviewRunner returns a reviewRunner that records the call and hands back a
// canned, already-decoded reviewResult (or an error). Because the bridge threads
// the JSONResult extractor into the run loop, the fake returns a typed
// reviewResult directly — no hand-encoded <result> string and no re-stubbing of
// extraction (pkg's own tests cover that).
func fakeReviewRunner(calls *[]string, rr reviewResult, err error) reviewRunner {
	return func(_ context.Context, _, _ string, _ map[string]string, _ taboo.PlanOverrides, _ taboo.Commander) (reviewResult, taboo.OrchestratedResult, error) {
		*calls = append(*calls, "runReview")
		return rr, taboo.OrchestratedResult{}, err
	}
}

// A two-line diff for foo.go: line 10 (context) and line 11 (added) are
// addressable; line 99 is not.
const reviewDiff = `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -10,1 +10,2 @@
 context line
+added line
`

func TestReviewPostsSummaryAndInDiffComments(t *testing.T) {
	t.Parallel()

	rr := reviewResult{
		Summary:  "overall ok",
		Comments: []reviewComment{{Path: "foo.go", Line: 11, Body: "good"}},
	}
	gh := &fakeReviewGH{diff: reviewDiff}
	var runCalls []string
	run := fakeReviewRunner(&runCalls, rr, nil)

	if err := review(context.Background(), t.TempDir(), 42, gh, run); err != nil {
		t.Fatalf("review returned error: %v", err)
	}

	wantCalls := []string{"PRDiff", "PostReview"}
	if strings.Join(gh.calls, ",") != strings.Join(wantCalls, ",") {
		t.Errorf("gh call order = %v, want %v", gh.calls, wantCalls)
	}
	if len(runCalls) != 1 {
		t.Errorf("runReview called %d times, want 1", len(runCalls))
	}
	if gh.postNumber != 42 {
		t.Errorf("posted to PR %d, want 42", gh.postNumber)
	}
	if gh.postSummary != "overall ok" {
		t.Errorf("summary = %q, want %q", gh.postSummary, "overall ok")
	}
	if len(gh.postComments) != 1 || gh.postComments[0].Path != "foo.go" || gh.postComments[0].Line != 11 {
		t.Errorf("comments = %+v, want one foo.go:11 comment", gh.postComments)
	}
}

func TestReviewDropsOutOfDiffCommentsWithoutError(t *testing.T) {
	t.Parallel()

	// One in-diff comment (foo.go:11) and one phantom (foo.go:99). The phantom is
	// dropped silently; the run must still succeed and post exactly one review.
	rr := reviewResult{
		Summary: "s",
		Comments: []reviewComment{
			{Path: "foo.go", Line: 11, Body: "keep me"},
			{Path: "foo.go", Line: 99, Body: "phantom"},
		},
	}
	gh := &fakeReviewGH{diff: reviewDiff}
	var runCalls []string
	run := fakeReviewRunner(&runCalls, rr, nil)

	if err := review(context.Background(), t.TempDir(), 42, gh, run); err != nil {
		t.Fatalf("review returned error on a phantom comment: %v", err)
	}

	if got := countCalls(gh.calls, "PostReview"); got != 1 {
		t.Errorf("PostReview called %d times, want exactly 1", got)
	}
	if len(gh.postComments) != 1 || gh.postComments[0].Line != 11 {
		t.Errorf("comments = %+v, want only the in-diff foo.go:11", gh.postComments)
	}
}

func TestReviewSkipsPostWhenEmptyToAvoid422(t *testing.T) {
	t.Parallel()

	// Empty summary and the only comment is out of diff: nothing survives. The
	// post must be skipped (not an empty COMMENT review, which GitHub 422s).
	rr := reviewResult{
		Summary:  "",
		Comments: []reviewComment{{Path: "foo.go", Line: 99, Body: "phantom"}},
	}
	gh := &fakeReviewGH{diff: reviewDiff}
	var runCalls []string
	run := fakeReviewRunner(&runCalls, rr, nil)

	if err := review(context.Background(), t.TempDir(), 42, gh, run); err != nil {
		t.Fatalf("review returned error: %v", err)
	}

	if got := countCalls(gh.calls, "PostReview"); got != 0 {
		t.Errorf("PostReview called %d times, want 0 (clean review must skip)", got)
	}
}

func TestReviewPostsBodiedReviewWhenSummaryButNoComments(t *testing.T) {
	t.Parallel()

	// A summary with no comments still posts (a bodied COMMENT review is valid).
	rr := reviewResult{Summary: "LGTM, no inline notes"}
	gh := &fakeReviewGH{diff: reviewDiff}
	var runCalls []string
	run := fakeReviewRunner(&runCalls, rr, nil)

	if err := review(context.Background(), t.TempDir(), 42, gh, run); err != nil {
		t.Fatalf("review returned error: %v", err)
	}

	if got := countCalls(gh.calls, "PostReview"); got != 1 {
		t.Errorf("PostReview called %d times, want 1", got)
	}
	if gh.postSummary != "LGTM, no inline notes" || len(gh.postComments) != 0 {
		t.Errorf("posted summary=%q comments=%+v, want the summary with no comments", gh.postSummary, gh.postComments)
	}
}

func TestReviewSurfacesRunFailure(t *testing.T) {
	t.Parallel()

	// When the agent emits no usable <result> block the bridge decodes nothing and
	// returns ErrNoResult after the run. Unlike a phantom line, this is a real
	// error: review must surface it (wrapped) and post no review.
	gh := &fakeReviewGH{diff: reviewDiff}
	var runCalls []string
	run := fakeReviewRunner(&runCalls, reviewResult{}, taboo.ErrNoResult)

	err := review(context.Background(), t.TempDir(), 42, gh, run)
	if err == nil {
		t.Fatal("review returned nil, want a run error")
	}
	if !errors.Is(err, taboo.ErrNoResult) {
		t.Errorf("error = %v, want it to wrap taboo.ErrNoResult", err)
	}
	if !strings.HasPrefix(err.Error(), "run review agent: ") {
		t.Errorf("error = %q, want it to start with %q", err.Error(), "run review agent: ")
	}
	if got := countCalls(gh.calls, "PostReview"); got != 0 {
		t.Errorf("PostReview called %d times, want 0 after a run failure", got)
	}
}

func countCalls(calls []string, name string) int {
	n := 0
	for _, c := range calls {
		if c == name {
			n++
		}
	}
	return n
}

// Ensure *ghio.Client satisfies reviewGH and the typed bridge satisfies
// reviewRunner, so the production wiring in runReview stays type-correct.
var (
	_ reviewGH     = (*ghio.Client)(nil)
	_ reviewRunner = taboo.RunWorkflowAs[reviewResult]
)
