package taboo

import (
	"context"
	"strings"
)

// StopReason explains why an orchestrated run's iteration loop ended.
type StopReason string

const (
	// StopMaxIterations means the loop exhausted OrchestratedRequest.MaxIterations
	// without seeing the completion signal.
	StopMaxIterations StopReason = "max-iterations"
	// StopSignal means the agent emitted the completion signal and the loop
	// stopped early.
	StopSignal StopReason = "signal"
)

// OrchestratedRequest describes a looped run: a single-run RunRequest plus the
// loop's own knobs. These knobs live here rather than on RunRequest so the
// single-run primitive (Runner.Run) keeps a clean contract.
type OrchestratedRequest struct {
	RunRequest
	// MaxIterations bounds how many times the agent is re-run in the worktree
	// (zero or negative = a single run).
	MaxIterations int
	// CompletionSignal is the sentinel watched for in the agent's stdout to stop
	// the loop early (empty = no early stop).
	CompletionSignal string
}

// OrchestratedResult reports the outcome of a looped run: the final iteration's
// RunResult plus the loop's own bookkeeping. Because every iteration shares one
// worktree and the agent commits in place, the final Commit is the branch HEAD
// after the last iteration.
type OrchestratedResult struct {
	RunResult
	// Iterations is how many times the agent was run.
	Iterations int
	// StopReason explains why the loop ended.
	StopReason StopReason
}

// Orchestrator composes a Runner into an iteration loop. It prepares the
// worktree once via Runner.Setup, then re-runs the agent with Runner.Exec up to
// MaxIterations, stopping early once the completion signal appears in the
// agent's stdout.
type Orchestrator struct {
	runner *Runner
}

// NewOrchestrator returns an Orchestrator that drives runner.
func NewOrchestrator(runner *Runner) *Orchestrator {
	return &Orchestrator{runner: runner}
}

// Run prepares the worktree once, then re-execs the agent up to
// req.MaxIterations times in that same worktree, stopping early once the
// completion signal appears in the agent's stdout.
func (o *Orchestrator) Run(ctx context.Context, req OrchestratedRequest) (OrchestratedResult, error) {
	maxIter := req.MaxIterations
	if maxIter < 1 {
		maxIter = 1
	}

	base, err := o.runner.Setup(ctx, req.RunRequest)
	if err != nil {
		return OrchestratedResult{RunResult: base}, err
	}

	res := OrchestratedResult{RunResult: base}
	for i := 0; i < maxIter; i++ {
		rr, err := o.runner.Exec(ctx, req.RunRequest, base)
		res.RunResult = rr
		res.Iterations = i + 1
		if err != nil {
			return res, err
		}
		if req.CompletionSignal != "" && strings.Contains(rr.Output, req.CompletionSignal) {
			res.StopReason = StopSignal
			return res, nil
		}
	}

	res.StopReason = StopMaxIterations
	return res, nil
}
