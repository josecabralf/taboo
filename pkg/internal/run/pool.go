package run

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/josecabralf/taboo/pkg/internal/exec"
	"github.com/josecabralf/taboo/pkg/internal/workshop"
)

// Pool fans multiple agent runs out across a bounded set of workshops and
// aggregates their results.
//
// Each concurrency slot owns a distinct, deterministically-named workshop
// ("<Workshop>-<slot>") under its own project directory ("<ProjectDir>/slot-<slot>"),
// so a slot's rendered definition, worktrees, and session store never collide
// with another slot's. A slot processes its queued requests sequentially,
// reusing its workshop across waves (the launch cost is paid once per slot).
// Every request still gets its own branch and worktree, so concurrent runs never
// touch each other's files — isolation is at the workshop level.
//
// The limit bounds both the number of concurrent workshops and the number of
// worker goroutines: with more requests than the limit, requests queue and run
// in waves. All slots share the base RepoPath (the two-mount rule pins the
// gitcommon mount to the host .git), so Pool serializes `git worktree add`
// across slots; concurrent commits to distinct branches are otherwise safe
// because refs are per-worktree and the object store is append-only. Callers
// MUST NOT run `git gc`/`repack`/`prune` against RepoPath while a Pool run is in
// flight. A single Pool is not safe for concurrent Run calls (overlapping calls
// would collide on the same slot directories and workshop names); serialize them
// per Pool instance. Config.Agent must be safe for concurrent use — the built-in
// profiles are immutable values.
type Pool struct {
	cfg   workshop.Config
	limit int
	cmd   exec.Commander
}

// NewPool returns a Pool that fans runs out across at most limit concurrent
// workshops derived from cfg, driving workshop/git through cmd. A limit below 1
// is treated as 1.
func NewPool(cfg workshop.Config, limit int, cmd exec.Commander) *Pool {
	if limit < 1 {
		limit = 1
	}
	return &Pool{cfg: cfg, limit: limit, cmd: cmd}
}

// slotConfig derives the Config for concurrency slot i: a distinct workshop name
// and an isolated project directory, so the slot's definition, worktrees, and
// sessions never collide with another slot's.
func (p *Pool) slotConfig(slot int) workshop.Config {
	c := p.cfg
	c.Workshop = fmt.Sprintf("%s-%d", p.cfg.Workshop, slot)
	c.ProjectDir = filepath.Join(p.cfg.ProjectDir, fmt.Sprintf("slot-%d", slot))
	return c
}

// Run executes each request concurrently, bounded by the pool's limit, and
// returns one RunResult per request in input order (results[i] corresponds to
// reqs[i]). A request that fails does not abort the batch: its error is recorded
// on the corresponding RunResult.Err and the remaining runs proceed. The
// returned error is non-nil only when the batch cannot be started at all (the
// context is already canceled); per-run failures never surface there.
//
// If ctx is canceled after the batch starts, runs already in flight finish on
// their own (cancellation reaches them through the Commander) and queued runs
// are skipped: each skipped run's RunResult carries its Branch and ctx.Err() on
// RunResult.Err, with no workshop or git commands issued for it. The batch error
// stays nil; cancellation surfaces per run, like any other per-run failure.
func (p *Pool) Run(ctx context.Context, reqs []RunRequest) ([]RunResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(reqs) == 0 {
		return nil, nil
	}

	workers := p.limit
	if workers > len(reqs) {
		workers = len(reqs)
	}

	// Slots share one RepoPath, so serialize the `git worktree add` that mutates
	// it; everything else (workshop swaps, agent exec) still runs concurrently.
	cmd := serialCommander{inner: p.cmd, gitLock: &sync.Mutex{}}

	type job struct {
		idx int
		req RunRequest
	}
	jobs := make(chan job, len(reqs))
	results := make([]RunResult, len(reqs))

	var wg sync.WaitGroup
	for slot := 0; slot < workers; slot++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			r := New(p.slotConfig(slot), cmd)
			for j := range jobs {
				// Skip queued runs once ctx is canceled rather than dispatching
				// commands that would only fail: record the cancellation per run
				// so results stay in input order and the caller can tell skipped
				// runs from completed ones.
				if err := ctx.Err(); err != nil {
					results[j.idx] = RunResult{Branch: j.req.Branch, Err: err}
					continue
				}
				res, err := r.Run(ctx, j.req)
				res.Err = err
				results[j.idx] = res // distinct index per goroutine: race-free
			}
		}(slot)
	}
	for i, req := range reqs {
		jobs <- job{idx: i, req: req}
	}
	close(jobs)
	// Wait before returning so no worker writes results after the slice is handed
	// back to the caller (the property that keeps Run race-free).
	wg.Wait()

	return results, nil
}

// serialCommander wraps a Commander and serializes concurrent `git worktree add`
// invocations behind gitLock. Worktree creation mutates the shared repo's .git
// metadata (refs and the worktrees registry), which is not safe to run from
// several processes at once; every other command passes straight through and may
// run concurrently. Pool uses it so fan-out across slots that share one RepoPath
// stays correct.
type serialCommander struct {
	inner   exec.Commander
	gitLock *sync.Mutex
}

func (s serialCommander) Run(ctx context.Context, c exec.Cmd) error {
	if isWorktreeAdd(c) {
		s.gitLock.Lock()
		defer s.gitLock.Unlock()
	}
	return s.inner.Run(ctx, c)
}
