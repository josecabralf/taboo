package taboo

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
)

// argAfter returns the argument immediately following tok, or "" if tok is the
// last/absent argument.
func argAfter(args []string, tok string) string {
	for i, a := range args {
		if a == tok && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// projectOf returns the --project directory of a workshop Cmd.
func projectOf(c Cmd) (string, bool) {
	if c.Name != "workshop" {
		return "", false
	}
	if p := argAfter(c.Args, "--project"); p != "" {
		return p, true
	}
	return "", false
}

// projectWorkshop extracts the (project, workshop) identity a workshop Cmd acts
// on: the project is the --project dir, the workshop is parsed from the verb's
// operand. It returns empties for git calls. Used to key per-slot assertions.
func projectWorkshop(c Cmd) (project, workshop string) {
	project, _ = projectOf(c)
	switch verbOf(c) {
	case "info", "launch", "stop", "start":
		workshop = argAfter(c.Args, verbOf(c))
	case "remount":
		workshop, _, _ = strings.Cut(argAfter(c.Args, "remount"), "/")
	case "exec":
		for i, a := range c.Args {
			if a == "--" && i > 0 {
				workshop = c.Args[i-1]
				break
			}
		}
	}
	return project, workshop
}

// remountWorkshops returns the distinct workshop names seen across every
// `remount <ws>/<sdk>:<plug> <src>` call, in first-seen order. Every run issues
// remounts, so this is a reliable census of which workshops the pool drove.
func (f *fakeCommander) remountWorkshops(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	var out []string
	for _, c := range f.snapshot() {
		if verbOf(c) != "remount" {
			continue
		}
		ws, _, ok := strings.Cut(argAfter(c.Args, "remount"), "/")
		if !ok {
			t.Fatalf("malformed remount target in %v", c.Args)
		}
		if !seen[ws] {
			seen[ws] = true
			out = append(out, ws)
		}
	}
	return out
}

// poolRequests builds n requests with distinct branches and prompts so each run
// is individually identifiable in the recorded call sequence.
func poolRequests(n int) []RunRequest {
	reqs := make([]RunRequest, n)
	for i := range reqs {
		reqs[i] = RunRequest{Branch: fmt.Sprintf("agent/%d", i), Prompt: fmt.Sprintf("task-%d", i)}
	}
	return reqs
}

// assertInputOrder checks results[i] corresponds to reqs[i] by branch.
func assertInputOrder(t *testing.T, results []RunResult, reqs []RunRequest) {
	t.Helper()
	if len(results) != len(reqs) {
		t.Fatalf("len(results) = %d, want %d", len(results), len(reqs))
	}
	for i := range reqs {
		if results[i].Branch != reqs[i].Branch {
			t.Errorf("results[%d].Branch = %q, want %q (results must be in input order)", i, results[i].Branch, reqs[i].Branch)
		}
	}
}

func TestPool_FansOutDistinctWorkshopsAndWorktrees(t *testing.T) {
	const n = 4
	// Gate exec and hold every run in flight at once: with instant fake commands
	// a single fast worker would otherwise drain the queue, so forcing genuine
	// concurrency is what makes "each concurrent run in its own workshop" observable.
	fc := &fakeCommander{
		gateVerb: "exec",
		gate:     make(chan struct{}),
		entered:  make(chan struct{}, n),
	}
	cfg := testConfig(t)
	p := NewPool(cfg, n, fc) // limit == n: every request can run at once

	reqs := poolRequests(n)
	done := make(chan []RunResult, 1)
	go func() {
		res, _ := p.Run(context.Background(), reqs)
		done <- res
	}()
	for i := 0; i < n; i++ { // every run reaches exec, each in its own slot
		<-fc.entered
	}
	for i := 0; i < n; i++ {
		fc.gate <- struct{}{}
	}
	results := <-done

	assertInputOrder(t, results, reqs)
	for i, res := range results {
		if res.Err != nil {
			t.Errorf("results[%d].Err = %v, want nil", i, res.Err)
		}
	}

	// N distinct, deterministically-named workshops drove the batch.
	if ws := fc.remountWorkshops(t); len(ws) != n {
		t.Errorf("distinct workshops = %v, want %d", ws, n)
	}

	// Each request ran in its own slot's project dir, so N distinct --project
	// dirs appear in the call stream.
	projs := map[string]bool{}
	for _, c := range fc.snapshot() {
		if pd, ok := projectOf(c); ok {
			projs[pd] = true
		}
	}
	if len(projs) != n {
		t.Errorf("distinct project dirs = %d, want %d", len(projs), n)
	}

	// Per-run isolation: every result has a unique worktree path, and that path
	// is the source of exactly one worktree-add and one workspace remount — never
	// shared with another run.
	for _, res := range results {
		if res.WorktreePath == "" {
			t.Fatalf("result for %q has empty worktree path", res.Branch)
		}
		adds, mounts := 0, 0
		for _, c := range fc.snapshot() {
			if _, ok := worktreeAddBranch(c); ok && slices.Contains(c.Args, res.WorktreePath) {
				adds++
			}
			if verbOf(c) == "remount" && strings.HasSuffix(argAfter(c.Args, "remount"), ":workspace") &&
				slices.Contains(c.Args, res.WorktreePath) {
				mounts++
			}
		}
		if adds != 1 {
			t.Errorf("worktree %q added %d times, want exactly 1", res.WorktreePath, adds)
		}
		if mounts != 1 {
			t.Errorf("worktree %q workspace-remounted %d times, want exactly 1", res.WorktreePath, mounts)
		}
	}
}

func TestPool_BoundsConcurrency(t *testing.T) {
	const limit, n = 2, 5
	fc := &fakeCommander{
		gateVerb: "exec",
		gate:     make(chan struct{}),
		entered:  make(chan struct{}, n),
	}
	p := NewPool(testConfig(t), limit, fc)
	reqs := poolRequests(n)

	done := make(chan []RunResult, 1)
	go func() {
		res, _ := p.Run(context.Background(), reqs)
		done <- res
	}()

	// Phase 1: exactly `limit` agent execs park at the gate at once.
	for i := 0; i < limit; i++ {
		<-fc.entered
	}
	if got := fc.inflight.Load(); got != int32(limit) {
		t.Fatalf("inflight after %d entered = %d, want %d", limit, got, limit)
	}

	// Phase 2: release one parked exec and wait for the freed worker to re-park
	// on its next request. Concurrency must hold steady at `limit` — never a
	// (limit+1)th — at each synchronized step. No timeouts.
	for i := 0; i < n-limit; i++ {
		fc.gate <- struct{}{}
		<-fc.entered
		if got := fc.inflight.Load(); got != int32(limit) {
			t.Fatalf("inflight at step %d = %d, want %d", i, got, limit)
		}
	}

	// Phase 3: drain the final parked execs.
	for i := 0; i < limit; i++ {
		fc.gate <- struct{}{}
	}

	results := <-done
	assertInputOrder(t, results, reqs)
	if peak := fc.peak.Load(); peak != int32(limit) {
		t.Errorf("peak concurrency = %d, want %d", peak, limit)
	}
	if got := fc.countVerb("exec"); got != n {
		t.Errorf("exec count = %d, want %d (one per request)", got, n)
	}
}

func TestPool_PerRunErrorDoesNotAbortBatch(t *testing.T) {
	const n = 4
	const failPrompt = "task-2"
	fc := &fakeCommander{
		errFn: func(c Cmd) error {
			if verbOf(c) == "exec" && slices.Contains(c.Args, failPrompt) {
				return fmt.Errorf("agent boom")
			}
			return nil
		},
	}
	p := NewPool(testConfig(t), n, fc)
	reqs := poolRequests(n)

	results, err := p.Run(context.Background(), reqs)
	if err != nil {
		t.Fatalf("batch error = %v, want nil (a per-run failure must not abort the batch)", err)
	}
	assertInputOrder(t, results, reqs)
	for i, res := range results {
		if i == 2 {
			if res.Err == nil {
				t.Errorf("results[2].Err = nil, want the failing run's error")
			}
		} else if res.Err != nil {
			t.Errorf("results[%d].Err = %v, want nil", i, res.Err)
		}
	}
}

func TestPool_AllRunsFail(t *testing.T) {
	const n = 3
	fc := &fakeCommander{errFn: failOnVerb("exec")}
	p := NewPool(testConfig(t), n, fc)
	reqs := poolRequests(n)

	results, err := p.Run(context.Background(), reqs)
	if err != nil {
		t.Fatalf("batch error = %v, want nil", err)
	}
	assertInputOrder(t, results, reqs)
	for i, res := range results {
		if res.Err == nil {
			t.Errorf("results[%d].Err = nil, want a failure", i)
		}
	}
}

func TestPool_ReusesWorkshopAcrossWaves(t *testing.T) {
	const limit, n = 2, 4 // two waves of two
	// Model the real workshop lifecycle: `info` fails for a (project, workshop)
	// until it has been launched, then succeeds (reuse). Keyed per slot so reuse
	// is asserted per workshop, not globally.
	var mu sync.Mutex
	launched := map[string]bool{}
	fc := &fakeCommander{
		gateVerb: "exec",
		gate:     make(chan struct{}),
		entered:  make(chan struct{}, n),
		errFn: func(c Cmd) error {
			proj, ws := projectWorkshop(c)
			key := proj + "\x00" + ws
			mu.Lock()
			defer mu.Unlock()
			switch verbOf(c) {
			case "launch":
				launched[key] = true
			case "info":
				if !launched[key] {
					return fmt.Errorf("no such workshop %q", ws)
				}
			}
			return nil
		},
	}
	p := NewPool(testConfig(t), limit, fc)
	reqs := poolRequests(n)

	done := make(chan []RunResult, 1)
	go func() {
		res, _ := p.Run(context.Background(), reqs)
		done <- res
	}()
	// Wave 1: both slots launch and reach exec concurrently. Releasing them lets
	// each worker pull a wave-2 request, whose `info` now succeeds (reuse, no
	// second launch). Driving it in two explicit waves pins per-slot reuse.
	for wave := 0; wave < n/limit; wave++ {
		for i := 0; i < limit; i++ {
			<-fc.entered
		}
		for i := 0; i < limit; i++ {
			fc.gate <- struct{}{}
		}
	}
	results := <-done
	assertInputOrder(t, results, reqs)

	launchByWS := map[string]int{}
	execByWS := map[string]int{}
	for _, c := range fc.snapshot() {
		_, ws := projectWorkshop(c)
		switch verbOf(c) {
		case "launch":
			launchByWS[ws]++
		case "exec":
			execByWS[ws]++
		}
	}
	if len(launchByWS) != limit {
		t.Errorf("launched %d distinct workshops, want %d slots", len(launchByWS), limit)
	}
	for ws, k := range launchByWS {
		if k != 1 {
			t.Errorf("workshop %s launched %d times, want 1 (reused across waves)", ws, k)
		}
	}
	total := 0
	for _, k := range execByWS {
		total += k
	}
	if total != n {
		t.Errorf("total execs = %d, want %d", total, n)
	}
}

func TestPool_SerializesWorktreeAdd(t *testing.T) {
	// All slots share one RepoPath, so the pool must serialize `git worktree
	// add` even when every slot runs concurrently — only one may be in flight.
	const limit, n = 3, 3
	fc := &fakeCommander{
		gateVerb: "worktree",
		gate:     make(chan struct{}),
		entered:  make(chan struct{}, n),
	}
	p := NewPool(testConfig(t), limit, fc)
	reqs := poolRequests(n)

	done := make(chan struct{})
	go func() {
		_, _ = p.Run(context.Background(), reqs)
		close(done)
	}()

	for i := 0; i < n; i++ {
		<-fc.entered
		if got := fc.inflight.Load(); got != 1 {
			t.Fatalf("worktree-add inflight = %d, want 1 (must be serialized)", got)
		}
		fc.gate <- struct{}{}
	}
	<-done
	if peak := fc.peak.Load(); peak != 1 {
		t.Errorf("worktree-add peak concurrency = %d, want 1 (serialized)", peak)
	}
}

func TestPool_EmptyRequestsIssuesNothing(t *testing.T) {
	fc := &fakeCommander{}
	p := NewPool(testConfig(t), 4, fc)

	results, err := p.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("len(results) = %d, want 0", len(results))
	}
	if got := len(fc.snapshot()); got != 0 {
		t.Errorf("issued %d commands for an empty batch, want 0", got)
	}
}

func TestPool_LimitBelowOneDefaultsToSingleSlot(t *testing.T) {
	fc := &fakeCommander{}
	p := NewPool(testConfig(t), 0, fc) // limit < 1 -> 1
	reqs := poolRequests(2)

	results, err := p.Run(context.Background(), reqs)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	assertInputOrder(t, results, reqs)
	if ws := fc.remountWorkshops(t); len(ws) != 1 {
		t.Errorf("distinct workshops = %v, want 1 (single slot)", ws)
	}
}

func TestPool_AlreadyCanceledContext(t *testing.T) {
	fc := &fakeCommander{}
	p := NewPool(testConfig(t), 4, fc)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results, err := p.Run(ctx, poolRequests(3))
	if err == nil {
		t.Error("Run error = nil, want the context error for an already-canceled context")
	}
	if results != nil {
		t.Errorf("results = %v, want nil when the batch never started", results)
	}
	if got := len(fc.snapshot()); got != 0 {
		t.Errorf("issued %d commands for a canceled batch, want 0", got)
	}
}
