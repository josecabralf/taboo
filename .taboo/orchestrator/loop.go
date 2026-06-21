// loop.go implements the "loop" subcommand: the master orchestrator that drains
// the backlog of ready-for-agent issues by repeatedly planning a parallel-safe
// batch and fanning the implement workflow out across it, bounded-parallel,
// through taboo.Pool. Where "implement" drives one issue, "loop" drives the whole
// queue to empty.
//
// Each iteration ("wave") asks the planner for the next batch of ready,
// parallel-safe issues, resolves an implement Plan per issue (without running it),
// claims every issue, then hands the batch of RunRequests to the pool to run at
// once. A wave's RunResults feed the per-issue outcome handling.
//
// The label state machine an issue moves through is what makes the drain
// terminate. At claim time the loop removes ready-for-agent and adds
// agent:in-progress; on success it removes agent:in-progress, and on failure it
// also adds agent:blocked plus a diagnostic comment before releasing in-progress.
// Claiming takes an issue out of the next wave's candidate set two independent
// ways: removing ready-for-agent drops it from the planner's
// ListOpenIssuesByLabel(ready-for-agent) listing, and adding agent:in-progress
// makes selectBatch's in-progress filter exclude it. Either alone would suffice;
// together a claimed issue is never re-selected and the queue strictly shrinks.
// This mirrors agent-implement.yml, which removes its own trigger label the
// moment it picks an issue up.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"

	"afk/internal/ghio"

	taboo "github.com/josecabralf/taboo/pkg"
)

// blockedLabel is the label the loop applies to an issue whose implement run
// failed, taking it out of the ready pool until a human intervenes (see runWave's
// failure branch, which adds it alongside a diagnostic comment).
const blockedLabel = "agent:blocked"

const (
	// Default max iterations bounds how many waves the loop runs before giving up,
	// so a planner that keeps returning work (or a stuck issue that never clears
	// its ready label) cannot spin forever.
	defaultLoopMaxIterations = 10
	// Default parallelism bounds how many implement runs a single wave fans out at
	// once through taboo.Pool.
	defaultLoopParallelism = 3
)

// loopGH is the subset of GitHub I/O the loop drives: it reads an issue to build
// the implement prompt and moves the issue through its label state machine.
// *ghio.Client satisfies it; tests substitute a fake to record label/comment ops
// without shelling out.
type loopGH interface {
	IssueView(ctx context.Context, number int) (ghio.Issue, error)
	AddIssueLabel(ctx context.Context, number int, label string) error
	RemoveIssueLabel(ctx context.Context, number int, label string) error
	CommentIssue(ctx context.Context, number int, body string) error
}

// batchPlanner returns the next parallel-safe batch of ready issues. The plan
// sequence's selectBatch satisfies it; tests substitute a fake returning canned
// batches that drain to empty so the loop terminates.
type batchPlanner func(ctx context.Context, startDir string) ([]planItem, error)

// planResolver resolves a named workflow into an inspectable Plan WITHOUT running
// it, so the loop can collect one RunRequest per issue and hand the whole batch
// to the pool at once. A thin taboo Plan-resolution wrapper satisfies it; tests
// substitute a fake that returns a canned Plan.
type planResolver func(startDir, workflow string, vars map[string]string, ov taboo.PlanOverrides) (*taboo.Plan, error)

// poolRunner fans a batch of RunRequests across a bounded pool, returning one
// RunResult per request with each run's failure recorded on RunResult.Err so one
// failed run does not abort the wave. The taboo.Pool fan-out satisfies it; tests
// substitute a fake returning canned results.
type poolRunner func(ctx context.Context, cfg taboo.Config, limit int, cmd taboo.Commander, reqs []taboo.RunRequest) ([]taboo.RunResult, error)

// loopOptions are the loop subcommand's knobs: how many waves to run, how wide to
// fan each wave out, and whether to only plan-and-print without running anything.
type loopOptions struct {
	maxIterations int
	parallelism   int
	dryRun        bool
}

// loop is the testable core of the loop subcommand: it drains the backlog wave by
// wave. Each iteration plans the next parallel-safe batch; an empty batch means
// the queue is drained, so it logs and returns. Otherwise it runs the wave and
// repeats, up to opts.maxIterations (a safety bound against a never-emptying
// queue). The gh, planBatch, resolve and runPool seams are injected so tests
// drive the full sequence with fakes.
func loop(ctx context.Context, startDir string, opts loopOptions, out io.Writer, gh loopGH, planBatch batchPlanner, resolve planResolver, runPool poolRunner) error {
	// Dry run: plan the first batch and print it without claiming, running, or
	// touching any label, so an operator can preview the wave the loop would drive.
	// TODO(next): also preview the resolved RunRequests, not just the planned batch.
	if opts.dryRun {
		batch, err := planBatch(ctx, startDir)
		if err != nil {
			return fmt.Errorf("plan batch: %w", err)
		}
		return printBatch(out, batch)
	}

	for iter := 0; iter < opts.maxIterations; iter++ {
		batch, err := planBatch(ctx, startDir)
		if err != nil {
			return fmt.Errorf("plan batch: %w", err)
		}
		if len(batch) == 0 {
			fmt.Fprintln(os.Stderr, "afk: no ready issues remain; loop done")
			return nil
		}
		if err := runWave(ctx, startDir, opts.parallelism, gh, resolve, runPool, batch); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "afk: reached max iterations (%d); stopping\n", opts.maxIterations)
	return nil
}

// printBatch writes a planned batch to out as a JSON array (an empty batch prints
// as []). It is the dry-run sink: the same shape the plan subcommand emits, so a
// preview is consumable by the same tooling.
func printBatch(out io.Writer, batch []planItem) error {
	if batch == nil {
		batch = []planItem{}
	}
	data, err := json.Marshal(batch)
	if err != nil {
		return fmt.Errorf("marshal batch: %w", err)
	}
	if _, err := fmt.Fprintln(out, string(data)); err != nil {
		return fmt.Errorf("write batch: %w", err)
	}
	return nil
}

// runWave runs one wave: resolve an implement Plan per item (collecting one
// RunRequest each and the wave's runner Config), claim every item, then fan the
// requests out across the pool and handle each run's outcome. The batch and
// results are index-aligned (one result per request, in request order), so
// results[i] pairs with batch[i].
func runWave(ctx context.Context, startDir string, limit int, gh loopGH, resolve planResolver, runPool poolRunner, batch []planItem) error {
	cfg, reqs, err := resolveRequests(ctx, startDir, gh, resolve, batch)
	if err != nil {
		return err
	}

	// Claim every item before running so the next wave's planner cannot re-select
	// one that is now in flight.
	for _, item := range batch {
		claimIssue(ctx, gh, item.Number)
	}

	results, err := runPool(ctx, cfg, limit, taboo.NewExecCommander(), reqs)
	if err != nil {
		// The whole batch failed to run, so the runs never executed; unclaim each
		// issue — restore ready-for-agent and drop agent:in-progress — so it returns
		// to the pool for a later wave (or a retrigger) to pick up, rather than being
		// stranded with neither label.
		for _, item := range batch {
			unclaimIssue(ctx, gh, item.Number)
		}
		return fmt.Errorf("run batch: %w", err)
	}

	// results[i] pairs with batch[i]; settle each run's outcome independently so
	// one failure never aborts the rest of the wave.
	for i, item := range batch {
		settleResult(ctx, gh, item, results[i])
	}
	return nil
}

// resolveRequests fetches each item's issue, builds its implement vars, and
// resolves an implement Plan per item WITHOUT running it, returning one RunRequest
// per item plus the wave's runner Config. Every item resolves the same workflow
// against the same project, so the Config is identical across the batch and is
// taken from the first item. A fetch or resolve failure aborts the wave — it is a
// setup error, not a per-run failure — before any issue is claimed.
func resolveRequests(ctx context.Context, startDir string, gh loopGH, resolve planResolver, batch []planItem) (taboo.Config, []taboo.RunRequest, error) {
	var cfg taboo.Config
	reqs := make([]taboo.RunRequest, 0, len(batch))
	for i, item := range batch {
		iss, err := gh.IssueView(ctx, item.Number)
		if err != nil {
			return taboo.Config{}, nil, fmt.Errorf("fetch issue #%d: %w", item.Number, err)
		}

		vars := map[string]string{
			"ISSUE_NUMBER":     strconv.Itoa(item.Number),
			"ISSUE_TITLE":      iss.Title,
			"ISSUE_BODY":       iss.Body,
			"PLAN_OUTPUT_PATH": planFile,
		}

		p, err := resolve(startDir, "implement", vars, taboo.PlanOverrides{
			Branch: item.Branch,
			Stdout: os.Stderr,
			Stderr: os.Stderr,
		})
		if err != nil {
			return taboo.Config{}, nil, fmt.Errorf("resolve implement for #%d: %w", item.Number, err)
		}
		if i == 0 {
			cfg = p.Config
		}
		reqs = append(reqs, p.Request.RunRequest)
	}
	return cfg, reqs, nil
}

// settleResult records one run's outcome and releases its in-progress claim. A
// run's failure is recorded on res.Err (the pool never aborts the wave for it):
// a failed run gets agent:blocked plus a diagnostic comment, a successful one is
// just logged. The label/comment ops are best-effort — a failing one is logged,
// not propagated, so it cannot strand the rest of the batch — and the in-progress
// claim is always released afterward.
func settleResult(ctx context.Context, gh loopGH, item planItem, res taboo.RunResult) {
	if res.Err != nil {
		fmt.Fprintf(os.Stderr, "afk: implement run failed for #%d: %v\n", item.Number, res.Err)
		if err := gh.AddIssueLabel(ctx, item.Number, blockedLabel); err != nil {
			fmt.Fprintf(os.Stderr, "afk: add blocked label on #%d: %v\n", item.Number, err)
		}
		if err := gh.CommentIssue(ctx, item.Number, blockedComment(item, res.Err)); err != nil {
			fmt.Fprintf(os.Stderr, "afk: comment blocked on #%d: %v\n", item.Number, err)
		}
	} else {
		fmt.Fprintf(os.Stderr, "afk: implemented #%d on %s\n", item.Number, item.Branch)
	}
	releaseInProgress(ctx, gh, item.Number)
}

// releaseInProgress removes the agent:in-progress label from an issue,
// best-effort: a failure is logged but never propagated, since a label cleanup
// must not decide the wave's outcome.
func releaseInProgress(ctx context.Context, gh loopGH, number int) {
	if err := gh.RemoveIssueLabel(ctx, number, inProgressLabel); err != nil {
		fmt.Fprintf(os.Stderr, "afk: release in-progress on #%d: %v\n", number, err)
	}
}

// unclaimIssue reverses a claim, returning the issue to the ready pool: it
// restores ready-for-agent and drops agent:in-progress, best-effort (each op
// logged, not propagated). The loop calls it when the whole wave fails to run —
// the runs never executed — so the issues land back where claimIssue found them,
// for a later wave or a retrigger to pick up, instead of being stranded with
// neither label.
func unclaimIssue(ctx context.Context, gh loopGH, number int) {
	if err := gh.AddIssueLabel(ctx, number, readyLabel); err != nil {
		fmt.Fprintf(os.Stderr, "afk: restore ready label on #%d: %v\n", number, err)
	}
	releaseInProgress(ctx, gh, number)
}

// blockedComment composes the explanatory comment the loop posts when an
// implement run fails, naming the issue, the error, and how to retry.
func blockedComment(item planItem, runErr error) string {
	return fmt.Sprintf("The implement run failed for issue #%d.\n\nError:\n\n```\n%v\n```\n\nRe-add the `%s` label to retry.", item.Number, runErr, readyLabel)
}

// claimIssue moves an issue into the in-progress state, best-effort: it logs any
// failure to stderr but never aborts the wave, since a label op failing should not
// strand a run that is about to start. It removes the ready label and adds
// in-progress; each independently keeps a later wave's planner from re-selecting
// the issue (selectBatch lists ready-for-agent issues and then excludes any that
// are in-progress), which is what lets the drain terminate. This mirrors
// agent-implement.yml removing its trigger label the moment it claims an issue.
func claimIssue(ctx context.Context, gh loopGH, number int) {
	if err := gh.RemoveIssueLabel(ctx, number, readyLabel); err != nil {
		fmt.Fprintf(os.Stderr, "afk: remove ready label on #%d: %v\n", number, err)
	}
	if err := gh.AddIssueLabel(ctx, number, inProgressLabel); err != nil {
		fmt.Fprintf(os.Stderr, "afk: add in-progress label on #%d: %v\n", number, err)
	}
}
