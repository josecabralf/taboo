//go:build integration

// Benchmark harness for issue #29 acceptance criterion 1: "Benchmark current
// Pool fan-out cold-start cost (baseline numbers recorded)."
//
// This records the WALL-CLOCK cost of a cold Pool fan-out against real workshop
// + LXD, attributed to phases (launch vs the per-run stop/remount/start swap vs
// agent exec) so the warm-clone speedup question (criterion 2) can be answered
// on data rather than guesswork. It is NOT a go `testing.B` benchmark: a single
// launch costs minutes, so b.N looping is meaningless; instead it is a gated
// test that runs the fan-out once and logs a structured report.
//
// Run it on a machine with workshop + LXD installed (never inside the dev
// workshop — a workshop within a workshop is problematic; see the Makefile):
//
//	TABOO_BENCH=1 go test -tags integration ./pkg/taboo/ \
//	    -run TestBenchmark_PoolFanoutColdStart -timeout 60m -v
//
// Knobs (env): TABOO_BENCH_SLOTS (default 4) sets both the request count and the
// concurrency limit, so every slot launches a distinct cold workshop in one wave
// — the worst case the issue cares about. TABOO_BENCH_WARM_HOST=1 documents that
// the host already holds the ubuntu@24.04 image and the `go` SDK volume (the
// realistic steady state a Pool hits; the base/volume download is a one-time host
// tax, shared across slots — see docs/spikes/0001-warm-workshops-fanout.md).
//
// Because each slot has its own project dir (distinct LXD project-id) AND its own
// workshop name, workshop's per-(project,name) SDK snapshot is never shared
// across slots: every slot's first launch re-runs the agent SDK's setup-base
// hook cold. That per-slot cold provision is exactly what a warm ZFS clone would
// eliminate, and what the `launch` row below measures.
package taboo

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"
)

// phaseTimer wraps a Commander and records each workshop/git invocation's verb
// and wall-clock duration. It is the measurement seam: the benchmark attributes
// fan-out cost to phases by summing durations per verb (`launch` = the cold
// provision; `stop`/`remount`/`start` = the per-run swap; `exec` = the agent).
// It is concurrency-safe because Pool fans runs out across goroutines.
type phaseTimer struct {
	inner Commander
	mu    sync.Mutex
	log   []phaseTiming
}

type phaseTiming struct {
	verb string
	dur  time.Duration
}

func (p *phaseTimer) Run(ctx context.Context, c Cmd) error {
	start := time.Now()
	err := p.inner.Run(ctx, c)
	elapsed := time.Since(start)
	p.mu.Lock()
	p.log = append(p.log, phaseTiming{verb: verbOf(c), dur: elapsed})
	p.mu.Unlock()
	return err
}

type stat struct {
	verb           string
	count          int
	total          time.Duration
	min, max, mean time.Duration
}

// byVerb collapses the recorded log into one stat per verb, sorted by total
// descending so the dominant cost sits at the top of the report.
func (p *phaseTimer) byVerb() []stat {
	p.mu.Lock()
	defer p.mu.Unlock()

	agg := map[string]*stat{}
	for _, t := range p.log {
		s := agg[t.verb]
		if s == nil {
			s = &stat{verb: t.verb, min: t.dur, max: t.dur}
			agg[t.verb] = s
		}
		s.count++
		s.total += t.dur
		if t.dur < s.min {
			s.min = t.dur
		}
		if t.dur > s.max {
			s.max = t.dur
		}
	}
	out := make([]stat, 0, len(agg))
	for _, s := range agg {
		s.mean = s.total / time.Duration(s.count)
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].total > out[j].total })
	return out
}

// TestBenchmark_PoolFanoutColdStart fans a cold batch out across N slots and
// reports the baseline cost per phase. Skipped unless TABOO_BENCH=1, so the
// normal integration run does not pay minutes per invocation.
func TestBenchmark_PoolFanoutColdStart(t *testing.T) {
	if os.Getenv("TABOO_BENCH") == "" {
		t.Skip("TABOO_BENCH not set; skipping the cold-start fan-out benchmark")
	}

	slots := 4
	if v := os.Getenv("TABOO_BENCH_SLOTS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("TABOO_BENCH_SLOTS=%q: want a positive integer", v)
		}
		slots = n
	}

	repo := initSeedRepo(t)
	proj := nonTmpDir(t)
	ws := fmt.Sprintf("taboo-bench-%d", os.Getpid())
	// A deterministic shell "agent" (no LLM, no API key) reporting Name()
	// "opencode", so the real opencode agent SDK is still installed cold — that
	// per-slot setup-base install is the provisioning cost we want to measure —
	// while the run itself is a fixed one-commit script, not agent latency.
	cfg := Config{
		Workshop:   ws,
		Base:       "ubuntu@24.04",
		Agent:      scriptProfile{argv: []string{"bash", "-lc"}},
		RepoPath:   repo,
		ProjectDir: proj,
	}
	t.Cleanup(func() { cleanupPoolWorkshops(t, proj, ws, slots) })

	pt := &phaseTimer{inner: NewExecCommander()}
	pool := NewPool(cfg, slots, pt)

	const script = `set -eu
git config user.email bench@example.com
git config user.name bench
echo bench > BENCH.md
git add -A
git commit -qm "bench: add BENCH.md"`
	reqs := make([]RunRequest, slots)
	for i := range reqs {
		reqs[i] = RunRequest{
			Branch:  fmt.Sprintf("bench/%d", i),
			Prompt:  script,
			Timeout: 5 * time.Minute,
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Minute)
	defer cancel()

	wallStart := time.Now()
	results, err := pool.Run(ctx, reqs)
	wall := time.Since(wallStart)
	if err != nil {
		t.Fatalf("Pool.Run: %v", err)
	}
	for i, res := range results {
		if res.Err != nil {
			t.Errorf("slot result %d failed: %v", i, res.Err)
		}
	}

	// Report. The `launch` row is the cold-provision baseline criterion 1 asks
	// for; `stop`+`remount`+`start` should reproduce CONTEXT's ~2s/~3.6s swap.
	warmHost := os.Getenv("TABOO_BENCH_WARM_HOST") != ""
	t.Logf("\n=== Pool fan-out cold-start baseline ===\n"+
		"slots=%d  concurrency-limit=%d  warm-host=%v\n"+
		"wall-clock (whole batch): %s\n",
		slots, slots, warmHost, wall.Round(time.Millisecond))
	t.Logf("%-10s %5s %12s %12s %12s %12s", "phase", "n", "total", "mean", "min", "max")
	for _, s := range pt.byVerb() {
		t.Logf("%-10s %5d %12s %12s %12s %12s",
			s.verb, s.count,
			s.total.Round(time.Millisecond), s.mean.Round(time.Millisecond),
			s.min.Round(time.Millisecond), s.max.Round(time.Millisecond))
	}
	t.Logf("Interpretation: the `launch` mean is the per-slot cold-provision cost a " +
		"warm ZFS clone would replace; multiply (mean_launch - clone_cost) by " +
		"(slots-1) for the warm-clone savings ceiling. See " +
		"docs/spikes/0001-warm-workshops-fanout.md.")
}

// cleanupPoolWorkshops removes the per-slot workshops the benchmark launched and
// prunes the worktrees. Mirrors newIntegrationRunner's cleanup, fanned across the
// slot project dirs Pool.slotConfig derives (<proj>/slot-<i>, <ws>-<i>).
func cleanupPoolWorkshops(t *testing.T, proj, ws string, slots int) {
	t.Helper()
	for i := 0; i < slots; i++ {
		slotProj := fmt.Sprintf("%s/slot-%d", proj, i)
		slotWS := fmt.Sprintf("%s-%d", ws, i)
		_ = NewExecCommander().Run(context.Background(), Cmd{
			Name: "workshop", Args: []string{"--project", slotProj, "remove", slotWS},
		})
	}
}
