package workshop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/josecabralf/taboo/pkg/internal/agent"
)

// testConfig builds a Config pointing at a fresh repo (seeded with a project
// workshop.yaml) and a fresh ProjectDir, mirroring the runner-side fixture the
// provisioning tests used before this logic moved into internal/workshop.
func testConfig(t *testing.T) Config {
	t.Helper()
	repo := t.TempDir()
	writeProjectDef(t, repo, "name: myproject\nbase: ubuntu@24.04\nsdks:\n  - name: go\n")
	return Config{
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

// sourceDefinitionPath points at the project's OWN workshop.yaml — the file the
// project's human developers already use — which lives at the repo root
// (RepoPath), never under taboo's ProjectDir (<repo>/.taboo). The anchor is
// load-bearing: a future accidental swap to ProjectDir would silently derive the
// agent's workshop from the wrong (or a non-existent) file, so the test pins both
// the exact path and the RepoPath/NOT-ProjectDir prefixes.
func TestSourceDefinitionPath_AnchorsOnRepoPath(t *testing.T) {
	cfg := testConfig(t)

	want := filepath.Join(cfg.RepoPath, "workshop.yaml")
	got, err := sourceDefinitionPath(cfg)
	if err != nil {
		t.Fatalf("sourceDefinitionPath() error: %v", err)
	}
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

// TestWriteDefinition_FingerprintIsStableForUnchangedSource pins the property the
// reuse fast path (acceptance #1) rests on: an unchanged project workshop.yaml
// derives to a byte-identical definition, hence the same fingerprint, so a run
// reuses the live workshop instead of churning a spurious refresh every time. It
// guards against a future derive change (e.g. an unsorted plug map) that would
// silently vary the digest from one run to the next.
func TestWriteDefinition_FingerprintIsStableForUnchangedSource(t *testing.T) {
	cfg := testConfig(t)
	source := []byte("name: myproject\nbase: ubuntu@24.04\nsdks:\n  - name: go\n")

	fp1, _, err := writeDefinition(cfg, source)
	if err != nil {
		t.Fatalf("writeDefinition (1): %v", err)
	}
	fp2, _, err := writeDefinition(cfg, source)
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

	srcPath, err := sourceDefinitionPath(cfg)
	if err != nil {
		t.Fatalf("sourceDefinitionPath: %v", err)
	}
	sourceBytes, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("read source def: %v", err)
	}
	if _, _, err := writeDefinition(cfg, sourceBytes); err != nil {
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
	for _, name := range agent.AgentNames() {
		t.Run(name, func(t *testing.T) {
			p, err := agent.NewProfile(name, "")
			if err != nil {
				t.Fatalf("NewProfile(%q): %v", name, err)
			}
			cfg := testConfig(t) // fresh ProjectDir (t.TempDir)
			cfg.Agent = p

			if err := seedSDK(cfg); err != nil {
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
