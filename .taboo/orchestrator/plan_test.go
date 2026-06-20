package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"afk/internal/ghio"

	taboo "github.com/josecabralf/taboo/pkg"
)

// fakePlanGH records the labels it was queried for and returns canned issues per
// label, without shelling out. The states field maps an issue number to the
// state IssueState should report; numbers absent from it default to "CLOSED" (so
// the cycle-1/2 fixtures, whose bodies declare no "blocked by", are unaffected).
// The stateErr field, if set for a number, makes IssueState fail for that dependency.
type fakePlanGH struct {
	calls       []string
	by          map[string][]ghio.Issue
	states      map[int]string
	stateErr    map[int]error
	stateLookup []int
}

func (f *fakePlanGH) ListOpenIssuesByLabel(_ context.Context, label string) ([]ghio.Issue, error) {
	f.calls = append(f.calls, label)
	if issues, ok := f.by[label]; ok {
		return issues, nil
	}
	return []ghio.Issue{}, nil
}

func (f *fakePlanGH) IssueState(_ context.Context, number int) (string, error) {
	f.stateLookup = append(f.stateLookup, number)
	if err, ok := f.stateErr[number]; ok {
		return "", err
	}
	if state, ok := f.states[number]; ok {
		return state, nil
	}
	return "CLOSED", nil
}

// fakePlanRunner returns a planRunner that records the vars it was called with
// (into capturedVars) and hands back canned items (or an error). Because the
// bridge threads the JSONResult extractor into the run loop, the fake returns a
// typed []planItem directly — no hand-encoded <result> string.
func fakePlanRunner(capturedVars *map[string]string, items []planItem, err error) planRunner {
	return func(_ context.Context, _, _ string, vars map[string]string, _ taboo.PlanOverrides, _ taboo.Commander) ([]planItem, taboo.OrchestratedResult, error) {
		*capturedVars = vars
		return items, taboo.OrchestratedResult{}, err
	}
}

// recordingPlanRunner is like fakePlanRunner but also flips *called to true when
// invoked, so a test can assert the plan agent was (or was not) run.
func recordingPlanRunner(called *bool, items []planItem) planRunner {
	return func(_ context.Context, _, _ string, _ map[string]string, _ taboo.PlanOverrides, _ taboo.Commander) ([]planItem, taboo.OrchestratedResult, error) {
		*called = true
		return items, taboo.OrchestratedResult{}, nil
	}
}

func TestPlanPrintsSelectedItemsAsJSON(t *testing.T) {
	t.Parallel()

	gh := &fakePlanGH{by: map[string][]ghio.Issue{
		readyLabel: {{Number: 1, Title: "a", Body: "x"}, {Number: 2, Title: "b", Body: "y"}},
	}}
	items := []planItem{{Number: 1, Title: "a", Branch: "agent/issue-1-a"}}
	var captured map[string]string
	run := fakePlanRunner(&captured, items, nil)

	var buf bytes.Buffer
	if err := plan(context.Background(), t.TempDir(), &buf, gh, run); err != nil {
		t.Fatalf("plan returned error: %v", err)
	}

	wantJSON, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	if got, want := buf.String(), string(wantJSON)+"\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}

	wantCalls := []string{readyLabel, inProgressLabel}
	if len(gh.calls) != len(wantCalls) || gh.calls[0] != wantCalls[0] || gh.calls[1] != wantCalls[1] {
		t.Errorf("gh queried labels %v, want exactly %v", gh.calls, wantCalls)
	}

	candidates := captured["CANDIDATES"]
	for _, n := range []int{1, 2} {
		if !strings.Contains(candidates, strconv.Itoa(n)) {
			t.Errorf("CANDIDATES var %q, want it to contain issue number %d", candidates, n)
		}
	}
}

func TestPlanSurfacesRunFailure(t *testing.T) {
	t.Parallel()

	// When the agent emits no usable <result> block the bridge returns ErrNoResult.
	// plan must surface it, wrapped with the run-agent prefix.
	gh := &fakePlanGH{by: map[string][]ghio.Issue{readyLabel: {{Number: 1, Title: "a", Body: "x"}}}}
	var captured map[string]string
	run := fakePlanRunner(&captured, nil, taboo.ErrNoResult)

	var buf bytes.Buffer
	err := plan(context.Background(), t.TempDir(), &buf, gh, run)
	if err == nil {
		t.Fatal("plan returned nil, want a run error")
	}
	if !errors.Is(err, taboo.ErrNoResult) {
		t.Errorf("error = %v, want it to wrap taboo.ErrNoResult", err)
	}
	if !strings.HasPrefix(err.Error(), "run plan agent: ") {
		t.Errorf("error = %q, want it to start with %q", err.Error(), "run plan agent: ")
	}
}

func TestPlanExcludesInProgressIssues(t *testing.T) {
	t.Parallel()

	gh := &fakePlanGH{by: map[string][]ghio.Issue{
		readyLabel: {
			{Number: 1, Title: "a", Body: "x"},
			{Number: 2, Title: "b", Body: "y"},
			{Number: 3, Title: "c", Body: "z"},
		},
		inProgressLabel: {{Number: 2, Title: "b", Body: "y"}},
	}}
	var captured map[string]string
	run := fakePlanRunner(&captured, []planItem{{Number: 1, Title: "a", Branch: "agent/issue-1-a"}}, nil)

	var buf bytes.Buffer
	if err := plan(context.Background(), t.TempDir(), &buf, gh, run); err != nil {
		t.Fatalf("plan returned error: %v", err)
	}

	if captured == nil {
		t.Fatal("runner was not called: CANDIDATES were never captured")
	}

	var candidates []ghio.Issue
	if err := json.Unmarshal([]byte(captured["CANDIDATES"]), &candidates); err != nil {
		t.Fatalf("decode CANDIDATES %q: %v", captured["CANDIDATES"], err)
	}

	got := make([]int, len(candidates))
	for i, c := range candidates {
		got[i] = c.Number
	}
	want := []int{1, 3}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("candidate numbers = %v, want %v (in-progress #2 excluded)", got, want)
	}
}

func TestPlanDropsBlockedIssues(t *testing.T) {
	t.Parallel()

	gh := &fakePlanGH{
		by: map[string][]ghio.Issue{
			readyLabel: {
				{Number: 1, Title: "a", Body: "Blocked by #5"},
				{Number: 2, Title: "b", Body: "Blocked by #6"},
				{Number: 3, Title: "c", Body: "no deps"},
			},
		},
		states: map[int]string{5: "OPEN", 6: "CLOSED"},
	}
	var captured map[string]string
	run := fakePlanRunner(&captured, []planItem{{Number: 2, Title: "b", Branch: "agent/issue-2-b"}}, nil)

	var buf bytes.Buffer
	if err := plan(context.Background(), t.TempDir(), &buf, gh, run); err != nil {
		t.Fatalf("plan returned error: %v", err)
	}

	if captured == nil {
		t.Fatal("runner was not called: CANDIDATES were never captured")
	}

	var candidates []ghio.Issue
	if err := json.Unmarshal([]byte(captured["CANDIDATES"]), &candidates); err != nil {
		t.Fatalf("decode CANDIDATES %q: %v", captured["CANDIDATES"], err)
	}

	got := make([]int, len(candidates))
	for i, c := range candidates {
		got[i] = c.Number
	}
	want := []int{2, 3}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("candidate numbers = %v, want %v (#1 blocked by open #5)", got, want)
	}
}

func TestPlanShortCircuitsWhenAllBlocked(t *testing.T) {
	t.Parallel()

	gh := &fakePlanGH{
		by:     map[string][]ghio.Issue{readyLabel: {{Number: 1, Title: "a", Body: "Blocked by #5"}}},
		states: map[int]string{5: "OPEN"},
	}
	var called bool
	run := recordingPlanRunner(&called, []planItem{{Number: 1, Title: "a", Branch: "agent/issue-1-a"}})

	var buf bytes.Buffer
	if err := plan(context.Background(), t.TempDir(), &buf, gh, run); err != nil {
		t.Fatalf("plan returned error: %v", err)
	}

	if called {
		t.Error("plan agent was run, want it skipped when no candidate is eligible")
	}
	if got, want := buf.String(), "[]\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestPlanSurfacesDependencyLookupError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("gh boom")
	gh := &fakePlanGH{
		by:       map[string][]ghio.Issue{readyLabel: {{Number: 1, Title: "a", Body: "Blocked by #5"}}},
		stateErr: map[int]error{5: wantErr},
	}
	var captured map[string]string
	run := fakePlanRunner(&captured, nil, nil)

	var buf bytes.Buffer
	err := plan(context.Background(), t.TempDir(), &buf, gh, run)
	if err == nil {
		t.Fatal("plan returned nil, want a dependency-lookup error")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want it to wrap %v", err, wantErr)
	}
	if !strings.HasPrefix(err.Error(), "check dependencies for #1: ") {
		t.Errorf("error = %q, want it to start with %q", err.Error(), "check dependencies for #1: ")
	}
}

func TestExcludeInProgress(t *testing.T) {
	t.Parallel()

	issue := func(n int) ghio.Issue { return ghio.Issue{Number: n} }
	tests := map[string]struct {
		ready      []ghio.Issue
		inProgress []ghio.Issue
		want       []int
	}{
		"empty in-progress passes ready through": {
			ready:      []ghio.Issue{issue(1), issue(2), issue(3)},
			inProgress: nil,
			want:       []int{1, 2, 3},
		},
		"drops the in-progress issue, keeps order": {
			ready:      []ghio.Issue{issue(1), issue(2), issue(3)},
			inProgress: []ghio.Issue{issue(2)},
			want:       []int{1, 3},
		},
		"drops multiple in-progress issues": {
			ready:      []ghio.Issue{issue(1), issue(2), issue(3), issue(4)},
			inProgress: []ghio.Issue{issue(1), issue(3)},
			want:       []int{2, 4},
		},
		"all ready in progress yields none": {
			ready:      []ghio.Issue{issue(1), issue(2)},
			inProgress: []ghio.Issue{issue(1), issue(2)},
			want:       []int{},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := excludeInProgress(tc.ready, tc.inProgress)
			gotNums := make([]int, len(got))
			for i, c := range got {
				gotNums[i] = c.Number
			}
			if len(gotNums) != len(tc.want) {
				t.Fatalf("numbers = %v, want %v", gotNums, tc.want)
			}
			for i := range tc.want {
				if gotNums[i] != tc.want[i] {
					t.Fatalf("numbers = %v, want %v", gotNums, tc.want)
				}
			}
		})
	}
}

func TestParseBlockedBy(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		body string
		want []int
	}{
		"single dependency":        {body: "Blocked by #12", want: []int{12}},
		"comma list, lowercase":    {body: "blocked by #12, #13", want: []int{12, 13}},
		"no blocked-by means none": {body: "Some text. Fixes #9. Closes #8.", want: nil},
		"deduped first-seen order": {body: "Blocked by #5\n\nBlocked by #5", want: []int{5}},
		"empty body":               {body: "", want: nil},
		"bulleted markdown list":   {body: "## Blocked by\n\n- #79\n- #80", want: []int{79, 80}},
		"colon then bulleted list": {body: "Blocked by:\n- #5\n- #6", want: []int{5, 6}},
		"blank line ends the run":  {body: "Blocked by #5\n\n#99 unrelated", want: []int{5}},
		"fixes is not blocked-by":  {body: "Fixes #9", want: nil},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := parseBlockedBy(tc.body)
			if len(got) != len(tc.want) {
				t.Fatalf("parseBlockedBy(%q) = %v, want %v", tc.body, got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("parseBlockedBy(%q) = %v, want %v", tc.body, got, tc.want)
				}
			}
		})
	}
}

func TestUnblockedCandidatesDedupesSharedDepAndIsCaseInsensitive(t *testing.T) {
	t.Parallel()

	// Two candidates share dependency #5; a third depends on #6. #5 reports a
	// lowercase "closed" (EqualFold must treat it as resolved); #6 is OPEN.
	gh := &fakePlanGH{
		by: map[string][]ghio.Issue{
			readyLabel: {
				{Number: 1, Title: "a", Body: "Blocked by #5"},
				{Number: 2, Title: "b", Body: "Blocked by #5"},
				{Number: 3, Title: "c", Body: "Blocked by #6"},
			},
		},
		states: map[int]string{5: "closed", 6: "OPEN"},
	}
	run := recordingPlanRunner(new(bool), []planItem{{Number: 1, Title: "a", Branch: "agent/issue-1-a"}})

	var buf bytes.Buffer
	if err := plan(context.Background(), t.TempDir(), &buf, gh, run); err != nil {
		t.Fatalf("plan returned error: %v", err)
	}

	// #1 and #2 survive (lowercase "closed" is resolved); #3 is dropped (#6 OPEN).
	// #5 is queried once despite two candidates depending on it; #6 once.
	if got, want := len(gh.stateLookup), 2; got != want {
		t.Errorf("IssueState called %d times %v, want %d (shared dep #5 fetched once)", got, gh.stateLookup, want)
	}
	count5 := 0
	for _, n := range gh.stateLookup {
		if n == 5 {
			count5++
		}
	}
	if count5 != 1 {
		t.Errorf("dependency #5 queried %d times, want 1 (cached across candidates)", count5)
	}
}

// Ensure *ghio.Client satisfies planGH and the typed bridge satisfies planRunner,
// so the production wiring in runPlan stays type-correct.
var (
	_ planGH     = (*ghio.Client)(nil)
	_ planRunner = taboo.RunWorkflowAs[[]planItem]
)
