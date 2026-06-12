package taboo

import "context"

// StopReason explains why an orchestrated run's iteration loop ended.
type StopReason string

const (
	// StopMaxIterations means the loop exhausted RunRequest.MaxIterations
	// without seeing the completion signal.
	StopMaxIterations StopReason = "max-iterations"
	// StopSignal means the agent emitted the completion signal and the loop
	// stopped early.
	StopSignal StopReason = "signal"
)

// OrchestratedResult reports the outcome of a looped run: the final iteration's
// RunResult plus the loop's own bookkeeping.
type OrchestratedResult struct {
	RunResult
	// Iterations is how many times the agent was run.
	Iterations int
	// StopReason explains why the loop ended.
	StopReason StopReason
}

// Orchestrator composes a Runner into an iteration loop. Runner.Run remains the
// single-iteration primitive; the Orchestrator re-runs it up to
// RunRequest.MaxIterations, teeing each run's stdout through a SignalScanner so
// it can stop early when the agent emits its completion signal.
type Orchestrator struct {
	runner *Runner
}

// NewOrchestrator returns an Orchestrator that drives runner.
func NewOrchestrator(runner *Runner) *Orchestrator {
	return &Orchestrator{runner: runner}
}

// Run executes the agent up to req.MaxIterations times, stopping early once the
// completion signal appears in the agent's stdout.
func (o *Orchestrator) Run(ctx context.Context, req RunRequest) (OrchestratedResult, error) {
	maxIter := req.MaxIterations
	if maxIter < 1 {
		maxIter = 1
	}

	var res OrchestratedResult
	for i := 0; i < maxIter; i++ {
		scanner := NewSignalScanner(req.CompletionSignal, req.Stdout)
		iterReq := req
		iterReq.Stdout = scanner

		rr, err := o.runner.Run(ctx, iterReq)
		res.RunResult = rr
		res.Iterations = i + 1
		if err != nil {
			return res, err
		}
		if scanner.Found() {
			res.StopReason = StopSignal
			return res, nil
		}
	}

	res.StopReason = StopMaxIterations
	return res, nil
}
