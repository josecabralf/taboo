package main

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"

	"afk/internal/ghio"

	taboo "github.com/josecabralf/taboo/pkg"
)

// fakeLoopGH implements loopGH, recording the label/comment ops the loop drives
// (added/removed as "number:label" strings, comments as "number:body") and
// serving canned issues. It records nothing beyond what the assertions need, so a
// test can prove the claim/release label transitions happened in order without
// shelling out to gh.
type fakeLoopGH struct {
	issues  map[int]ghio.Issue
	viewErr error // when set, IssueView fails — drives the resolve setup-error path

	viewed   []int
	added    []string
	removed  []string
	comments []string
}

func (f *fakeLoopGH) IssueView(_ context.Context, number int) (ghio.Issue, error) {
	f.viewed = append(f.viewed, number)
	if f.viewErr != nil {
		return ghio.Issue{}, f.viewErr
	}
	return f.issues[number], nil
}

func (f *fakeLoopGH) AddIssueLabel(_ context.Context, number int, label string) error {
	f.added = append(f.added, strconv.Itoa(number)+":"+label)
	return nil
}

func (f *fakeLoopGH) RemoveIssueLabel(_ context.Context, number int, label string) error {
	f.removed = append(f.removed, strconv.Itoa(number)+":"+label)
	return nil
}

func (f *fakeLoopGH) CommentIssue(_ context.Context, number int, body string) error {
	f.comments = append(f.comments, strconv.Itoa(number)+":"+body)
	return nil
}

// fakeBatchPlanner returns a batchPlanner that hands back the supplied batches in
// order, one per call, then a non-nil empty batch once exhausted — so the drain
// loop sees "no ready issues remain" and terminates instead of spinning.
func fakeBatchPlanner(batches ...[]planItem) batchPlanner {
	call := 0
	return func(_ context.Context, _ string) ([]planItem, error) {
		if call >= len(batches) {
			return []planItem{}, nil
		}
		b := batches[call]
		call++
		return b, nil
	}
}

// resolveCall captures one planResolver invocation's salient inputs.
type resolveCall struct {
	workflow string
	vars     map[string]string
	branch   string
}

// fakeResolve returns a planResolver that records each call and returns a Plan
// whose Config is a fixed sentinel and whose RunRequest echoes the override
// branch and a per-issue prompt ("p-<ISSUE_NUMBER>"), so a test can prove the
// resolved requests reached the pool in order with the right branch/prompt.
func fakeResolve(cfg taboo.Config, calls *[]resolveCall) planResolver {
	return func(_, workflow string, vars map[string]string, ov taboo.PlanOverrides) (*taboo.Plan, error) {
		*calls = append(*calls, resolveCall{workflow: workflow, vars: vars, branch: ov.Branch})
		return &taboo.Plan{
			Config: cfg,
			Request: taboo.OrchestratedRequest{
				RunRequest: taboo.RunRequest{Branch: ov.Branch, Prompt: "p-" + vars["ISSUE_NUMBER"]},
			},
		}, nil
	}
}

// poolCall captures one poolRunner invocation's salient inputs.
type poolCall struct {
	cfg   taboo.Config
	limit int
	reqs  []taboo.RunRequest
}

// fakePool returns a poolRunner that records each call and returns one successful
// (Err nil) RunResult per request, echoing each request's branch — the happy
// path where every run in the wave succeeds.
func fakePool(calls *[]poolCall) poolRunner {
	return fakePoolWithErrs(calls, nil)
}

// fakePoolWithErrs is fakePool with per-index failures injected: results[i].Err
// is errs[i] (nil where unset), so a test can drive the mixed-outcome wave where
// some runs fail and some succeed without aborting the batch.
func fakePoolWithErrs(calls *[]poolCall, errs map[int]error) poolRunner {
	return func(_ context.Context, cfg taboo.Config, limit int, _ taboo.Commander, reqs []taboo.RunRequest) ([]taboo.RunResult, error) {
		*calls = append(*calls, poolCall{cfg: cfg, limit: limit, reqs: reqs})
		results := make([]taboo.RunResult, len(reqs))
		for i, r := range reqs {
			results[i] = taboo.RunResult{Branch: r.Branch, Err: errs[i]}
		}
		return results, nil
	}
}

// failingPool returns a poolRunner whose batch run fails wholesale (the pool
// could not start the wave), so a test can drive the release-on-batch-failure
// path. It records the call so the request count stays assertable.
func failingPool(calls *[]poolCall, err error) poolRunner {
	return func(_ context.Context, cfg taboo.Config, limit int, _ taboo.Commander, reqs []taboo.RunRequest) ([]taboo.RunResult, error) {
		*calls = append(*calls, poolCall{cfg: cfg, limit: limit, reqs: reqs})
		return nil, err
	}
}

func TestLoopRunsOneWaveAndClaimsReleasesEachIssue(t *testing.T) {
	t.Parallel()

	gh := &fakeLoopGH{issues: map[int]ghio.Issue{
		1: {Number: 1, Title: "first", Body: "body-1"},
		2: {Number: 2, Title: "second", Body: "body-2"},
	}}
	batch := []planItem{
		{Number: 1, Title: "first", Branch: "agent/issue-1-first"},
		{Number: 2, Title: "second", Branch: "agent/issue-2-second"},
	}
	planBatch := fakeBatchPlanner(batch)
	cfg := taboo.Config{Workshop: "ws-sentinel"}
	var resolves []resolveCall
	resolve := fakeResolve(cfg, &resolves)
	var pools []poolCall
	runPool := fakePool(&pools)

	opts := loopOptions{maxIterations: defaultLoopMaxIterations, parallelism: defaultLoopParallelism}
	if err := loop(context.Background(), t.TempDir(), opts, io.Discard, gh, planBatch, resolve, runPool); err != nil {
		t.Fatalf("loop returned error: %v", err)
	}

	// Each item is fetched once, in batch order.
	if got := joinInts(gh.viewed); got != "[1 2]" {
		t.Errorf("issues viewed = %v, want [1 2]", gh.viewed)
	}

	// resolve ran the "implement" workflow per item, branch + vars threaded.
	if len(resolves) != 2 {
		t.Fatalf("resolve called %d times, want 2", len(resolves))
	}
	for i, item := range batch {
		rc := resolves[i]
		if rc.workflow != "implement" {
			t.Errorf("resolve[%d] workflow = %q, want %q", i, rc.workflow, "implement")
		}
		if rc.branch != item.Branch {
			t.Errorf("resolve[%d] branch = %q, want %q", i, rc.branch, item.Branch)
		}
		if rc.vars["ISSUE_NUMBER"] != strconv.Itoa(item.Number) {
			t.Errorf("resolve[%d] ISSUE_NUMBER = %q, want %q", i, rc.vars["ISSUE_NUMBER"], strconv.Itoa(item.Number))
		}
		iss := gh.issues[item.Number]
		if rc.vars["ISSUE_TITLE"] != iss.Title || rc.vars["ISSUE_BODY"] != iss.Body {
			t.Errorf("resolve[%d] title/body vars = %q/%q, want %q/%q", i, rc.vars["ISSUE_TITLE"], rc.vars["ISSUE_BODY"], iss.Title, iss.Body)
		}
		if rc.vars["PLAN_OUTPUT_PATH"] != planFile {
			t.Errorf("resolve[%d] PLAN_OUTPUT_PATH = %q, want %q", i, rc.vars["PLAN_OUTPUT_PATH"], planFile)
		}
	}

	// One pool call carrying both requests in order, with the wave's cfg and limit.
	if len(pools) != 1 {
		t.Fatalf("pool called %d times, want 1", len(pools))
	}
	pc := pools[0]
	if pc.limit != opts.parallelism {
		t.Errorf("pool limit = %d, want %d", pc.limit, opts.parallelism)
	}
	if pc.cfg != cfg {
		t.Errorf("pool cfg = %+v, want the cfg resolve returned %+v", pc.cfg, cfg)
	}
	if len(pc.reqs) != 2 {
		t.Fatalf("pool got %d reqs, want 2", len(pc.reqs))
	}
	for i, item := range batch {
		if pc.reqs[i].Branch != item.Branch {
			t.Errorf("pool req[%d] branch = %q, want %q", i, pc.reqs[i].Branch, item.Branch)
		}
		if pc.reqs[i].Prompt != "p-"+strconv.Itoa(item.Number) {
			t.Errorf("pool req[%d] prompt = %q, want %q", i, pc.reqs[i].Prompt, "p-"+strconv.Itoa(item.Number))
		}
	}

	// Claim: ready removed + in-progress added per issue. Release: in-progress removed.
	for _, item := range batch {
		n := strconv.Itoa(item.Number)
		assertContains(t, gh.removed, n+":"+readyLabel, "claim should remove ready")
		assertContains(t, gh.added, n+":"+inProgressLabel, "claim should add in-progress")
		assertContains(t, gh.removed, n+":"+inProgressLabel, "release should remove in-progress")
	}

	// Happy path: no blocked label, no comment this cycle.
	for _, a := range gh.added {
		if strings.HasSuffix(a, ":"+blockedLabel) {
			t.Errorf("added blocked label %q, want none on the happy path", a)
		}
	}
	if len(gh.comments) != 0 {
		t.Errorf("comments = %v, want none on the happy path", gh.comments)
	}
}

func TestLoopBlocksFailedIssueWithoutAbortingBatch(t *testing.T) {
	t.Parallel()

	gh := &fakeLoopGH{issues: map[int]ghio.Issue{
		1: {Number: 1, Title: "first", Body: "b1"},
		2: {Number: 2, Title: "second", Body: "b2"},
		3: {Number: 3, Title: "third", Body: "b3"},
	}}
	batch := []planItem{
		{Number: 1, Title: "first", Branch: "agent/issue-1-first"},
		{Number: 2, Title: "second", Branch: "agent/issue-2-second"},
		{Number: 3, Title: "third", Branch: "agent/issue-3-third"},
	}
	planBatch := fakeBatchPlanner(batch)
	var resolves []resolveCall
	resolve := fakeResolve(taboo.Config{Workshop: "ws"}, &resolves)
	var pools []poolCall
	// The middle run fails; the other two succeed. A failing run must not abort
	// the wave — every item is still processed.
	runPool := fakePoolWithErrs(&pools, map[int]error{1: errors.New("boom")})

	opts := loopOptions{maxIterations: defaultLoopMaxIterations, parallelism: defaultLoopParallelism}
	if err := loop(context.Background(), t.TempDir(), opts, io.Discard, gh, planBatch, resolve, runPool); err != nil {
		t.Fatalf("loop returned error: %v", err)
	}

	// The failed issue is blocked and gets a diagnostic comment naming the issue
	// and the retry hint.
	assertContains(t, gh.added, "2:"+blockedLabel, "failed issue should be blocked")
	var failComment string
	for _, c := range gh.comments {
		if strings.HasPrefix(c, "2:") {
			failComment = c
		}
	}
	if failComment == "" {
		t.Fatalf("no comment posted on failed issue #2, comments = %v", gh.comments)
	}
	if !strings.Contains(failComment, "#2") {
		t.Errorf("comment %q should name the issue number", failComment)
	}
	if !strings.Contains(failComment, readyLabel) {
		t.Errorf("comment %q should give the retry hint (re-add %q)", failComment, readyLabel)
	}

	// The two successful issues are neither blocked nor commented on.
	for _, n := range []string{"1", "3"} {
		if got := containsStr(gh.added, n+":"+blockedLabel); got {
			t.Errorf("issue #%s succeeded but got blocked label", n)
		}
		for _, c := range gh.comments {
			if strings.HasPrefix(c, n+":") {
				t.Errorf("issue #%s succeeded but got a comment %q", n, c)
			}
		}
	}

	// Every item — failed or not — has its in-progress claim released.
	for _, item := range batch {
		n := strconv.Itoa(item.Number)
		assertContains(t, gh.removed, n+":"+inProgressLabel, "release should remove in-progress on every item")
	}
}

func TestLoopDrainsMultipleWavesThenTerminates(t *testing.T) {
	t.Parallel()

	gh := &fakeLoopGH{issues: map[int]ghio.Issue{
		1: {Number: 1, Title: "first", Body: "b1"},
		2: {Number: 2, Title: "second", Body: "b2"},
	}}
	batch1 := []planItem{{Number: 1, Title: "first", Branch: "agent/issue-1-first"}}
	batch2 := []planItem{{Number: 2, Title: "second", Branch: "agent/issue-2-second"}}
	planBatch := fakeBatchPlanner(batch1, batch2)
	var resolves []resolveCall
	resolve := fakeResolve(taboo.Config{Workshop: "ws"}, &resolves)
	var pools []poolCall
	runPool := fakePool(&pools)

	opts := loopOptions{maxIterations: defaultLoopMaxIterations, parallelism: defaultLoopParallelism}
	if err := loop(context.Background(), t.TempDir(), opts, io.Discard, gh, planBatch, resolve, runPool); err != nil {
		t.Fatalf("loop returned error: %v", err)
	}

	// Two non-empty batches drain in exactly two waves, then the empty batch stops it.
	if len(pools) != 2 {
		t.Errorf("pool called %d times, want 2 (one per non-empty wave)", len(pools))
	}
}

func TestLoopDryRunPrintsBatchAndTouchesNothing(t *testing.T) {
	t.Parallel()

	gh := &fakeLoopGH{issues: map[int]ghio.Issue{
		1: {Number: 1, Title: "first", Body: "b1"},
		2: {Number: 2, Title: "second", Body: "b2"},
	}}
	batch := []planItem{
		{Number: 1, Title: "first", Branch: "agent/issue-1-first"},
		{Number: 2, Title: "second", Branch: "agent/issue-2-second"},
	}
	planBatch := fakeBatchPlanner(batch)
	var resolves []resolveCall
	resolve := fakeResolve(taboo.Config{}, &resolves)
	var pools []poolCall
	runPool := fakePool(&pools)

	var out strings.Builder
	opts := loopOptions{maxIterations: defaultLoopMaxIterations, parallelism: defaultLoopParallelism, dryRun: true}
	if err := loop(context.Background(), t.TempDir(), opts, &out, gh, planBatch, resolve, runPool); err != nil {
		t.Fatalf("loop returned error: %v", err)
	}

	// The batch is emitted as a single JSON array line carrying both issues.
	line := strings.TrimSpace(out.String())
	if !strings.HasPrefix(line, "[") || !strings.HasSuffix(line, "]") {
		t.Errorf("dry-run output = %q, want a JSON array line", line)
	}
	if !strings.Contains(line, `"number":1`) || !strings.Contains(line, `"number":2`) {
		t.Errorf("dry-run output = %q, want both issues 1 and 2", line)
	}

	// A dry run plans and prints only: nothing is resolved, run, or relabelled.
	if len(resolves) != 0 {
		t.Errorf("resolve called %d times, want 0 on a dry run", len(resolves))
	}
	if len(pools) != 0 {
		t.Errorf("pool called %d times, want 0 on a dry run", len(pools))
	}
	if len(gh.added) != 0 || len(gh.removed) != 0 || len(gh.comments) != 0 {
		t.Errorf("gh label/comment ops on a dry run (added=%v removed=%v comments=%v), want none", gh.added, gh.removed, gh.comments)
	}
}

func TestLoopStopsAtMaxIterationsWhenQueueNeverDrains(t *testing.T) {
	t.Parallel()

	gh := &fakeLoopGH{issues: map[int]ghio.Issue{
		1: {Number: 1, Title: "first", Body: "b1"},
	}}
	// A planner that always hands back the same non-empty batch: the queue never
	// drains, so only the maxIterations bound can stop the loop.
	batch := []planItem{{Number: 1, Title: "first", Branch: "agent/issue-1-first"}}
	planBatch := func(_ context.Context, _ string) ([]planItem, error) { return batch, nil }
	var resolves []resolveCall
	resolve := fakeResolve(taboo.Config{Workshop: "ws"}, &resolves)
	var pools []poolCall
	runPool := fakePool(&pools)

	opts := loopOptions{maxIterations: 3, parallelism: defaultLoopParallelism}
	if err := loop(context.Background(), t.TempDir(), opts, io.Discard, gh, planBatch, resolve, runPool); err != nil {
		t.Fatalf("loop returned error: %v", err)
	}

	// The loop is bounded: exactly maxIterations waves run, never infinite.
	if len(pools) != 3 {
		t.Errorf("pool called %d times, want 3 (capped at maxIterations)", len(pools))
	}
}

func TestLoopBatchRunErrorReleasesClaims(t *testing.T) {
	t.Parallel()

	gh := &fakeLoopGH{issues: map[int]ghio.Issue{
		1: {Number: 1, Title: "first", Body: "b1"},
		2: {Number: 2, Title: "second", Body: "b2"},
	}}
	batch := []planItem{
		{Number: 1, Title: "first", Branch: "agent/issue-1-first"},
		{Number: 2, Title: "second", Branch: "agent/issue-2-second"},
	}
	planBatch := fakeBatchPlanner(batch)
	var resolves []resolveCall
	resolve := fakeResolve(taboo.Config{Workshop: "ws"}, &resolves)
	var pools []poolCall
	// The whole batch fails to run (e.g. the context was canceled before fan-out).
	runPool := failingPool(&pools, errors.New("ctx canceled"))

	opts := loopOptions{maxIterations: defaultLoopMaxIterations, parallelism: defaultLoopParallelism}
	err := loop(context.Background(), t.TempDir(), opts, io.Discard, gh, planBatch, resolve, runPool)
	if err == nil {
		t.Fatalf("loop returned nil, want an error when the batch run fails")
	}
	if !strings.Contains(err.Error(), "run batch") {
		t.Errorf("error = %q, want it to mention %q", err.Error(), "run batch")
	}

	// On a batch-run failure each item is unclaimed: in-progress released AND ready
	// restored, so the issue returns to the pool for a later wave (or a retrigger)
	// instead of being stranded with neither label.
	for _, item := range batch {
		n := strconv.Itoa(item.Number)
		assertContains(t, gh.removed, n+":"+inProgressLabel, "batch-run failure should release in-progress")
		assertContains(t, gh.added, n+":"+readyLabel, "batch-run failure should restore ready")
	}

	// A failed batch is not a per-run failure: no issue is blocked or commented on.
	for _, item := range batch {
		n := strconv.Itoa(item.Number)
		if containsStr(gh.added, n+":"+blockedLabel) {
			t.Errorf("issue #%s got blocked label on a whole-batch failure, want none", n)
		}
	}
	if len(gh.comments) != 0 {
		t.Errorf("comments = %v, want none on a whole-batch failure", gh.comments)
	}
}

func TestLoopResolveSetupFailureAbortsWaveBeforeClaim(t *testing.T) {
	t.Parallel()

	// IssueView fails while resolving requests. That is a setup error, not a per-run
	// failure: the wave must abort before any issue is claimed, so no label is ever
	// touched and the pool never runs.
	gh := &fakeLoopGH{
		issues:  map[int]ghio.Issue{1: {Number: 1, Title: "first", Body: "b1"}},
		viewErr: errors.New("gh issue view boom"),
	}
	batch := []planItem{{Number: 1, Title: "first", Branch: "agent/issue-1-first"}}
	planBatch := fakeBatchPlanner(batch)
	var resolves []resolveCall
	resolve := fakeResolve(taboo.Config{Workshop: "ws"}, &resolves)
	var pools []poolCall
	runPool := fakePool(&pools)

	opts := loopOptions{maxIterations: defaultLoopMaxIterations, parallelism: defaultLoopParallelism}
	err := loop(context.Background(), t.TempDir(), opts, io.Discard, gh, planBatch, resolve, runPool)
	if err == nil {
		t.Fatal("loop returned nil, want an error when issue fetch fails during resolve")
	}
	if !strings.Contains(err.Error(), "fetch issue") {
		t.Errorf("error = %q, want it to mention %q", err.Error(), "fetch issue")
	}

	// No claim, no run: the setup failure short-circuits before any side effect.
	if len(gh.added) != 0 || len(gh.removed) != 0 || len(gh.comments) != 0 {
		t.Errorf("labels/comments touched on a setup failure (added=%v removed=%v comments=%v), want none", gh.added, gh.removed, gh.comments)
	}
	if len(pools) != 0 {
		t.Errorf("pool called %d times, want 0 when resolve setup fails", len(pools))
	}
}

func TestLoopReturnsImmediatelyWhenFirstBatchEmpty(t *testing.T) {
	t.Parallel()

	gh := &fakeLoopGH{issues: map[int]ghio.Issue{}}
	planBatch := fakeBatchPlanner() // first call already exhausted -> empty
	var resolves []resolveCall
	resolve := fakeResolve(taboo.Config{}, &resolves)
	var pools []poolCall
	runPool := fakePool(&pools)

	opts := loopOptions{maxIterations: defaultLoopMaxIterations, parallelism: defaultLoopParallelism}
	if err := loop(context.Background(), t.TempDir(), opts, io.Discard, gh, planBatch, resolve, runPool); err != nil {
		t.Fatalf("loop returned error: %v", err)
	}

	if len(resolves) != 0 {
		t.Errorf("resolve called %d times, want 0 when nothing is ready", len(resolves))
	}
	if len(pools) != 0 {
		t.Errorf("pool called %d times, want 0 when nothing is ready", len(pools))
	}
	if len(gh.viewed) != 0 || len(gh.added) != 0 || len(gh.removed) != 0 || len(gh.comments) != 0 {
		t.Errorf("gh was touched (viewed=%v added=%v removed=%v comments=%v), want untouched", gh.viewed, gh.added, gh.removed, gh.comments)
	}
}

// joinInts renders a []int as its default fmt form for substring comparison.
func joinInts(ns []int) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = strconv.Itoa(n)
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// containsStr reports whether want is among got.
func containsStr(got []string, want string) bool {
	for _, g := range got {
		if g == want {
			return true
		}
	}
	return false
}

// assertContains fails the test if want is not among got.
func assertContains(t *testing.T, got []string, want, why string) {
	t.Helper()
	for _, g := range got {
		if g == want {
			return
		}
	}
	t.Errorf("%s: %q not in %v", why, want, got)
}

// Ensure *ghio.Client satisfies loopGH, so the production wiring stays type-correct.
var _ loopGH = (*ghio.Client)(nil)
