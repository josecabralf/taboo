package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/josecabralf/taboo/internal/agent"
	"github.com/josecabralf/taboo/internal/exec"
	"github.com/josecabralf/taboo/internal/workshop"
)

// fakeCommander records every invocation and can be programmed to fail
// specific commands via errFn. It is safe for concurrent use (the Pool fans runs
// out across goroutines), so mu guards calls and worktrees and every accessor
// takes it.
type fakeCommander struct {
	mu        sync.Mutex
	calls     []exec.Cmd
	errFn     func(c exec.Cmd) error
	stdoutFn  func(c exec.Cmd) string // programmed stdout for a matched call
	worktrees map[string]struct{}     // branches already added, to model git's statefulness

	// Optional concurrency gate for Pool tests. A call whose verb == gateVerb
	// updates the inflight/peak meter, signals entered, then blocks until it
	// receives a token from gate — all WITHOUT holding mu, so concurrently gated
	// calls genuinely overlap and peak reflects true simultaneity. Leaving gate
	// nil disables it (the default for non-concurrency tests).
	gateVerb string
	gate     chan struct{}
	entered  chan struct{}
	inflight atomic.Int32
	peak     atomic.Int32
}

func (f *fakeCommander) Run(_ context.Context, c exec.Cmd) error {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	// Model the one piece of git statefulness the orchestrator depends on: a
	// second `worktree add -b <branch>` for a branch already added fails, as
	// real git does. Without this the fake would silently accept a re-add and
	// hide a loop that re-creates the worktree every iteration.
	if branch, ok := worktreeAddBranch(c); ok {
		if _, exists := f.worktrees[branch]; exists {
			f.mu.Unlock()
			return fmt.Errorf("fatal: a branch named %q already exists", branch)
		}
		if f.worktrees == nil {
			f.worktrees = map[string]struct{}{}
		}
		f.worktrees[branch] = struct{}{}
	}
	stdoutFn, errFn := f.stdoutFn, f.errFn
	gateVerb, gate, entered := f.gateVerb, f.gate, f.entered
	f.mu.Unlock()

	// Gate matching calls without holding mu so they truly overlap. Count this
	// call as in flight, push the peak up to the new high-water mark, announce
	// arrival, then park until the test releases one token.
	if gate != nil && verbOf(c) == gateVerb {
		n := f.inflight.Add(1)
		for {
			p := f.peak.Load()
			if n <= p || f.peak.CompareAndSwap(p, n) {
				break
			}
		}
		if entered != nil {
			entered <- struct{}{}
		}
		<-gate
		f.inflight.Add(-1)
	}

	if stdoutFn != nil && c.Stdout != nil {
		if s := stdoutFn(c); s != "" {
			_, _ = io.WriteString(c.Stdout, s)
		}
	}
	if errFn != nil {
		return errFn(c)
	}
	return nil
}

// snapshot returns a copy of the recorded calls, taken under the lock, so tests
// can inspect the sequence after concurrent runs without racing the recorder.
func (f *fakeCommander) snapshot() []exec.Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// verbs returns the workshop/git subcommand verb of each recorded call, in
// order, for sequence assertions. For workshop calls the verb is the token
// after "--project <dir>"; for git it is "worktree".
func (f *fakeCommander) verbs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var vs []string
	for _, c := range f.calls {
		vs = append(vs, verbOf(c))
	}
	return vs
}

func verbOf(c exec.Cmd) string {
	if c.Name == "git" {
		if len(c.Args) >= 3 {
			return c.Args[2] // -C <repo> <verb>
		}
		return c.Name
	}
	// workshop --project <dir> <verb> ...
	for i, a := range c.Args {
		if a == "--project" {
			if i+2 < len(c.Args) {
				return c.Args[i+2]
			}
		}
	}
	if len(c.Args) > 0 {
		return c.Args[0]
	}
	return c.Name
}

// worktreeAddBranch reports the branch of a `git -C <repo> worktree add -b
// <branch> <path>` invocation, matching the worktree-add Runner.Setup issues.
// The ok result is false for any other call.
func worktreeAddBranch(c exec.Cmd) (string, bool) {
	if c.Name != "git" {
		return "", false
	}
	for i := 0; i+1 < len(c.Args); i++ {
		if c.Args[i] == "-b" {
			return c.Args[i+1], true
		}
	}
	return "", false
}

func failOnVerb(verb string) func(exec.Cmd) error {
	return func(c exec.Cmd) error {
		if verbOf(c) == verb {
			return fmt.Errorf("simulated failure for %q", verb)
		}
		return nil
	}
}

func testConfig(t *testing.T) workshop.Config {
	t.Helper()
	repo := t.TempDir()
	writeProjectDef(t, repo, "name: myproject\nbase: ubuntu@24.04\nsdks:\n  - name: go\n")
	return workshop.Config{
		Workshop:   "taboo-run",
		Base:       "ubuntu@24.04",
		Agent:      mustProfile("opencode", openCodeModel),
		RepoPath:   repo,
		ProjectDir: t.TempDir(),
	}
}

// writeProjectDef drops a workshop.yaml at the repo root (the file taboo derives from).
func writeProjectDef(t *testing.T, repo, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, "workshop.yaml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write project def: %v", err)
	}
}

func TestEnsureWorkshop_AbsentLaunches(t *testing.T) {
	// `info` fails -> workshop is absent -> taboo launches. ensureWorkshop is now
	// info-or-launch ONLY; seeding the SDK and writing the derived definition moved
	// to materialize, so this test asserts only the verb sequence.
	fc := &fakeCommander{errFn: failOnVerb("info")}
	cfg := testConfig(t)
	r := New(cfg, fc)

	if err := r.ensureWorkshop(context.Background(), "fp"); err != nil {
		t.Fatalf("ensureWorkshop: %v", err)
	}

	if got := fc.verbs(); !slices.Equal(got, []string{"info", "launch"}) {
		t.Errorf("calls = %v, want [info launch]", got)
	}
}

// TestEnsureWorkshop_AbsentLaunchesPersistsFingerprint pins the tracer-bullet
// path: an absent workshop is launched AND the derived def's fingerprint is
// recorded beside it, so the next run can probe for drift. The persisted digest
// must be exactly the fingerprint ensureWorkshop was handed (what the live
// workshop was just provisioned with).
func TestEnsureWorkshop_AbsentLaunchesPersistsFingerprint(t *testing.T) {
	cfg := testConfig(t)
	fc := &fakeCommander{errFn: failOnVerb("info")}
	r := New(cfg, fc)

	const fp = "f1deadbeef" // an arbitrary stand-in digest

	if err := r.ensureWorkshop(context.Background(), fp); err != nil {
		t.Fatalf("ensureWorkshop: %v", err)
	}

	// Absent -> launch.
	if got := fc.verbs(); !slices.Equal(got, []string{"info", "launch"}) {
		t.Errorf("calls = %v, want [info launch]", got)
	}

	// The fingerprint is persisted beside the derived def.
	b, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "workshop.fingerprint"))
	if err != nil {
		t.Fatalf("fingerprint sidecar not written: %v", err)
	}
	if got := strings.TrimSpace(string(b)); got != fp {
		t.Errorf("persisted fingerprint = %q, want %q", got, fp)
	}
}

func TestEnsureWorkshop_PresentReuses(t *testing.T) {
	// `info` succeeds -> workshop exists -> no launch. Pure reuse now also requires
	// the persisted fingerprint to MATCH the one ensureWorkshop is handed; a stale
	// (or absent) record forces a refresh instead, so pre-record the match.
	fc := &fakeCommander{} // all calls succeed
	r := New(testConfig(t), fc)

	if err := r.writeFingerprint("fp"); err != nil {
		t.Fatal(err)
	}

	if err := r.ensureWorkshop(context.Background(), "fp"); err != nil {
		t.Fatalf("ensureWorkshop: %v", err)
	}

	if got := fc.verbs(); !slices.Equal(got, []string{"info"}) {
		t.Errorf("calls = %v, want [info] (reuse, no launch)", got)
	}
}

// TestEnsureWorkshop_ChangedTriggersRefresh pins the #70 drift case: the workshop
// is present (info succeeds) but the persisted fingerprint is STALE — the
// project's workshop.yaml changed since the live workshop was provisioned, so
// taboo must reconcile by refreshing (not relaunching) and re-record the new
// digest so the next run takes the reuse fast path.
func TestEnsureWorkshop_ChangedTriggersRefresh(t *testing.T) {
	cfg := testConfig(t)
	fc := &fakeCommander{} // all verbs succeed -> workshop present
	r := New(cfg, fc)

	// A stale record: the live workshop was last provisioned with a different def.
	if err := r.writeFingerprint("stale"); err != nil {
		t.Fatal(err)
	}

	if err := r.ensureWorkshop(context.Background(), "fresh"); err != nil {
		t.Fatalf("ensureWorkshop: %v", err)
	}

	if got := fc.verbs(); !slices.Contains(got, "refresh") {
		t.Errorf("calls = %v, want a refresh (drift reconcile)", got)
	}
	if got := fc.verbs(); slices.Contains(got, "launch") {
		t.Errorf("calls = %v, want NO launch (refresh, not relaunch)", got)
	}

	// The sidecar is re-persisted to the new digest.
	b, err := os.ReadFile(r.fingerprintPath())
	if err != nil {
		t.Fatalf("fingerprint sidecar not written: %v", err)
	}
	if got := strings.TrimSpace(string(b)); got != "fresh" {
		t.Errorf("persisted fingerprint = %q, want %q", got, "fresh")
	}
}

// TestEnsureWorkshop_PresentWithoutFingerprintRefreshes covers the upgrade path: a
// workshop that predates fingerprinting (or was launched out of band) is present
// but carries no provisioning record. With no proof it matches the current def,
// taboo reconciles by refreshing — the safe default — and records the digest so
// later unchanged runs reuse it.
func TestEnsureWorkshop_PresentWithoutFingerprintRefreshes(t *testing.T) {
	cfg := testConfig(t)
	fc := &fakeCommander{} // info succeeds -> present; no sidecar on disk
	r := New(cfg, fc)

	if err := r.ensureWorkshop(context.Background(), "current"); err != nil {
		t.Fatalf("ensureWorkshop: %v", err)
	}

	if got := fc.verbs(); !slices.Contains(got, "refresh") {
		t.Errorf("calls = %v, want a refresh (no prior fingerprint -> safe reconcile)", got)
	}
	if got := fc.verbs(); slices.Contains(got, "launch") {
		t.Errorf("calls = %v, want NO launch (workshop is present)", got)
	}
	b, err := os.ReadFile(r.fingerprintPath())
	if err != nil {
		t.Fatalf("fingerprint sidecar not written: %v", err)
	}
	if got := strings.TrimSpace(string(b)); got != "current" {
		t.Errorf("persisted fingerprint = %q, want %q", got, "current")
	}
}

// TestEnsureWorkshop_UnchangedReusesNoRefresh locks acceptance #1: when the
// workshop is present (info succeeds) AND the persisted fingerprint MATCHES the
// just-derived def, taboo reuses it as-is — no refresh, no launch, no re-write
// churn. This no-op is what the expensive-launch amortization depends on, so the
// verb sequence must be exactly [info]; a future change that accidentally
// refreshed an unchanged workshop would trip this guard.
func TestEnsureWorkshop_UnchangedReusesNoRefresh(t *testing.T) {
	cfg := testConfig(t)
	fc := &fakeCommander{} // all verbs succeed -> workshop present
	r := New(cfg, fc)

	// The live workshop was last provisioned with this exact def.
	if err := r.writeFingerprint("same"); err != nil {
		t.Fatal(err)
	}

	if err := r.ensureWorkshop(context.Background(), "same"); err != nil {
		t.Fatalf("ensureWorkshop: %v", err)
	}

	if got := fc.verbs(); !slices.Equal(got, []string{"info"}) {
		t.Errorf("calls = %v, want [info] (reuse, no refresh/launch)", got)
	}
}

// TestReadFingerprint_SurfacesNonNotExistError pins the recovery boundary: an
// absent sidecar is the expected "no record" case ("", nil), but a real read
// failure must NOT masquerade as absent — masking it would force a spurious
// refresh and bury the underlying I/O fault. A directory at the fingerprint path
// makes os.ReadFile fail with a non-NotExist error, which readFingerprint must
// surface unchanged.
func TestReadFingerprint_SurfacesNonNotExistError(t *testing.T) {
	cfg := testConfig(t)
	r := New(cfg, &fakeCommander{})

	// A directory where the sidecar file is expected -> ReadFile fails with a
	// non-NotExist error (EISDIR).
	if err := os.MkdirAll(r.fingerprintPath(), 0o750); err != nil {
		t.Fatalf("mkdir fingerprint path: %v", err)
	}

	got, err := r.readFingerprint()
	if err == nil {
		t.Fatalf("readFingerprint() = %q, nil; want a surfaced non-NotExist error", got)
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Errorf("readFingerprint() error = %v; a non-NotExist error must not be reported as absent", err)
	}
}

// TestEnsureWorkshop_PropagatesFingerprintReadError pins that ensureWorkshop does
// not swallow a fingerprint read failure: when the workshop is present (info
// succeeds) but the sidecar is unreadable for a reason OTHER than absence, taboo
// must abort rather than silently forcing a refresh on a corrupt read.
func TestEnsureWorkshop_PropagatesFingerprintReadError(t *testing.T) {
	cfg := testConfig(t)
	fc := &fakeCommander{} // info succeeds -> workshop present
	r := New(cfg, fc)

	// A directory at the sidecar path -> readFingerprint hits a non-NotExist error.
	if err := os.MkdirAll(r.fingerprintPath(), 0o750); err != nil {
		t.Fatalf("mkdir fingerprint path: %v", err)
	}

	if err := r.ensureWorkshop(context.Background(), "fp"); err == nil {
		t.Fatal("ensureWorkshop() = nil; want the surfaced fingerprint read error")
	}
	if got := fc.verbs(); slices.Contains(got, "refresh") {
		t.Errorf("calls = %v; a read error must abort, not force a refresh", got)
	}
}

// TestMaterialize_ErrorsWhenSourceAbsent pins the defensive precondition: taboo
// DERIVES from the project's own workshop.yaml, so its absence is a hard error
// (not a silent fallback). The source read moved up into materialize, so the
// error surfaces there; it must name the missing path so the operator can see
// which file to create.
func TestMaterialize_ErrorsWhenSourceAbsent(t *testing.T) {
	cfg := testConfig(t)
	cfg.RepoPath = t.TempDir() // a bare repo dir with NO workshop.yaml
	r := New(cfg, &fakeCommander{})

	_, err := r.materialize()
	if err == nil {
		t.Fatal("materialize succeeded with no source workshop.yaml; want an error")
	}
	if !strings.Contains(err.Error(), "no workshop definition found") {
		t.Errorf("error %q does not report the missing workshop definition", err)
	}
	if !strings.Contains(err.Error(), cfg.RepoPath) {
		t.Errorf("error %q does not name the repo path %q the operator must add a definition to", err, cfg.RepoPath)
	}
}

// TestSetup_ReconcilesOnReuse proves materialize (hence reconcile) runs on EVERY
// Setup, including the workshop-REUSE path (info succeeds, no launch). A source
// with a project-mylib SDK must get its <ProjectDir>/.workshop/mylib symlink even
// when no launch occurs.
func TestSetup_ReconcilesOnReuse(t *testing.T) {
	repoPath, projectDir := t.TempDir(), t.TempDir()
	// A source declaring an in-project SDK as project-mylib.
	writeProjectDef(t, repoPath, "name: x\nbase: ubuntu@24.04\nsdks:\n  - name: project-mylib\n")
	// The project's real SDK dir the symlink must point at.
	realDir := filepath.Join(repoPath, ".workshop", "mylib")
	if err := os.MkdirAll(realDir, 0o750); err != nil {
		t.Fatalf("mkdir real sdk dir: %v", err)
	}

	cfg := workshop.Config{
		Workshop:   "taboo-run",
		Base:       "ubuntu@24.04",
		Agent:      mustProfile("opencode", openCodeModel),
		RepoPath:   repoPath,
		ProjectDir: projectDir,
	}
	// info SUCCEEDS -> ensureWorkshop reuses, no launch.
	fc := &fakeCommander{}
	r := New(cfg, fc)

	if _, err := r.Setup(context.Background(), RunRequest{Branch: "agent/x", Prompt: "go"}); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	// Reconcile ran even on the reuse path: the symlink exists and points at the
	// project's real SDK dir.
	link := filepath.Join(projectDir, ".workshop", "mylib")
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("reconcile did not run on reuse path; symlink absent: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("%s is not a symlink (mode %v)", link, fi.Mode())
	}
	if target, _ := os.Readlink(link); target != realDir {
		t.Errorf("link target = %q, want %q", target, realDir)
	}

	// Confirm reuse, not launch.
	verbs := fc.verbs()
	if !slices.Contains(verbs, "info") {
		t.Errorf("verbs %v missing info (expected the reuse probe)", verbs)
	}
	if slices.Contains(verbs, "launch") {
		t.Errorf("verbs %v contains launch; workshop should have been reused", verbs)
	}
}

// TestSetup_ChangedProjectDefRefreshesWorkshop exercises the whole drift loop
// through the public Setup API rather than a hand-passed digest: materialize
// derives + hashes the REAL def, ensureWorkshop persists/compares the
// fingerprint, and a changed project workshop.yaml between runs reconciles the
// live workshop. Run 1 launches a fresh workshop (info fails) and records its
// fingerprint; the source def is then mutated to add an SDK; Run 2 sees the
// workshop present but the regenerated def's fingerprint no longer matches, so
// it refreshes and re-records. This proves acceptance #2 (changed def triggers a
// refresh), #3 (the new toolchain reaches the def the refresh reprovisions
// from), and #4 (the fingerprint is compared and updated each run).
func TestSetup_ChangedProjectDefRefreshesWorkshop(t *testing.T) {
	cfg := testConfig(t)
	// Model the real workshop lifecycle: `info` fails until a `launch` has run,
	// then succeeds (present). A single workshop, so a plain bool suffices.
	var launched bool
	fc := &fakeCommander{
		errFn: func(c exec.Cmd) error {
			switch verbOf(c) {
			case "launch":
				launched = true
			case "info":
				if !launched {
					return fmt.Errorf("no such workshop")
				}
			}
			return nil
		},
	}
	r := New(cfg, fc)
	ctx := context.Background()

	// Run 1: info fails -> launch -> fingerprint persisted.
	if _, err := r.Setup(ctx, RunRequest{Branch: "agent/run-1", Prompt: "x"}); err != nil {
		t.Fatalf("Setup run 1: %v", err)
	}
	fp1, err := r.readFingerprint()
	if err != nil {
		t.Fatalf("readFingerprint run 1: %v", err)
	}
	if fp1 == "" {
		t.Fatal("run 1 did not persist a fingerprint")
	}

	// Mutate the project's source def to add a recognizable new SDK.
	writeProjectDef(t, cfg.RepoPath, "name: myproject\nbase: ubuntu@24.04\nsdks:\n  - name: go\n  - name: ruff\n")

	// Run 2: info now succeeds (present); the regenerated def differs -> refresh.
	if _, err := r.Setup(ctx, RunRequest{Branch: "agent/run-2", Prompt: "x"}); err != nil {
		t.Fatalf("Setup run 2: %v", err)
	}

	// The changed def reconciled via refresh; launch happened only on run 1.
	verbs := fc.verbs()
	if !slices.Contains(verbs, "refresh") {
		t.Errorf("verbs %v missing refresh; the changed def should have reconciled the workshop", verbs)
	}
	if got := strings.Count(strings.Join(verbs, " "), "launch"); got != 1 {
		t.Errorf("launch count = %d, want 1 (only run 1 launches)", got)
	}

	// The fingerprint was re-derived and re-persisted for the changed def.
	fp2, err := r.readFingerprint()
	if err != nil {
		t.Fatalf("readFingerprint run 2: %v", err)
	}
	if fp2 == fp1 {
		t.Errorf("fingerprint unchanged (%q) after a changed def; it must be recompared and updated each run", fp2)
	}

	// The regenerated derived def reprovisioned from carries the new toolchain.
	def, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "workshop.yaml"))
	if err != nil {
		t.Fatalf("read derived def: %v", err)
	}
	if !strings.Contains(string(def), "ruff") {
		t.Errorf("derived def %q missing the added SDK %q", def, "ruff")
	}
}

// TestSetup_BaseRefFetchesAndStartsWorktreeFromRef pins the #83 capability: when
// RunRequest.BaseRef is set, Setup first fetches origin (so the ref — and
// origin/main, which the agent later merges — are current) and then starts the
// run's worktree branch FROM that ref, rather than from the host repo's HEAD.
// The fetch must precede the worktree add (the ref has to exist locally before
// git can branch from it).
func TestSetup_BaseRefFetchesAndStartsWorktreeFromRef(t *testing.T) {
	cfg := testConfig(t)
	fc := &fakeCommander{errFn: failOnVerb("info")} // absent -> launch, like TestRun
	r := New(cfg, fc)

	res, err := r.Setup(context.Background(), RunRequest{
		Branch:  "agent/update-pr-12",
		BaseRef: "origin/feature-x",
		Prompt:  "merge main",
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	// origin is fetched so the base ref (and origin/main) are current locally.
	fetch := fc.findCallN(t, "fetch", 0)
	wantFetch := []string{"-C", cfg.RepoPath, "fetch", "origin"}
	if !slices.Equal(fetch.Args, wantFetch) {
		t.Errorf("fetch args = %v, want %v", fetch.Args, wantFetch)
	}

	// The worktree branch starts at the base ref's tip (the start-point trailing arg).
	wtAdd := fc.findCallN(t, "worktree", 0)
	wantAdd := []string{"-C", cfg.RepoPath, "worktree", "add", "-b", "agent/update-pr-12", res.WorktreePath, "origin/feature-x"}
	if !slices.Equal(wtAdd.Args, wantAdd) {
		t.Errorf("worktree add args = %v, want %v", wtAdd.Args, wantAdd)
	}

	// The fetch must come before the worktree add: git can only branch from the
	// ref once it has been updated locally.
	verbs := fc.verbs()
	if fi, wi := slices.Index(verbs, "fetch"), slices.Index(verbs, "worktree"); fi == -1 || fi > wi {
		t.Errorf("verbs %v: fetch must precede worktree add", verbs)
	}
}

// TestSetup_NoBaseRefSkipsFetchAndStartPoint pins the default path's negative: an
// empty BaseRef must NOT fetch and must add the worktree with no start-point, so
// a plain run still branches off the host repo's HEAD exactly as before #83. A
// stray fetch (a network round-trip) or a trailing start-point would silently
// change every existing run's behavior.
func TestSetup_NoBaseRefSkipsFetchAndStartPoint(t *testing.T) {
	cfg := testConfig(t)
	fc := &fakeCommander{errFn: failOnVerb("info")}
	r := New(cfg, fc)

	res, err := r.Setup(context.Background(), RunRequest{Branch: "agent/plain", Prompt: "go"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	if slices.Contains(fc.verbs(), "fetch") {
		t.Errorf("verbs %v contains fetch; the default path must not fetch", fc.verbs())
	}
	wtAdd := fc.findCallN(t, "worktree", 0)
	wantAdd := []string{"-C", cfg.RepoPath, "worktree", "add", "-b", "agent/plain", res.WorktreePath}
	if !slices.Equal(wtAdd.Args, wantAdd) {
		t.Errorf("worktree add args = %v, want %v (no start-point)", wtAdd.Args, wantAdd)
	}
}

// findCallN returns the nth (0-based) recorded Cmd whose verb matches, or fails.
func (f *fakeCommander) findCallN(t *testing.T, verb string, n int) exec.Cmd {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := 0
	for _, c := range f.calls {
		if verbOf(c) == verb {
			if seen == n {
				return c
			}
			seen++
		}
	}
	t.Fatalf("no call #%d with verb %q in %v", n, verb, f.verbs())
	return exec.Cmd{}
}

func TestRun_PerRunSequence(t *testing.T) {
	fc := &fakeCommander{errFn: failOnVerb("info")} // workshop absent -> launches
	cfg := testConfig(t)
	r := New(cfg, fc)

	res, err := r.Run(context.Background(), RunRequest{
		Branch: "agent/skeleton",
		Prompt: "scaffold a go module",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The verified recipe order: ensure (info+launch) -> worktree add ->
	// stop -> remount workspace -> remount gitcommon -> remount sessions ->
	// start -> exec. The sessions remount is present because OpenCode is a
	// session-capable agent.
	wantSeq := []string{"info", "launch", "worktree", "stop", "remount", "remount", "remount", "start", "exec", "rev-parse"}
	if got := fc.verbs(); !slices.Equal(got, wantSeq) {
		t.Fatalf("sequence =\n  %v\nwant\n  %v", got, wantSeq)
	}

	// Two-mount rule: workspace -> the worktree; gitcommon -> the repo's .git.
	// Assert the whole remount argv against the builder the production path uses
	// (its flag layout is pinned by TestWorkshopArgs), so this test stays about
	// wiring the right plug+source rather than positional argument shape.
	wantWs := workshop.RemountArgs(cfg.ProjectDir, cfg.Workshop, string(cfg.Agent.Name()), "workspace", res.WorktreePath)
	if got := fc.findCallN(t, "remount", 0).Args; !slices.Equal(got, wantWs) {
		t.Errorf("workspace remount args =\n  %v\nwant\n  %v", got, wantWs)
	}
	// The gitcommon source is the host .git absolute path; derived from RepoPath
	// via the production helper (the load-bearing half of the two-mount rule),
	// which tracks the temp RepoPath testConfig allocates.
	wantGc := workshop.RemountArgs(cfg.ProjectDir, cfg.Workshop, string(cfg.Agent.Name()), "gitcommon", workshop.GitCommonTarget(cfg.RepoPath))
	if got := fc.findCallN(t, "remount", 1).Args; !slices.Equal(got, wantGc) {
		t.Errorf("gitcommon remount args =\n  %v\nwant\n  %v", got, wantGc)
	}
	// Sessions mount: a host sessions dir under the project is bound so the
	// agent's session files survive the swap. It is the third remount.
	wantSess := workshop.RemountArgs(cfg.ProjectDir, cfg.Workshop, string(cfg.Agent.Name()), "sessions", filepath.Join(cfg.ProjectDir, "sessions"))
	if got := fc.findCallN(t, "remount", 2).Args; !slices.Equal(got, wantSess) {
		t.Errorf("sessions remount args =\n  %v\nwant\n  %v", got, wantSess)
	}
	// The host sessions dir must exist for the remount source to resolve.
	if _, err := os.Stat(filepath.Join(cfg.ProjectDir, "sessions")); err != nil {
		t.Errorf("host sessions dir not created: %v", err)
	}

	// The worktree is created on the requested branch.
	wtAdd := fc.findCallN(t, "worktree", 0)
	if !slices.Contains(wtAdd.Args, "agent/skeleton") {
		t.Errorf("worktree add missing branch: %v", wtAdd.Args)
	}
	if !slices.Contains(wtAdd.Args, res.WorktreePath) {
		t.Errorf("worktree add missing worktree path %q: %v", res.WorktreePath, wtAdd.Args)
	}

	// exec carries the agent command + prompt, env keys, and /taboo/workspace cwd.
	exec := fc.findCallN(t, "exec", 0)
	if !slices.Contains(exec.Args, "scaffold a go module") {
		t.Errorf("exec missing prompt: %v", exec.Args)
	}
	if !slices.Contains(exec.Args, "OPENROUTER_API_KEY") {
		t.Errorf("exec missing env key: %v", exec.Args)
	}
	if !slices.Contains(exec.Args, "/taboo/workspace") {
		t.Errorf("exec missing /taboo/workspace cwd: %v", exec.Args)
	}
}

// A session-capable agent has its session-dir env var redirected to the sessions
// mount target at exec time, so the agent writes session files into the bound
// host directory rather than the ephemeral rootfs. The value is set explicitly
// (NAME=VALUE), unlike inherited credential keys.
func TestRun_RedirectsSessionDirEnv(t *testing.T) {
	fc := &fakeCommander{}
	cfg := testConfig(t)
	r := New(cfg, fc)

	if _, err := r.Run(context.Background(), RunRequest{Branch: "agent/x", Prompt: "go"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	spec, ok := cfg.Agent.Sessions()
	if !ok {
		t.Fatal("test config agent is not session-capable")
	}
	// The redirect value must be the sessions mount target itself (the host dir
	// Setup bound there), so the agent writes session files into the bind-mount.
	want := spec.DirEnv + "=" + workshop.SessionsTarget // e.g. XDG_DATA_HOME=/taboo/sessions
	exec := fc.findCallN(t, "exec", 0)
	if !slices.Contains(exec.Args, want) {
		t.Errorf("exec missing session-dir redirect %q in argv: %v", want, exec.Args)
	}
}

// A run that carries a resume-session id threads it through the AgentProfile's
// command builder into the agent exec, so the agent continues the prior session
// rather than starting fresh. This is the end-to-end resume path: RunRequest ->
// CommandOptions -> BuildCommand -> exec argv.
func TestRun_ResumeSessionReachesExec(t *testing.T) {
	fc := &fakeCommander{}
	cfg := testConfig(t)
	r := New(cfg, fc)

	const sessionID = "ses_abc123"
	if _, err := r.Run(context.Background(), RunRequest{
		Branch: "agent/x", Prompt: "go", ResumeSession: sessionID,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	exec := fc.findCallN(t, "exec", 0)
	if !slices.Contains(exec.Args, sessionID) {
		t.Errorf("exec missing resume session id %q in argv: %v", sessionID, exec.Args)
	}
}

// A plain resume (no Fork) threads the session id to exec but must NOT carry the
// agent's fork flag: resume continues the source session in place, whereas a
// stray --fork would divert the work into a new forked session and leave the
// resumed conversation untouched. This pins the negative at the Runner layer;
// TestRun_ResumeSessionReachesExec only asserts the id is present.
func TestRun_ResumeWithoutForkOmitsForkFlag(t *testing.T) {
	fc := &fakeCommander{}
	cfg := testConfig(t)
	r := New(cfg, fc)

	if _, err := r.Run(context.Background(), RunRequest{
		Branch: "agent/x", Prompt: "go", ResumeSession: "ses_abc123",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	exec := fc.findCallN(t, "exec", 0)
	if slices.Contains(exec.Args, "--fork") {
		t.Errorf("plain resume exec carries --fork; it must not: %v", exec.Args)
	}
}

// A fork run resumes a prior session AND branches it: the session id and the
// agent's fork flag both reach the exec, while Setup allocates a fresh worktree
// on the fork's branch. Together these isolate a divergent continuation at the
// session level (the source conversation is not mutated) and the filesystem
// level (a new worktree) — the two halves of taboo's fork.
func TestRun_ForkReachesExecAndAllocatesNewWorktree(t *testing.T) {
	fc := &fakeCommander{}
	cfg := testConfig(t)
	r := New(cfg, fc)

	const sessionID = "ses_src"
	res, err := r.Run(context.Background(), RunRequest{
		Branch: "fork/divergent", Prompt: "go", ResumeSession: sessionID, Fork: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Session-level isolation: the resume id and the agent's fork flag both reach
	// exec, so the agent forks the source session rather than appending to it.
	exec := fc.findCallN(t, "exec", 0)
	if !slices.Contains(exec.Args, sessionID) {
		t.Errorf("fork exec missing resume session id %q: %v", sessionID, exec.Args)
	}
	if !slices.Contains(exec.Args, "--fork") {
		t.Errorf("fork exec missing --fork flag: %v", exec.Args)
	}

	// Filesystem-level isolation: a fresh worktree is allocated on the fork branch.
	wtAdd := fc.findCallN(t, "worktree", 0)
	if !slices.Contains(wtAdd.Args, "fork/divergent") {
		t.Errorf("fork did not allocate a worktree on the fork branch: %v", wtAdd.Args)
	}
	if !slices.Contains(wtAdd.Args, res.WorktreePath) {
		t.Errorf("fork worktree path %q missing from worktree add: %v", res.WorktreePath, wtAdd.Args)
	}
}

// An agent with no session store gets none of the sessions wiring: no sessions
// remount in the swap, no session-dir env on exec, and no host sessions dir is
// created. This pins the negative branch of the Sessions() guard.
func TestRun_SessionlessAgent_NoSessionsWiring(t *testing.T) {
	fc := &fakeCommander{}
	cfg := testConfig(t)
	cfg.Agent = stdinProfile{} // Sessions() ok == false
	r := New(cfg, fc)

	if _, err := r.Run(context.Background(), RunRequest{Branch: "agent/x", Prompt: "go"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Only the two core remounts (workspace, gitcommon); no sessions remount.
	remounts := 0
	for _, v := range fc.verbs() {
		if v == "remount" {
			remounts++
		}
	}
	if remounts != 2 {
		t.Errorf("got %d remounts, want 2 (workspace+gitcommon, no sessions); verbs: %v", remounts, fc.verbs())
	}

	// No session-dir redirect leaks into the agent exec.
	exec := fc.findCallN(t, "exec", 0)
	for _, a := range exec.Args {
		if strings.Contains(a, "/sessions") {
			t.Errorf("sessionless agent exec carries a session redirect: %v", exec.Args)
		}
	}

	// No host sessions dir is created when there is nothing to persist.
	if _, err := os.Stat(filepath.Join(cfg.ProjectDir, "sessions")); !os.IsNotExist(err) {
		t.Errorf("host sessions dir created for a sessionless agent (err=%v)", err)
	}
}

func TestRun_StreamsExecOutputToRequestWriters(t *testing.T) {
	var out, errBuf strings.Builder
	fc := &fakeCommander{
		stdoutFn: func(c exec.Cmd) string {
			if verbOf(c) == "exec" {
				return "agent says hi\n"
			}
			return ""
		},
	}
	cfg := testConfig(t)
	r := New(cfg, fc)

	_, err := r.Run(context.Background(), RunRequest{
		Branch: "agent/x", Prompt: "go",
		Stdout: &out, Stderr: &errBuf,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	exec := fc.findCallN(t, "exec", 0)
	if exec.Stdout == nil {
		t.Fatal("exec Cmd.Stdout not wired to request writer")
	}
	if out.String() != "agent says hi\n" {
		t.Errorf("streamed stdout = %q, want %q", out.String(), "agent says hi\n")
	}
}

func TestRun_CapturesExecStdout(t *testing.T) {
	// The runner retains the agent's exec stdout on RunResult.Output. When the
	// caller also supplies a Stdout writer, it tees: both the caller's writer
	// and RunResult.Output receive the output.
	for _, tc := range []struct {
		name   string
		stream bool
	}{
		{"no caller writer", false},
		{"tees to caller writer", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			fc := &fakeCommander{
				stdoutFn: func(c exec.Cmd) string {
					if verbOf(c) == "exec" {
						return "agent did stuff\n"
					}
					return ""
				},
			}
			cfg := testConfig(t)

			req := RunRequest{Branch: "agent/x", Prompt: "go"}
			if tc.stream {
				req.Stdout = &out
			}
			res, err := New(cfg, fc).Run(context.Background(), req)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			if res.Output != "agent did stuff\n" {
				t.Errorf("Output = %q, want %q", res.Output, "agent did stuff\n")
			}
			if tc.stream && out.String() != "agent did stuff\n" {
				t.Errorf("streamed stdout = %q, want %q", out.String(), "agent did stuff\n")
			}
		})
	}
}

func TestRun_CapturesCommit(t *testing.T) {
	fc := &fakeCommander{
		stdoutFn: func(c exec.Cmd) string {
			if verbOf(c) == "rev-parse" {
				return "deadbeefcafe\n"
			}
			return ""
		},
	}
	cfg := testConfig(t)
	r := New(cfg, fc)

	res, err := r.Run(context.Background(), RunRequest{Branch: "agent/x", Prompt: "go"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Commit != "deadbeefcafe" {
		t.Errorf("Commit = %q, want deadbeefcafe", res.Commit)
	}

	// rev-parse runs against the worktree, after exec.
	rp := fc.findCallN(t, "rev-parse", 0)
	if !slices.Contains(rp.Args, res.WorktreePath) {
		t.Errorf("rev-parse not run against worktree %q: %v", res.WorktreePath, rp.Args)
	}
	verbs := fc.verbs()
	if verbs[len(verbs)-1] != "rev-parse" {
		t.Errorf("rev-parse should be last; sequence = %v", verbs)
	}
}

// TestSetup_DogfoodSymlinksProjectTaboo is the dogfood at the Setup seam: a
// real Setup driven from a taboo-style source (a project-taboo in-project SDK)
// must symlink <ProjectDir>/.workshop/taboo at the project's real SDK dir, while
// leaving the seeded agent SDK as an untouched real dir — slices 4 + 5 composed.
// Hermetic: the source def and the real SDK dir are built in temp dirs, so it
// never depends on the live repo's .workshop/ tree. `info` SUCCEEDS so the
// workshop is REUSED, proving the symlink lands on the reuse path too
// (materialize runs before ensureWorkshop).
func TestSetup_DogfoodSymlinksProjectTaboo(t *testing.T) {
	repo := t.TempDir()
	writeProjectDef(t, repo, "name: taboo\nbase: ubuntu@24.04\nsdks:\n  - name: go\n    channel: 1.26/stable\n  - name: project-taboo\nactions:\n  make: |\n    make \"$@\"\n")

	// The project's real in-project SDK dir, with a sentinel the symlink must
	// resolve to once reconcile points <ProjectDir>/.workshop/taboo at it.
	realSDKDir := filepath.Join(repo, ".workshop", "taboo")
	if err := os.MkdirAll(realSDKDir, 0o750); err != nil {
		t.Fatalf("mkdir real sdk dir: %v", err)
	}
	const sentinel = "real project-taboo sdk\n"
	if err := os.WriteFile(filepath.Join(realSDKDir, "sdk.yaml"), []byte(sentinel), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	cfg := workshop.Config{
		Workshop:   "taboo-run",
		Base:       "ubuntu@24.04",
		Agent:      mustProfile("opencode", openCodeModel),
		RepoPath:   repo,
		ProjectDir: t.TempDir(),
	}
	// info SUCCEEDS -> workshop reused -> no launch. materialize (hence reconcile)
	// still runs before ensureWorkshop, so the symlink must appear here too.
	fc := &fakeCommander{}
	r := New(cfg, fc)

	if _, err := r.Setup(context.Background(), RunRequest{Branch: "agent/dogfood", Prompt: "go"}); err != nil {
		t.Fatalf("Setup: %v", err)
	}

	// <ProjectDir>/.workshop/taboo is a SYMLINK at the project's real SDK dir.
	link := filepath.Join(cfg.ProjectDir, ".workshop", "taboo")
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("project-taboo symlink absent: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is not a symlink (mode %v)", link, fi.Mode())
	}
	if target, _ := os.Readlink(link); target != realSDKDir {
		t.Errorf("link target = %q, want %q", target, realSDKDir)
	}

	// Reading THROUGH the link returns the real bytes (the link resolves to the
	// project's live SDK, not a copy).
	got, err := os.ReadFile(filepath.Join(link, "sdk.yaml"))
	if err != nil {
		t.Fatalf("read sentinel through link: %v", err)
	}
	if string(got) != sentinel {
		t.Errorf("read through link = %q, want %q", got, sentinel)
	}

	// The seeded agent SDK is a REAL dir, untouched by reconcile (sanity that the
	// seed survives the symlink reconciliation).
	seeded := filepath.Join(cfg.ProjectDir, ".workshop", "opencode")
	sfi, err := os.Lstat(seeded)
	if err != nil {
		t.Fatalf("seeded agent SDK dir absent: %v", err)
	}
	if sfi.Mode()&os.ModeSymlink != 0 {
		t.Errorf("%s is a symlink; the seeded agent SDK must stay a real dir", seeded)
	}
	if !sfi.IsDir() {
		t.Errorf("%s is not a directory (mode %v); the seed must remain a real dir", seeded, sfi.Mode())
	}

	// Confirm the reuse path: info probed, launch never issued.
	verbs := fc.verbs()
	if !slices.Contains(verbs, "info") {
		t.Errorf("verbs %v missing info (expected the reuse probe)", verbs)
	}
	if slices.Contains(verbs, "launch") {
		t.Errorf("verbs %v contains launch; workshop should have been reused", verbs)
	}
}

// stdinProfile is a minimal AgentProfile that delivers the prompt on stdin (as
// the Claude/Codex/Pi agents do), used to exercise the Exec stdin path that
// OpenCode — which delivers the prompt in argv — leaves untaken.
type stdinProfile struct{}

func (stdinProfile) Name() agent.AgentName { return agent.OpenCode }
func (stdinProfile) BuildCommand(o agent.CommandOptions) agent.AgentCommand {
	return agent.AgentCommand{Argv: []string{"claude", "--print", "-p", "-"}, Stdin: o.Prompt}
}
func (stdinProfile) CredentialEnvKeys() []string         { return nil }
func (stdinProfile) Sessions() (agent.SessionSpec, bool) { return agent.SessionSpec{}, false }

// A stdin-delivery agent has its prompt piped to the exec's stdin, and the
// prompt must not also appear in argv. This pins the ac.Stdin wiring in Exec,
// the whole reason AgentCommand carries a Stdin field (see ADR 0001).
func TestExec_StdinDeliveryAgentPipesPromptToStdin(t *testing.T) {
	fc := &fakeCommander{}
	cfg := testConfig(t)
	cfg.Agent = stdinProfile{}
	r := New(cfg, fc)

	const prompt = "do the task"
	if _, err := r.Run(context.Background(), RunRequest{Branch: "agent/x", Prompt: prompt}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	exec := fc.findCallN(t, "exec", 0)
	if exec.Stdin == nil {
		t.Fatal("exec Cmd.Stdin not wired for a stdin-delivery agent")
	}
	got, err := io.ReadAll(exec.Stdin)
	if err != nil {
		t.Fatalf("read exec stdin: %v", err)
	}
	if string(got) != prompt {
		t.Errorf("exec stdin = %q, want %q", got, prompt)
	}
	// The prompt rides stdin, so it must not leak into argv.
	if slices.Contains(exec.Args, prompt) {
		t.Errorf("prompt leaked into argv for a stdin-delivery agent: %v", exec.Args)
	}
}
