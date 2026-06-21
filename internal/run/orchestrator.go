package run

import (
	"context"
	"errors"
	"strings"

	"github.com/josecabralf/taboo/internal/result"
)

// ErrForkLoop is returned by Orchestrator.Run when a forked run is given more
// than one iteration. The loop re-execs the unchanged RunRequest, so Fork would
// re-fork the source session on every iteration instead of continuing the fork —
// and taboo cannot yet capture the new session id to resume it across iterations
// (session-id capture is out of scope; see
// docs/adr/0003-session-resume-fork-command-contract.md). A single-iteration
// fork, or a multi-iteration plain resume, is allowed.
var ErrForkLoop = errors.New("taboo: fork cannot be combined with multiple iterations")

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
	// ResultExtractor, if set, parses a typed result from the final iteration's
	// output once the loop ends (nil = skip; OrchestratedResult.Result stays nil).
	ResultExtractor result.ResultExtractor
}

// OrchestratedResult reports the outcome of a looped run: the final iteration's
// RunResult plus the loop's own bookkeeping. Because every iteration shares one
// worktree and the agent commits in place, the final Commit is the branch HEAD
// after the last iteration.
type OrchestratedResult struct {
	RunResult
	// Iterations is how many times the agent was run.
	Iterations int
	// StopReason explains why the loop ended. It is only meaningful when Run
	// returns a nil error; on a Setup/Exec failure Run returns early and leaves
	// StopReason at its zero value.
	StopReason StopReason
	// Result is the value decoded by req.ResultExtractor from the final output,
	// or nil if no extractor was configured. Callers type-assert it to their
	// result type (e.g. res.Result.(MyResult)).
	Result any
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
// completion signal appears in the agent's stdout. On a Setup or Exec failure it
// returns the populated result so far alongside the error, with StopReason left
// at its zero value; StopReason is only meaningful when the returned error is nil.
func (o *Orchestrator) Run(ctx context.Context, req OrchestratedRequest) (OrchestratedResult, error) {
	maxIter := req.MaxIterations
	if maxIter < 1 {
		maxIter = 1
	}
	// A looped fork would re-fork the source session each iteration (every Exec
	// rebuilds CommandOptions from the same req), not continue the fork, so reject
	// it up front before the expensive Setup. See ErrForkLoop.
	if req.Fork && maxIter > 1 {
		return OrchestratedResult{}, ErrForkLoop
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
			return o.extract(req, res)
		}
	}

	res.StopReason = StopMaxIterations
	return o.extract(req, res)
}

// extract runs req.ResultExtractor once over the final iteration's output and
// records the typed value on res.Result. It is the single post-loop step shared
// by both stop paths. On extraction failure res stays fully populated (the
// agent's commit is never discarded) and the wrapped sentinel error is
// returned alongside it.
func (o *Orchestrator) extract(req OrchestratedRequest, res OrchestratedResult) (OrchestratedResult, error) {
	if req.ResultExtractor == nil {
		return res, nil
	}
	extracted, err := req.ResultExtractor.Extract(res.Output)
	if err != nil {
		return res, err
	}
	res.Result = extracted
	return res, nil
}
