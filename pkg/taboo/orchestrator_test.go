package taboo

import (
	"context"
	"testing"
)

// countVerb returns how many recorded calls have the given workshop/git verb.
func (f *fakeCommander) countVerb(verb string) int {
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
	cfg.AgentCmd = []string{"opencode", "run"}
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
	cfg.AgentCmd = []string{"opencode", "run"}
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
	cfg.AgentCmd = []string{"opencode", "run"}
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
	cfg.AgentCmd = []string{"opencode", "run"}
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

func TestOrchestrator_StopsOnRunnerError(t *testing.T) {
	// A failing iteration aborts the loop and surfaces the error; no further
	// iterations run.
	fc := &fakeCommander{errFn: failOnVerb("exec")}
	cfg := testConfig(t)
	cfg.AgentCmd = []string{"opencode", "run"}
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
