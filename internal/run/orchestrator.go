package run

import (
	"context"
	"errors"
	"strings"

	"github.com/josecabralf/taboo/internal/result"
)

// ErrForkLoop is returned by Orchestrator.Run when a forked run is given more
// than one iteration. The loop re-execs the unchanged RunRequest, so Fork would
// re-fork the source session every iteration instead of continuing the fork, and
// taboo cannot yet capture the new session id to resume it across iterations. See
// docs/adr/0003-session-resume-fork-command-contract.md.
var ErrForkLoop = errors.New("taboo: fork cannot be combined with multiple iterations")

// StopReason explains why an orchestrated run's iteration loop ended.
type StopReason string

const (
	// StopMaxIterations means the loop exhausted MaxIterations without the signal.
	StopMaxIterations StopReason = "max-iterations"
	// StopSignal means the agent emitted the completion signal.
	StopSignal StopReason = "signal"
	// StopNoChange means an iteration ended with the branch tip unmoved. It
	// compares branch tips, so uncommitted or untracked changes do not count as
	// progress.
	StopNoChange StopReason = "no-change"
)

// OrchestratedRequest describes a looped run: a single-run RunRequest plus the
// loop's own knobs. The knobs live here rather than on RunRequest so the
// single-run primitive keeps a clean contract.
type OrchestratedRequest struct {
	RunRequest
	// MaxIterations bounds how many times the agent is re-run in the worktree
	// (zero or negative = a single run).
	MaxIterations int
	// CompletionSignal is the sentinel watched for in the agent's stdout to stop
	// the loop early (empty = no early stop).
	CompletionSignal string
	// StopOnNoChange stops the loop early when an iteration produces no new
	// commit. Off by default: a loop whose work product is output rather than
	// commits would otherwise stop after one iteration.
	StopOnNoChange bool
	// ResultExtractor, if set, parses a typed result from the final iteration's
	// output once the loop ends (nil = skip).
	ResultExtractor result.ResultExtractor
}

// OrchestratedResult reports the outcome of a looped run.
type OrchestratedResult struct {
	RunResult
	// Iterations is how many times the agent was run.
	Iterations int
	// StopReason explains why the loop ended. It is only meaningful when Run
	// returns a nil error; a Setup/Exec failure leaves it at its zero value.
	StopReason StopReason
	// Result is the value decoded by req.ResultExtractor from the final output,
	// or nil if no extractor was configured. Callers type-assert it to their
	// result type.
	Result any
}

// Orchestrator composes a Runner into an iteration loop.
type Orchestrator struct {
	runner *Runner
}

// NewOrchestrator returns an Orchestrator that drives runner.
func NewOrchestrator(runner *Runner) *Orchestrator {
	return &Orchestrator{runner: runner}
}

// Run prepares the workspace once, then re-execs the agent up to
// req.MaxIterations times in it, stopping early on the completion signal or,
// with req.StopOnNoChange, on an iteration that produces no new commit. On a
// Setup or Exec failure it returns the result so far alongside the error.
func (o *Orchestrator) Run(ctx context.Context, req OrchestratedRequest) (OrchestratedResult, error) {
	maxIter := req.MaxIterations
	if maxIter < 1 {
		maxIter = 1
	}
	// A looped fork would re-fork the source session each iteration, not continue
	// it, so reject it before the expensive Setup. See ErrForkLoop.
	if req.Fork && maxIter > 1 {
		return OrchestratedResult{}, ErrForkLoop
	}

	base, err := o.runner.Setup(ctx, req.RunRequest)
	if err != nil {
		return OrchestratedResult{RunResult: base}, err
	}

	res := OrchestratedResult{RunResult: base}
	// prev tracks the branch tip going into each iteration, seeded from Setup's
	// base capture, so the no-change check needs no git commands of its own.
	prev := base.BaseCommit
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
		// The signal check keeps priority: an iteration that both prints the
		// sentinel and lands no commit reports StopSignal.
		if req.StopOnNoChange && rr.Commit == prev {
			res.StopReason = StopNoChange
			return o.extract(req, res)
		}
		prev = rr.Commit
	}

	res.StopReason = StopMaxIterations
	return o.extract(req, res)
}

// extract runs req.ResultExtractor once over the final output and records the
// typed value on res.Result, the single post-loop step shared by all stop paths.
// On extraction failure res stays fully populated (the commit is never discarded)
// and the error is returned alongside it.
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
