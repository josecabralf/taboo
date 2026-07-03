package run

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/josecabralf/taboo/internal/exec"
	"github.com/josecabralf/taboo/internal/result"
)

// review is the typed result shape the orchestrator extraction tests decode an
// agent's <result> block into. It stands in for whatever struct a caller would
// pass to JSONResult[T].
type review struct {
	Summary string `json:"summary"`
	Score   int    `json:"score"`
}

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
	if got := fc.countVerb("exec"); got != 3 {
		t.Errorf("exec count = %d, want 3 (one per iteration)", got)
	}
	// The worktree is created ONCE and reused. A second `worktree add` would fail
	// against real git (the fake now enforces this), so this guards against a loop
	// that re-runs the full per-run setup every iteration.
	if got := fc.countVerb("worktree"); got != 1 {
		t.Errorf("worktree add count = %d, want 1 (Setup runs once, then Exec loops)", got)
	}
}

// TestOrchestrator_BaseCommitSurvivesIterations pins that Setup's base capture
// rides the embedded RunResult across the whole loop: while Commit advances
// with every iteration's final-HEAD capture, the final result still pairs the
// last Commit with the ORIGINAL base, so Changed() compares the run's end
// against where the branch started, not against the previous iteration.
func TestOrchestrator_BaseCommitSurvivesIterations(t *testing.T) {
	// The first rev-parse is Setup's base capture; each later one is an
	// iteration's final-HEAD capture, advancing every time.
	fc := &fakeCommander{stdoutFn: shaSequence("base0001", "head0001", "head0002", "head0003")}
	o := NewOrchestrator(New(testConfig(t), fc))

	res, err := o.Run(context.Background(), OrchestratedRequest{
		RunRequest:    RunRequest{Branch: "agent/x", Prompt: "go"},
		MaxIterations: 3,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Iterations != 3 {
		t.Fatalf("Iterations = %d, want 3", res.Iterations)
	}
	if res.BaseCommit != "base0001" {
		t.Errorf("BaseCommit = %q, want base0001 (the original base must survive every iteration)", res.BaseCommit)
	}
	if res.Commit != "head0003" {
		t.Errorf("Commit = %q, want head0003 (the last iteration's HEAD)", res.Commit)
	}
	if !res.Changed() {
		t.Error("Changed() = false, want true (final HEAD differs from the original base)")
	}
}

func TestOrchestrator_StopsEarlyOnSignal(t *testing.T) {
	// The agent emits the sentinel on its first exec; the loop must stop there
	// rather than exhausting MaxIterations.
	fc := &fakeCommander{
		stdoutFn: func(c exec.Cmd) string {
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
		stdoutFn: func(c exec.Cmd) string {
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
		stdoutFn: func(c exec.Cmd) string {
			if verbOf(c) == "exec" {
				return "done\n<result>{\"summary\":\"shipped\",\"score\":8}</result>\n"
			}
			return ""
		},
	}
	o := NewOrchestrator(New(testConfig(t), fc))

	res, err := o.Run(context.Background(), OrchestratedRequest{
		RunRequest:      RunRequest{Branch: "agent/x", Prompt: "go"},
		ResultExtractor: result.JSONResult[review](),
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
		stdoutFn: func(c exec.Cmd) string {
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
		stdoutFn: func(c exec.Cmd) string {
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
		ResultExtractor: result.JSONResult[review](),
	})
	if !errors.Is(err, result.ErrNoResult) {
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

// shaSequence programs a fake commander whose successive `git rev-parse HEAD`
// calls answer the given SHAs in order (the first is Setup's base capture, each
// later one an iteration's final-HEAD capture), repeating the last SHA once the
// script is exhausted. Every other verb answers empty stdout.
func shaSequence(shas ...string) func(exec.Cmd) string {
	var revParses atomic.Int32
	return func(c exec.Cmd) string {
		if verbOf(c) != "rev-parse" {
			return ""
		}
		n := int(revParses.Add(1))
		if n > len(shas) {
			n = len(shas)
		}
		return shas[n-1] + "\n"
	}
}

// TestOrchestrator_StopOnNoChangeStopsAtStall pins the opt-in commit-based
// early stop: with StopOnNoChange on, an iteration whose final-HEAD capture
// equals the previous tip is a fixed point — the loop stops there with
// StopNoChange instead of paying the remaining iterations.
func TestOrchestrator_StopOnNoChangeStopsAtStall(t *testing.T) {
	// base, iter1 moves the tip, iter2 stalls on the same SHA.
	fc := &fakeCommander{stdoutFn: shaSequence("base0001", "head0001", "head0001")}
	o := NewOrchestrator(New(testConfig(t), fc))

	res, err := o.Run(context.Background(), OrchestratedRequest{
		RunRequest:     RunRequest{Branch: "agent/x", Prompt: "go"},
		MaxIterations:  5,
		StopOnNoChange: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopNoChange {
		t.Errorf("StopReason = %q, want %q", res.StopReason, StopNoChange)
	}
	if res.Iterations != 2 {
		t.Errorf("Iterations = %d, want 2 (stop at the stalled iteration)", res.Iterations)
	}
	if got := fc.countVerb("exec"); got != 2 {
		t.Errorf("exec count = %d, want 2 (no re-run after the fixed point)", got)
	}
}

// TestOrchestrator_StopOnNoChangeFinalIterationTieBreak pins the tie-break: a
// stall on the LAST allowed iteration reports StopNoChange, not
// StopMaxIterations — the check runs after every Exec including the final one,
// and the budget running out at the same moment doesn't change that the tip
// stopped moving.
func TestOrchestrator_StopOnNoChangeFinalIterationTieBreak(t *testing.T) {
	// base, iter1 moves the tip, iter2 (the last allowed) stalls on the same SHA.
	fc := &fakeCommander{stdoutFn: shaSequence("base0001", "head0001", "head0001")}
	o := NewOrchestrator(New(testConfig(t), fc))

	res, err := o.Run(context.Background(), OrchestratedRequest{
		RunRequest:     RunRequest{Branch: "agent/x", Prompt: "go"},
		MaxIterations:  2,
		StopOnNoChange: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopNoChange {
		t.Errorf("StopReason = %q, want %q (no-change wins the final-iteration tie)", res.StopReason, StopNoChange)
	}
	if res.Iterations != 2 {
		t.Errorf("Iterations = %d, want 2", res.Iterations)
	}
}

// TestOrchestrator_StopOnNoChangeFirstIterationNoOp pins the seed: prev starts
// at Setup's BaseCommit, so a first Exec whose capture equals the base stops
// the loop at iteration 1 — the agent did nothing at all.
func TestOrchestrator_StopOnNoChangeFirstIterationNoOp(t *testing.T) {
	// Setup's base capture and the first iteration's capture answer the same SHA.
	fc := &fakeCommander{stdoutFn: shaSequence("base0001", "base0001")}
	o := NewOrchestrator(New(testConfig(t), fc))

	res, err := o.Run(context.Background(), OrchestratedRequest{
		RunRequest:     RunRequest{Branch: "agent/x", Prompt: "go"},
		MaxIterations:  4,
		StopOnNoChange: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopNoChange {
		t.Errorf("StopReason = %q, want %q", res.StopReason, StopNoChange)
	}
	if res.Iterations != 1 {
		t.Errorf("Iterations = %d, want 1 (first Exec already the fixed point)", res.Iterations)
	}
}

// TestOrchestrator_StopOnNoChangeProgressRunsToCap pins the advance: while
// every iteration moves the tip, the knob never fires and the loop exhausts
// MaxIterations exactly as before.
func TestOrchestrator_StopOnNoChangeProgressRunsToCap(t *testing.T) {
	fc := &fakeCommander{stdoutFn: shaSequence("base0001", "head0001", "head0002", "head0003")}
	o := NewOrchestrator(New(testConfig(t), fc))

	res, err := o.Run(context.Background(), OrchestratedRequest{
		RunRequest:     RunRequest{Branch: "agent/x", Prompt: "go"},
		MaxIterations:  3,
		StopOnNoChange: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopMaxIterations {
		t.Errorf("StopReason = %q, want %q (progress every iteration)", res.StopReason, StopMaxIterations)
	}
	if res.Iterations != 3 {
		t.Errorf("Iterations = %d, want 3", res.Iterations)
	}
	if got := fc.countVerb("exec"); got != 3 {
		t.Errorf("exec count = %d, want 3 (full budget)", got)
	}
}

// TestOrchestrator_SignalOutranksNoChange pins the check order: an iteration
// that both prints the sentinel and lands no commit reports StopSignal — the
// prompt-cooperating stop keeps priority over the commit-based one.
func TestOrchestrator_SignalOutranksNoChange(t *testing.T) {
	fc := &fakeCommander{
		stdoutFn: func(c exec.Cmd) string {
			if verbOf(c) == "exec" {
				return "TASK-DONE\n"
			}
			if verbOf(c) == "rev-parse" {
				return "base0001\n" // tip never moves
			}
			return ""
		},
	}
	o := NewOrchestrator(New(testConfig(t), fc))

	res, err := o.Run(context.Background(), OrchestratedRequest{
		RunRequest:       RunRequest{Branch: "agent/x", Prompt: "go"},
		MaxIterations:    5,
		CompletionSignal: "TASK-DONE",
		StopOnNoChange:   true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopSignal {
		t.Errorf("StopReason = %q, want %q (signal outranks no-change)", res.StopReason, StopSignal)
	}
	if res.Iterations != 1 {
		t.Errorf("Iterations = %d, want 1", res.Iterations)
	}
}

// TestOrchestrator_NoChangeKnobOffUnchangedBehavior pins the opt-in: with
// StopOnNoChange false a repeating tip never stops the loop — behavior is
// byte-identical to before the knob existed.
func TestOrchestrator_NoChangeKnobOffUnchangedBehavior(t *testing.T) {
	fc := &fakeCommander{stdoutFn: shaSequence("base0001", "base0001", "base0001", "base0001")}
	o := NewOrchestrator(New(testConfig(t), fc))

	res, err := o.Run(context.Background(), OrchestratedRequest{
		RunRequest:    RunRequest{Branch: "agent/x", Prompt: "go"},
		MaxIterations: 3,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopMaxIterations {
		t.Errorf("StopReason = %q, want %q (knob off: no early stop)", res.StopReason, StopMaxIterations)
	}
	if got := fc.countVerb("exec"); got != 3 {
		t.Errorf("exec count = %d, want 3 (knob off: full budget)", got)
	}
}

// TestOrchestrator_NoChangeStopRunsExtractor pins that the StopNoChange path
// funnels through extract like both existing stop paths: a configured
// ResultExtractor still decodes the final iteration's output.
func TestOrchestrator_NoChangeStopRunsExtractor(t *testing.T) {
	shas := shaSequence("base0001", "base0001")
	fc := &fakeCommander{
		stdoutFn: func(c exec.Cmd) string {
			if verbOf(c) == "exec" {
				return "<result>{\"summary\":\"stalled\",\"score\":1}</result>\n"
			}
			return shas(c)
		},
	}
	o := NewOrchestrator(New(testConfig(t), fc))

	res, err := o.Run(context.Background(), OrchestratedRequest{
		RunRequest:      RunRequest{Branch: "agent/x", Prompt: "go"},
		MaxIterations:   3,
		StopOnNoChange:  true,
		ResultExtractor: result.JSONResult[review](),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != StopNoChange {
		t.Fatalf("StopReason = %q, want %q", res.StopReason, StopNoChange)
	}
	rv, ok := res.Result.(review)
	if !ok {
		t.Fatalf("res.Result = %T, want review (extractor must run on the no-change stop)", res.Result)
	}
	if rv.Summary != "stalled" || rv.Score != 1 {
		t.Errorf("res.Result = %+v, want {stalled 1}", rv)
	}
}

// The artifact-reading capability flows through the OrchestratedResult embed:
// Setup populates RunResult.handle, the orchestrator copies that RunResult into
// OrchestratedResult, so the final result carries a non-nil handle rooted at the
// run's worktree. This guards against a future refactor that constructs the
// result without carrying the handle through.
func TestOrchestrator_ResultCarriesHandle(t *testing.T) {
	fc := &fakeCommander{} // info succeeds -> workshop present, Setup completes
	cfg := testConfig(t)
	o := NewOrchestrator(New(cfg, fc))

	res, err := o.Run(context.Background(), OrchestratedRequest{
		RunRequest: RunRequest{Branch: "agent/x", Prompt: "go"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.handle == nil {
		t.Fatal("res.handle = nil, want a non-nil worktree handle propagated from Setup")
	}
	wantWt := filepath.Join(cfg.ProjectDir, "worktrees", "agent-x")
	if res.handle.worktreePath != wantWt {
		t.Errorf("res.handle.worktreePath = %q, want %q (handle must point at the run's worktree)",
			res.handle.worktreePath, wantWt)
	}
}

// End-to-end: the embedded Artifact method is reachable on an OrchestratedResult
// and reads from the run's worktree root. The fake's `git worktree add` is a
// no-op, so the test materializes the worktree dir + file at the handle's
// worktree path itself, then reads it back through res.Artifact.
func TestOrchestrator_ArtifactReadsThroughEmbed(t *testing.T) {
	fc := &fakeCommander{}
	o := NewOrchestrator(New(testConfig(t), fc))

	res, err := o.Run(context.Background(), OrchestratedRequest{
		RunRequest: RunRequest{Branch: "agent/x", Prompt: "go"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := os.MkdirAll(res.handle.worktreePath, 0o750); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	want := "hello from the worktree\n"
	if err := os.WriteFile(filepath.Join(res.handle.worktreePath, "out.txt"), []byte(want), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	got, err := res.Artifact("out.txt")
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	if got != want {
		t.Errorf("Artifact = %q, want %q", got, want)
	}
}
