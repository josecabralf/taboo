package taboo

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"gopkg.in/yaml.v3"
)

// fakeCommander records every invocation and can be programmed to fail
// specific commands via errFn. It is safe for concurrent use (the Pool fans runs
// out across goroutines), so mu guards calls and worktrees and every accessor
// takes it.
type fakeCommander struct {
	mu        sync.Mutex
	calls     []Cmd
	errFn     func(c Cmd) error
	stdoutFn  func(c Cmd) string  // programmed stdout for a matched call
	worktrees map[string]struct{} // branches already added, to model git's statefulness

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

func (f *fakeCommander) Run(_ context.Context, c Cmd) error {
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
func (f *fakeCommander) snapshot() []Cmd {
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

func verbOf(c Cmd) string {
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
func worktreeAddBranch(c Cmd) (string, bool) {
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

func failOnVerb(verb string) func(Cmd) error {
	return func(c Cmd) error {
		if verbOf(c) == verb {
			return fmt.Errorf("simulated failure for %q", verb)
		}
		return nil
	}
}

func testConfig(t *testing.T) Config {
	t.Helper()
	repo := t.TempDir()
	writeProjectDef(t, repo, "name: myproject\nbase: ubuntu@24.04\nsdks:\n  - name: go\n")
	return Config{
		Workshop:   "taboo-run",
		Base:       "ubuntu@24.04",
		Agent:      OpenCode(openCodeModel),
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

// sourceDefinitionPath points at the project's OWN workshop.yaml — the file the
// project's human developers already use — which lives at the repo root
// (RepoPath), never under taboo's ProjectDir (<repo>/.taboo). The anchor is
// load-bearing: a future accidental swap to ProjectDir would silently derive the
// agent's workshop from the wrong (or a non-existent) file, so the test pins both
// the exact path and the RepoPath/NOT-ProjectDir prefixes.
func TestSourceDefinitionPath_AnchorsOnRepoPath(t *testing.T) {
	cfg := testConfig(t)
	r := New(cfg, &fakeCommander{})

	want := filepath.Join(cfg.RepoPath, "workshop.yaml")
	got := r.sourceDefinitionPath()
	if got != want {
		t.Errorf("sourceDefinitionPath() = %q, want %q", got, want)
	}
	if !strings.HasPrefix(got, cfg.RepoPath) {
		t.Errorf("sourceDefinitionPath() = %q, want it anchored on RepoPath %q", got, cfg.RepoPath)
	}
	if strings.HasPrefix(got, cfg.ProjectDir) {
		t.Errorf("sourceDefinitionPath() = %q must NOT be under ProjectDir %q", got, cfg.ProjectDir)
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

// TestWriteDefinition_FingerprintIsStableForUnchangedSource pins the property the
// reuse fast path (acceptance #1) rests on: an unchanged project workshop.yaml
// derives to a byte-identical definition, hence the same fingerprint, so a run
// reuses the live workshop instead of churning a spurious refresh every time. It
// guards against a future derive change (e.g. an unsorted plug map) that would
// silently vary the digest from one run to the next.
func TestWriteDefinition_FingerprintIsStableForUnchangedSource(t *testing.T) {
	cfg := testConfig(t)
	r := New(cfg, &fakeCommander{})
	source := []byte("name: myproject\nbase: ubuntu@24.04\nsdks:\n  - name: go\n")

	fp1, _, err := r.writeDefinition(source)
	if err != nil {
		t.Fatalf("writeDefinition (1): %v", err)
	}
	fp2, _, err := r.writeDefinition(source)
	if err != nil {
		t.Fatalf("writeDefinition (2): %v", err)
	}
	if fp1 == "" {
		t.Fatal("fingerprint is empty")
	}
	if fp1 != fp2 {
		t.Errorf("fingerprint not stable for unchanged source: %q vs %q", fp1, fp2)
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

// TestWriteDefinition_DerivesFromProjectDef pins the cut-over: writeDefinition no
// longer renders a definition from scratch — it READS the project's own
// workshop.yaml (at RepoPath) and DERIVES the agent's definition from it. The
// derived def lands at <ProjectDir>/workshop.yaml and must carry through the
// source verbatim (base, the source `go` sdk + its channel, the actions block)
// while overwriting name with cfg.Workshop and appending the agent SDK. Asserted
// on decoded values, not bytes, so formatting is not load-bearing.
func TestWriteDefinition_DerivesFromProjectDef(t *testing.T) {
	cfg := testConfig(t)
	// A recognizable source: a `go` sdk carrying a channel, plus an actions block,
	// both of which taboo does not model and must carry through untouched.
	source := "" +
		"name: myproject\n" +
		"base: ubuntu@24.04\n" +
		"sdks:\n" +
		"  - name: go\n" +
		"    channel: 1.26/stable\n" +
		"actions:\n" +
		"  test:\n" +
		"    command: go test ./...\n"
	writeProjectDef(t, cfg.RepoPath, source)
	r := New(cfg, &fakeCommander{})

	sourceBytes, err := os.ReadFile(r.sourceDefinitionPath())
	if err != nil {
		t.Fatalf("read source def: %v", err)
	}
	if _, _, err := r.writeDefinition(sourceBytes); err != nil {
		t.Fatalf("writeDefinition: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(cfg.ProjectDir, "workshop.yaml"))
	if err != nil {
		t.Fatalf("derived def not written to <ProjectDir>/workshop.yaml: %v", err)
	}

	var got struct {
		Name string `yaml:"name"`
		Base string `yaml:"base"`
		SDKs []struct {
			Name    string `yaml:"name"`
			Channel string `yaml:"channel"`
		} `yaml:"sdks"`
		Actions map[string]struct {
			Command string `yaml:"command"`
		} `yaml:"actions"`
	}
	if err := yaml.Unmarshal(data, &got); err != nil {
		t.Fatalf("derived def is not valid YAML: %v\n%s", err, data)
	}

	// name overwritten with the agent workshop name; base inherited from source.
	if got.Name != cfg.Workshop {
		t.Errorf("name = %q, want %q (overwritten with cfg.Workshop)", got.Name, cfg.Workshop)
	}
	if got.Base != "ubuntu@24.04" {
		t.Errorf("base = %q, want ubuntu@24.04 (inherited from source)", got.Base)
	}

	// The source `go` sdk and its unmodeled channel survive; the agent SDK is appended.
	var goSDK, agentSDK bool
	for _, s := range got.SDKs {
		switch s.Name {
		case "go":
			goSDK = true
			if s.Channel != "1.26/stable" {
				t.Errorf("go sdk channel = %q, want 1.26/stable (carried through)", s.Channel)
			}
		case projectSDKRef(cfg.Agent.Name()): // "project-opencode"
			agentSDK = true
		}
	}
	if !goSDK {
		t.Errorf("source `go` sdk not preserved; sdks = %+v", got.SDKs)
	}
	if !agentSDK {
		t.Errorf("agent SDK %q not appended; sdks = %+v", projectSDKRef(cfg.Agent.Name()), got.SDKs)
	}

	// The unmodeled actions block is carried through verbatim.
	if a, ok := got.Actions["test"]; !ok {
		t.Errorf("actions block dropped; got %+v", got.Actions)
	} else if a.Command != "go test ./..." {
		t.Errorf("actions.test.command = %q, want %q", a.Command, "go test ./...")
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
	if !strings.Contains(err.Error(), "workshop.yaml") {
		t.Errorf("error %q does not name the missing workshop.yaml path", err)
	}
}

// TestReconcileProjectSDKs_AddsMissing pins the happy path: for a wanted
// in-project SDK, reconcile creates <projectDir>/.workshop/<name> as a SYMLINK
// pointing at the project's real SDK dir <repoPath>/.workshop/<name> — no copy,
// so reads through the link see the real (live) contents.
func TestReconcileProjectSDKs_AddsMissing(t *testing.T) {
	projectDir, repoPath := t.TempDir(), t.TempDir()

	// The project's real SDK dir, with a file we will read back through the link.
	realDir := filepath.Join(repoPath, ".workshop", "mylib")
	if err := os.MkdirAll(realDir, 0o750); err != nil {
		t.Fatalf("mkdir real sdk dir: %v", err)
	}
	const payload = "real sdk contents\n"
	if err := os.WriteFile(filepath.Join(realDir, "marker"), []byte(payload), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	if err := reconcileProjectSDKs(projectDir, repoPath, []string{"mylib"}); err != nil {
		t.Fatalf("reconcileProjectSDKs: %v", err)
	}

	link := filepath.Join(projectDir, ".workshop", "mylib")
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat link: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is not a symlink (mode %v); want a symlink", link, fi.Mode())
	}
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != realDir {
		t.Errorf("link target = %q, want %q", target, realDir)
	}

	// Reading THROUGH the link must yield the real SDK's contents (proves it
	// points at the live dir, not a copy).
	got, err := os.ReadFile(filepath.Join(link, "marker"))
	if err != nil {
		t.Fatalf("read marker through link: %v", err)
	}
	if string(got) != payload {
		t.Errorf("read through link = %q, want %q", got, payload)
	}
}

// TestReconcileProjectSDKs_PrunesStale is the data-loss guard. The project's
// .workshop/ holds a mix: a STALE symlink no longer wanted, AND the seeded agent
// SDK as a REAL directory. Reconcile must prune the stale link but key pruning on
// the SYMLINK BIT — never membership — so the real agent-SDK dir (and its file)
// survives, and the symlink's target dir is never followed or removed.
func TestReconcileProjectSDKs_PrunesStale(t *testing.T) {
	projectDir, repoPath := t.TempDir(), t.TempDir()
	pws := filepath.Join(projectDir, ".workshop")
	if err := os.MkdirAll(pws, 0o750); err != nil {
		t.Fatalf("mkdir project .workshop: %v", err)
	}

	// A STALE symlink in the project .workshop, pointing at some real dir whose
	// contents must remain untouched (reconcile must never follow/remove it).
	staleTargetDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(staleTargetDir, "keep"), []byte("untouched\n"), 0o600); err != nil {
		t.Fatalf("write stale target file: %v", err)
	}
	oldlink := filepath.Join(pws, "oldlib")
	if err := os.Symlink(staleTargetDir, oldlink); err != nil {
		t.Fatalf("symlink oldlib: %v", err)
	}

	// The seeded agent SDK as a REAL directory with a sentinel file. Pruning by
	// membership (oldlib + opencode are both "not wanted") would delete this —
	// the data-loss bug this test guards against.
	realAgentDir := filepath.Join(pws, "opencode")
	if err := os.MkdirAll(realAgentDir, 0o750); err != nil {
		t.Fatalf("mkdir real agent dir: %v", err)
	}
	sentinel := filepath.Join(realAgentDir, "sentinel")
	if err := os.WriteFile(sentinel, []byte("agent sdk\n"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	// The wanted SDK's real source dir.
	if err := os.MkdirAll(filepath.Join(repoPath, ".workshop", "mylib"), 0o750); err != nil {
		t.Fatalf("mkdir wanted source dir: %v", err)
	}

	if err := reconcileProjectSDKs(projectDir, repoPath, []string{"mylib"}); err != nil {
		t.Fatalf("reconcileProjectSDKs: %v", err)
	}

	// The stale symlink is gone.
	if _, err := os.Lstat(oldlink); !os.IsNotExist(err) {
		t.Errorf("stale symlink oldlib still present (err=%v); want it pruned", err)
	}
	// The wanted symlink now exists and is a symlink.
	newlink := filepath.Join(pws, "mylib")
	fi, err := os.Lstat(newlink)
	if err != nil {
		t.Fatalf("wanted symlink mylib not created: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("mylib is not a symlink (mode %v)", fi.Mode())
	}
	// The REAL agent-SDK dir and its sentinel survive — pruning keyed on the
	// symlink bit, not membership, and never recursed into a real dir.
	if fi, err := os.Lstat(realAgentDir); err != nil || !fi.IsDir() {
		t.Errorf("real agent SDK dir deleted or clobbered (err=%v); data-loss bug", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("sentinel file in real agent SDK dir gone (err=%v); data-loss bug", err)
	}
	// The stale link's TARGET dir contents were never followed/removed.
	if _, err := os.Stat(filepath.Join(staleTargetDir, "keep")); err != nil {
		t.Errorf("stale link's target dir contents removed (err=%v); reconcile followed the link", err)
	}
}

// reconcileProjectSDKs is idempotent (a second run is a no-op) and never clobbers
// a REAL directory that already occupies a wanted name — it only manages symlinks.
// A real wanted-name dir is a data-loss hazard a membership-based "ensure" would
// overwrite; this pins the add-path's clobber guard.
func TestReconcileProjectSDKs_IdempotentAndSparesRealWantedName(t *testing.T) {
	projectDir, repoPath := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoPath, ".workshop", "mylib"), 0o750); err != nil {
		t.Fatalf("mkdir wanted source dir: %v", err)
	}
	// A REAL dir already occupying a WANTED name, with a precious file.
	realWanted := filepath.Join(projectDir, ".workshop", "realsdk")
	if err := os.MkdirAll(realWanted, 0o750); err != nil {
		t.Fatalf("mkdir real wanted dir: %v", err)
	}
	sentinel := filepath.Join(realWanted, "keep.txt")
	if err := os.WriteFile(sentinel, []byte("precious"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	// Run twice: the second pass must be a no-op, not a re-link or a clobber.
	for i := range 2 {
		if err := reconcileProjectSDKs(projectDir, repoPath, []string{"mylib", "realsdk"}); err != nil {
			t.Fatalf("reconcileProjectSDKs pass %d: %v", i, err)
		}
	}

	// mylib became a symlink to the project's real source SDK.
	link := filepath.Join(projectDir, ".workshop", "mylib")
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("mylib is not a symlink (err=%v)", err)
	}
	// realsdk stayed a REAL dir (never clobbered into a symlink) and kept its file.
	fi, err := os.Lstat(realWanted)
	if err != nil {
		t.Fatalf("real wanted-name dir gone: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Error("real wanted-name dir was clobbered into a symlink; data-loss bug")
	}
	if b, err := os.ReadFile(sentinel); err != nil || string(b) != "precious" {
		t.Errorf("real wanted-name dir contents lost (got %q, err=%v)", b, err)
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

	cfg := Config{
		Workshop:   "taboo-run",
		Base:       "ubuntu@24.04",
		Agent:      OpenCode(openCodeModel),
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
		errFn: func(c Cmd) error {
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
	fp1 := r.readFingerprint()
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
	if fp2 := r.readFingerprint(); fp2 == fp1 {
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

// TestSeedSDK_OnlyConfiguredAgent checks that seedSDK seeds only the configured
// agent's SDK tree into .workshop, not every embedded SDK. The configured
// agent's tree must be present so its "project-<agent>" reference resolves; the
// other trees are dead clutter and must not be copied in. The seeding is
// parametric over the agent, so this is asserted for every registered agent,
// not just opencode.
func TestSeedSDK_OnlyConfiguredAgent(t *testing.T) {
	// The negative assertion below must range over the *real* full set of
	// embedded SDK trees, so read it from the source of truth (sdkFS) rather
	// than a hand-maintained literal: a newly added sdk/<x>/ is then covered by
	// the "not seeded" check automatically.
	entries, err := sdkFS.ReadDir("sdk")
	if err != nil {
		t.Fatalf("read embedded sdk/: %v", err)
	}
	var embeddedSDKs []string
	for _, e := range entries {
		if e.IsDir() {
			embeddedSDKs = append(embeddedSDKs, e.Name())
		}
	}

	// Drive the loop from the registered roster so the "any configured agent"
	// claim is load-bearing and future agents are covered without edits.
	for _, name := range AgentNames() {
		t.Run(name, func(t *testing.T) {
			agent, err := NewProfile(name, "")
			if err != nil {
				t.Fatalf("NewProfile(%q): %v", name, err)
			}
			cfg := testConfig(t) // fresh ProjectDir (t.TempDir)
			cfg.Agent = agent
			r := New(cfg, &fakeCommander{})

			if err := r.seedSDK(); err != nil {
				t.Fatalf("seedSDK: %v", err)
			}

			// The configured agent's SDK is seeded.
			sdkYAML := filepath.Join(cfg.ProjectDir, ".workshop", name, "sdk.yaml")
			if _, err := os.Stat(sdkYAML); err != nil {
				t.Errorf("configured agent SDK not seeded: %v", err)
			}

			// No other embedded SDK tree is seeded.
			for _, other := range embeddedSDKs {
				if other == name {
					continue
				}
				dir := filepath.Join(cfg.ProjectDir, ".workshop", other)
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Errorf("unconfigured SDK %q seeded (err=%v); want it absent", other, err)
				}
			}
		})
	}
}

// findCallN returns the nth (0-based) recorded Cmd whose verb matches, or fails.
func (f *fakeCommander) findCallN(t *testing.T, verb string, n int) Cmd {
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
	return Cmd{}
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
	wantWs := remountArgs(cfg.ProjectDir, cfg.Workshop, cfg.Agent.Name(), "workspace", res.WorktreePath)
	if got := fc.findCallN(t, "remount", 0).Args; !slices.Equal(got, wantWs) {
		t.Errorf("workspace remount args =\n  %v\nwant\n  %v", got, wantWs)
	}
	// The gitcommon source is the host .git absolute path; derived from RepoPath
	// via the production helper (the load-bearing half of the two-mount rule),
	// which tracks the temp RepoPath testConfig allocates.
	wantGc := remountArgs(cfg.ProjectDir, cfg.Workshop, cfg.Agent.Name(), "gitcommon", gitCommonTarget(cfg.RepoPath))
	if got := fc.findCallN(t, "remount", 1).Args; !slices.Equal(got, wantGc) {
		t.Errorf("gitcommon remount args =\n  %v\nwant\n  %v", got, wantGc)
	}
	// Sessions mount: a host sessions dir under the project is bound so the
	// agent's session files survive the swap. It is the third remount.
	wantSess := remountArgs(cfg.ProjectDir, cfg.Workshop, cfg.Agent.Name(), "sessions", filepath.Join(cfg.ProjectDir, "sessions"))
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
	want := spec.DirEnv + "=" + sessionsTarget // e.g. XDG_DATA_HOME=/taboo/sessions
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
		stdoutFn: func(c Cmd) string {
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
				stdoutFn: func(c Cmd) string {
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
		stdoutFn: func(c Cmd) string {
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

	cfg := Config{
		Workshop:   "taboo-run",
		Base:       "ubuntu@24.04",
		Agent:      OpenCode(openCodeModel),
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

func (stdinProfile) Name() string { return "opencode" }
func (stdinProfile) BuildCommand(o CommandOptions) AgentCommand {
	return AgentCommand{Argv: []string{"claude", "--print", "-p", "-"}, Stdin: o.Prompt}
}
func (stdinProfile) CredentialEnvKeys() []string   { return nil }
func (stdinProfile) Sessions() (SessionSpec, bool) { return SessionSpec{}, false }

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
