package taboo

import (
	"context"
	"errors"
	"testing"
)

// countVerb returns how many recorded calls have the given workshop/git verb.
func (f *fakeCommander) countVerb(verb string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if verbOf(c) == verb {
			n++
		}
	}
	return n
}

func TestOrchestrator_SingleIterationByDefault(t *testing.T) {
	fc := &fakeCommander{} // info succeeds -> workshop present
	cfg := testConfig(t)
	o := NewOrchestrator(New(cfg, fc))

	res, err := o.Run(context.Background(), OrchestratedRequest{
		RunRequest: RunRequest{Branch: "agent/x", Prompt: "go"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Iterations != 1 {
		t.Errorf("Iterations = %d, want 1 (zero MaxIterations defaults to one run)", res.Iterations)
	}
	if res.StopReason != StopMaxIterations {
		t.Errorf("StopReason = %q, want %q", res.StopReason, StopMaxIterations)
	}
	if got := fc.countVerb("exec"); got != 1 {
		t.Errorf("exec count = %d, want 1", got)
	}
}

func TestOrchestrator_LoopsToMaxIterations(t *testing.T) {
	fc := &fakeCommander{} // no stdout -> signal never seen
	cfg := testConfig(t)
	o := NewOrchestrator(New(cfg, fc))

	res, err := o.Run(context.Background(), OrchestratedRequest{
		RunRequest:       RunRequest{Branch: "agent/x", Prompt: "go"},
		MaxIterations:    3,
		CompletionSignal: "DONE",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Iterations != 3 {
		t.Errorf("Iterations = %d, want 3", res.Iterations)
	}
	if res.StopReason != StopMaxIterations {
		t.Errorf("StopReason = %q, want %q", res.StopReason, StopMaxIterations)
	}
	// The agent re-execs once per iteration...
	if got := fc.countVerb("exec"); got != 3 {
		t.Errorf("exec count = %d, want 3 (one per iteration)", got)
	}
	// ...but the worktree is created ONCE and reused. A second `worktree add`
	// would fail against real git (the fake now enforces this), so this guards
	// against a loop that re-runs the full per-run setup every iteration.
	if got := fc.countVerb("worktree"); got != 1 {
		t.Errorf("worktree add count = %d, want 1 (Setup runs once, then Exec loops)", got)
	}
}

func TestOrchestrator_StopsEarlyOnSignal(t *testing.T) {
	// The agent emits the sentinel on its first exec; the loop must stop there
	// rather than exhausting MaxIterations.
	fc := &fakeCommander{
		stdoutFn: func(c Cmd) string {
			if verbOf(c) == "exec" {
				return "working...\nTASK-DONE\n"
			}
			return ""
		},
	}
	cfg := testConfig(t)
	o := NewOrchestrator(New(cfg, fc))

	res, err := o.Run(context.Background(), OrchestratedRequest{
		RunRequest:       RunRequest{Branch: "agent/x", Prompt: "go"},
		MaxIterations:    5,
		CompletionSignal: "TASK-DONE",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Iterations != 1 {
		t.Errorf("Iterations = %d, want 1 (stop on first signal)", res.Iterations)
	}
	if res.StopReason != StopSignal {
		t.Errorf("StopReason = %q, want %q", res.StopReason, StopSignal)
	}
	if got := fc.countVerb("exec"); got != 1 {
		t.Errorf("exec count = %d, want 1 (no further iterations after signal)", got)
	}
}

func TestOrchestrator_SignalMustMatchConfigured(t *testing.T) {
	// The agent emits a sentinel, but it is not the one configured for this run,
	// so the loop must run to completion rather than stopping early.
	fc := &fakeCommander{
		stdoutFn: func(c Cmd) string {
			if verbOf(c) == "exec" {
				return "OTHER-SENTINEL\n"
			}
			return ""
		},
	}
	cfg := testConfig(t)
	o := NewOrchestrator(New(cfg, fc))

	res, err := o.Run(context.Background(), OrchestratedRequest{
		RunRequest:       RunRequest{Branch: "agent/x", Prompt: "go"},
		MaxIterations:    2,
		CompletionSignal: "TASK-DONE",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.StopReason != StopMaxIterations {
		t.Errorf("StopReason = %q, want %q (non-matching sentinel must not stop)", res.StopReason, StopMaxIterations)
	}
	if res.Iterations != 2 {
		t.Errorf("Iterations = %d, want 2", res.Iterations)
	}
}

func TestOrchestrator_SurfacesExtractedResult(t *testing.T) {
	// The agent's final output carries a <result> block; the orchestrator runs
	// the extractor once post-loop and surfaces the typed value on res.Result.
	fc := &fakeCommander{
		stdoutFn: func(c Cmd) string {
			if verbOf(c) == "exec" {
				return "done\n<result>{\"summary\":\"shipped\",\"score\":8}</result>\n"
			}
			return ""
		},
	}
	o := NewOrchestrator(New(testConfig(t), fc))

	res, err := o.Run(context.Background(), OrchestratedRequest{
		RunRequest:      RunRequest{Branch: "agent/x", Prompt: "go"},
		ResultExtractor: JSONResult[review](),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rv, ok := res.Result.(review)
	if !ok {
		t.Fatalf("res.Result = %T, want taboo.review", res.Result)
	}
	if rv.Summary != "shipped" || rv.Score != 8 {
		t.Errorf("res.Result = %+v, want {shipped 8}", rv)
	}
}

func TestOrchestrator_NoExtractorLeavesResultNil(t *testing.T) {
	// Back-compat: without a ResultExtractor the run behaves as before and
	// res.Result stays nil with no error.
	fc := &fakeCommander{
		stdoutFn: func(c Cmd) string {
			if verbOf(c) == "exec" {
				return "<result>{\"summary\":\"ignored\"}</result>"
			}
			return ""
		},
	}
	o := NewOrchestrator(New(testConfig(t), fc))

	res, err := o.Run(context.Background(), OrchestratedRequest{
		RunRequest: RunRequest{Branch: "agent/x", Prompt: "go"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Result != nil {
		t.Errorf("res.Result = %v, want nil (no extractor configured)", res.Result)
	}
}

func TestOrchestrator_ExtractionErrorKeepsResultPopulated(t *testing.T) {
	// The extractor is set but the agent emits no block: o.Run returns
	// ErrNoResult, yet the populated result (Commit/Output/Iterations) is
	// preserved so a failed extraction never discards the agent's commit.
	fc := &fakeCommander{
		stdoutFn: func(c Cmd) string {
			if verbOf(c) == "exec" {
				return "the agent never emitted a block\n"
			}
			return ""
		},
	}
	o := NewOrchestrator(New(testConfig(t), fc))

	res, err := o.Run(context.Background(), OrchestratedRequest{
		RunRequest:      RunRequest{Branch: "agent/x", Prompt: "go"},
		MaxIterations:   2,
		ResultExtractor: JSONResult[review](),
	})
	if !errors.Is(err, ErrNoResult) {
		t.Fatalf("Run err = %v, want ErrNoResult", err)
	}
	if res.Result != nil {
		t.Errorf("res.Result = %v, want nil on extraction failure", res.Result)
	}
	// The load-bearing guarantee: a failed extraction returns the wrapped
	// sentinel yet preserves the agent's captured output, so the commit is never
	// discarded. (Iteration count is covered by TestOrchestrator_LoopsToMaxIterations.)
	if res.Output == "" {
		t.Error("res.Output is empty; the populated result must survive extraction failure")
	}
}

func TestOrchestrator_StopsOnRunnerError(t *testing.T) {
	// A failing iteration aborts the loop and surfaces the error; no further
	// iterations run.
	fc := &fakeCommander{errFn: failOnVerb("exec")}
	cfg := testConfig(t)
	o := NewOrchestrator(New(cfg, fc))

	res, err := o.Run(context.Background(), OrchestratedRequest{
		RunRequest:    RunRequest{Branch: "agent/x", Prompt: "go"},
		MaxIterations: 3,
	})
	if err == nil {
		t.Fatal("Run: want error from failing iteration, got nil")
	}
	if res.Iterations != 1 {
		t.Errorf("Iterations = %d, want 1 (loop aborts on first failure)", res.Iterations)
	}
	if got := fc.countVerb("exec"); got != 1 {
		t.Errorf("exec count = %d, want 1 (no retry after error)", got)
	}
}

// A forked run cannot be looped. The orchestrator re-execs the unchanged
// RunRequest each iteration, so Fork with MaxIterations > 1 would re-fork the
// source session every iteration rather than continue the fork — and taboo
// cannot yet capture the new id to resume it across iterations. Run rejects the
// combination up front with ErrForkLoop, before any workshop/git command runs.
func TestOrchestrator_ForkWithMultipleIterationsRejected(t *testing.T) {
	fc := &fakeCommander{}
	cfg := testConfig(t)
	o := NewOrchestrator(New(cfg, fc))

	_, err := o.Run(context.Background(), OrchestratedRequest{
		RunRequest:    RunRequest{Branch: "fork/x", Prompt: "go", ResumeSession: "ses_src", Fork: true},
		MaxIterations: 2,
	})
	if !errors.Is(err, ErrForkLoop) {
		t.Fatalf("Run err = %v, want ErrForkLoop", err)
	}
	// Rejected before dispatching anything, so the expensive Setup never runs.
	if got := len(fc.snapshot()); got != 0 {
		t.Errorf("dispatched %d commands, want 0 (reject before setup): %v", got, fc.verbs())
	}
}

// The fork-loop guard is narrow: a single-iteration fork is allowed (there is no
// loop to re-fork), and a multi-iteration plain resume is allowed too — resume
// mutates the source session in place, so each iteration continues the session
// the previous one grew. Only fork combined with a loop is rejected.
func TestOrchestrator_ForkSingleIterationAndResumeLoopAllowed(t *testing.T) {
	// Single-iteration fork: allowed (MaxIterations 0 -> one run).
	fcFork := &fakeCommander{}
	if _, err := NewOrchestrator(New(testConfig(t), fcFork)).Run(context.Background(), OrchestratedRequest{
		RunRequest: RunRequest{Branch: "fork/x", Prompt: "go", ResumeSession: "ses_src", Fork: true},
	}); err != nil {
		t.Fatalf("single-iteration fork: %v", err)
	}

	// Multi-iteration plain resume: allowed, and loops the agent once per iteration.
	fcResume := &fakeCommander{}
	if _, err := NewOrchestrator(New(testConfig(t), fcResume)).Run(context.Background(), OrchestratedRequest{
		RunRequest:    RunRequest{Branch: "agent/x", Prompt: "go", ResumeSession: "ses_src"},
		MaxIterations: 3,
	}); err != nil {
		t.Fatalf("multi-iteration resume: %v", err)
	}
	if got := fcResume.countVerb("exec"); got != 3 {
		t.Errorf("resume loop exec count = %d, want 3 (one per iteration)", got)
	}
}
