package taboo

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// findRepoWorkshopYAML locates and reads taboo's OWN workshop.yaml at the repo
// root. The test CWD is <repo>/pkg/taboo, so it walks up from the working dir
// until it finds a directory holding go.mod (the repo root marker) and reads the
// workshop.yaml there; it falls back to ../../workshop.yaml. The dogfood must
// actually run, so a genuine miss is a t.Fatal, never a silent skip.
func findRepoWorkshopYAML(t *testing.T) []byte {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			data, err := os.ReadFile(filepath.Join(dir, "workshop.yaml"))
			if err != nil {
				t.Fatalf("found repo root %q but could not read its workshop.yaml: %v", dir, err)
			}
			return data
		}
		parent := filepath.Dir(dir)
		if parent == dir { // reached filesystem root without a go.mod
			break
		}
		dir = parent
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "workshop.yaml"))
	if err != nil {
		t.Fatalf("could not locate taboo's own workshop.yaml (walked up from CWD for go.mod, then ../../workshop.yaml): %v", err)
	}
	return data
}

// findSDK returns the SDK entry named name from a decoded sdks list, or nil.
func findSDK(sdks []any, name string) map[string]any {
	for _, s := range sdks {
		m, ok := s.(map[string]any)
		if !ok {
			continue
		}
		if m["name"] == name {
			return m
		}
	}
	return nil
}

// deriveDefinition derives the agent workshop from the project's own
// workshop.yaml: it overwrites `name:` and appends the agent SDK to `sdks:`
// while leaving everything taboo does not model (here `base:`, the source SDK's
// `channel:`, and `actions:`) verbatim. Asserting on decoded values rather than
// raw bytes keeps the test immune to yaml.Marshal's whitespace/quoting.
func TestDeriveDefinition_PreservesUnmodeledFields(t *testing.T) {
	source := []byte(`name: myproject
base: ubuntu@24.04
sdks:
  - name: go
    channel: 1.26/stable
actions:
  make: |
    make "$@"
`)
	cfg := Config{
		Workshop: "taboo-run-abc",
		Base:     "ubuntu@99.99", // deliberately != source base; derive must inherit source base
		Agent:    OpenCode(openCodeModel),
		RepoPath: "/home/dev/repos/myproject",
	}

	out, err := deriveDefinition(cfg, source)
	if err != nil {
		t.Fatalf("deriveDefinition: %v", err)
	}

	var def map[string]any
	if err := yaml.Unmarshal([]byte(out), &def); err != nil {
		t.Fatalf("derived definition is not valid YAML: %v\n%s", err, out)
	}

	if def["name"] != "taboo-run-abc" {
		t.Errorf("name = %v, want taboo-run-abc (overwritten)", def["name"])
	}
	// base is inherited from the source, NOT taken from cfg.Base.
	if def["base"] != "ubuntu@24.04" {
		t.Errorf("base = %v, want ubuntu@24.04 (inherited, not cfg.Base)", def["base"])
	}

	// actions: is unmodeled and must survive verbatim.
	actions, ok := def["actions"].(map[string]any)
	if !ok {
		t.Fatalf("actions = %v, want a mapping", def["actions"])
	}
	if _, ok := actions["make"]; !ok {
		t.Errorf("actions.make missing, want preserved")
	}

	sdks, ok := def["sdks"].([]any)
	if !ok {
		t.Fatalf("sdks = %v, want a sequence", def["sdks"])
	}
	if len(sdks) != 2 {
		t.Fatalf("got %d sdks, want 2 (go + project-opencode)", len(sdks))
	}

	// The source `go` SDK survives, including its unmodeled channel.
	goSDK := findSDK(sdks, "go")
	if goSDK == nil {
		t.Fatalf("go sdk missing, want preserved; got %v", sdks)
	}
	if goSDK["channel"] != "1.26/stable" {
		t.Errorf("go sdk channel = %v, want 1.26/stable (unmodeled, preserved)", goSDK["channel"])
	}

	// The injected agent SDK carries the workspace + gitcommon mount plugs.
	agentSDK := findSDK(sdks, "project-opencode")
	if agentSDK == nil {
		t.Fatalf("project-opencode sdk missing, want appended; got %v", sdks)
	}
	plugs, ok := agentSDK["plugs"].(map[string]any)
	if !ok {
		t.Fatalf("agent sdk plugs = %v, want a mapping", agentSDK["plugs"])
	}
	assertMountPlug(t, plugs, "workspace", "/taboo/workspace")
	assertMountPlug(t, plugs, "gitcommon", "/home/dev/repos/myproject/.git")
}

// When the source has no sdks: key, derive creates one holding just the agent.
func TestDeriveDefinition_CreatesSdksWhenAbsent(t *testing.T) {
	source := []byte("name: bare\nbase: ubuntu@24.04\n")
	cfg := Config{
		Workshop: "taboo-run-abc",
		Base:     "ubuntu@99.99",
		Agent:    OpenCode(openCodeModel),
		RepoPath: "/home/dev/repos/myproject",
	}

	out, err := deriveDefinition(cfg, source)
	if err != nil {
		t.Fatalf("deriveDefinition: %v", err)
	}

	var def map[string]any
	if err := yaml.Unmarshal([]byte(out), &def); err != nil {
		t.Fatalf("derived definition is not valid YAML: %v\n%s", err, out)
	}

	sdks, ok := def["sdks"].([]any)
	if !ok {
		t.Fatalf("sdks = %v, want a sequence", def["sdks"])
	}
	if len(sdks) != 1 {
		t.Fatalf("got %d sdks, want 1 (project-opencode)", len(sdks))
	}
	agentSDK := findSDK(sdks, "project-opencode")
	if agentSDK == nil {
		t.Fatalf("project-opencode sdk missing; got %v", sdks)
	}
	plugs, ok := agentSDK["plugs"].(map[string]any)
	if !ok {
		t.Fatalf("agent sdk plugs = %v, want a mapping", agentSDK["plugs"])
	}
	assertMountPlug(t, plugs, "workspace", "/taboo/workspace")
	assertMountPlug(t, plugs, "gitcommon", "/home/dev/repos/myproject/.git")
}

// Per ADR 0009, taboo's two RELOCATABLE mount targets move under a reserved
// `/taboo/...` prefix so they cannot collide with the project's own mounts in
// the shared in-workshop namespace. The git-common target is NOT namespaced:
// its path IS the mechanism (the two-mount rule), so it stays at the host
// .git absolute path.
func TestDeriveDefinition_NamespacesWorkspaceAndSessionsTargets(t *testing.T) {
	source := []byte("name: p\nbase: ubuntu@24.04\n")
	cfg := Config{
		Workshop: "taboo-run-abc",
		Agent:    OpenCode(openCodeModel), // session-capable: gets a sessions plug
		RepoPath: "/home/dev/repos/myproject",
	}

	out, err := deriveDefinition(cfg, source)
	if err != nil {
		t.Fatalf("deriveDefinition: %v", err)
	}

	var def map[string]any
	if err := yaml.Unmarshal([]byte(out), &def); err != nil {
		t.Fatalf("derived definition is not valid YAML: %v\n%s", err, out)
	}

	sdks, ok := def["sdks"].([]any)
	if !ok {
		t.Fatalf("sdks = %v, want a sequence", def["sdks"])
	}
	agentSDK := findSDK(sdks, "project-opencode")
	if agentSDK == nil {
		t.Fatalf("project-opencode sdk missing; got %v", sdks)
	}
	plugs, ok := agentSDK["plugs"].(map[string]any)
	if !ok {
		t.Fatalf("agent sdk plugs = %v, want a mapping", agentSDK["plugs"])
	}

	// The two relocatable targets move under /taboo/...
	assertMountPlug(t, plugs, "workspace", "/taboo/workspace")
	assertMountPlug(t, plugs, "sessions", "/taboo/sessions")
	// git-common is NOT namespaced: its path is the two-mount mechanism.
	assertMountPlug(t, plugs, "gitcommon", "/home/dev/repos/myproject/.git")
}

func assertMountPlug(t *testing.T, plugs map[string]any, name, target string) {
	t.Helper()
	p, ok := plugs[name].(map[string]any)
	if !ok {
		t.Fatalf("%s plug = %v, want a mapping", name, plugs[name])
	}
	if p["interface"] != "mount" {
		t.Errorf("%s plug interface = %v, want mount", name, p["interface"])
	}
	if p["workshop-target"] != target {
		t.Errorf("%s plug workshop-target = %v, want %s", name, p["workshop-target"], target)
	}
}

// TestDeriveDefinition_DogfoodTabooRepo is the true dogfood at the derive
// seam: it derives the agent workshop from taboo's OWN <repo>/workshop.yaml
// (read via the repo-root locator) and proves slices 1 + 2 compose end-to-end
// against the real definition. The cfg.Base is deliberately bogus to prove
// base is INHERITED from the source, not taken from cfg. Asserted on decoded
// values, so yaml.Marshal's formatting is not load-bearing.
func TestDeriveDefinition_DogfoodTabooRepo(t *testing.T) {
	source := findRepoWorkshopYAML(t)

	cfg := Config{
		Workshop: "taboo-dogfood",
		Base:     "ubuntu@00.00", // deliberately != source base; derive must inherit source base
		Agent:    OpenCode(openCodeModel),
		RepoPath: "/srv/taboo",
	}

	out, err := deriveDefinition(cfg, source)
	if err != nil {
		t.Fatalf("deriveDefinition: %v", err)
	}

	var def map[string]any
	if err := yaml.Unmarshal([]byte(out), &def); err != nil {
		t.Fatalf("derived definition is not valid YAML: %v\n%s", err, out)
	}

	// name is overwritten with the agent workshop; base is INHERITED from taboo's
	// own definition (ubuntu@24.04), NOT taken from the bogus cfg.Base.
	if def["name"] != "taboo-dogfood" {
		t.Errorf("name = %v, want taboo-dogfood (overwritten)", def["name"])
	}
	if def["base"] != "ubuntu@24.04" {
		t.Errorf("base = %v, want ubuntu@24.04 (inherited from source, not cfg.Base)", def["base"])
	}

	// actions.make is unmodeled and must survive verbatim.
	actions, ok := def["actions"].(map[string]any)
	if !ok {
		t.Fatalf("actions = %v, want a mapping", def["actions"])
	}
	if _, ok := actions["make"]; !ok {
		t.Errorf("actions.make missing, want preserved verbatim")
	}

	sdks, ok := def["sdks"].([]any)
	if !ok {
		t.Fatalf("sdks = %v, want a sequence", def["sdks"])
	}

	// The source `go` SDK survives WITH its unmodeled channel.
	goSDK := findSDK(sdks, "go")
	if goSDK == nil {
		t.Fatalf("go sdk missing, want preserved; got %v", sdks)
	}
	if goSDK["channel"] != "1.26/stable" {
		t.Errorf("go sdk channel = %v, want 1.26/stable (unmodeled, preserved)", goSDK["channel"])
	}

	// The source project-taboo SDK survives.
	if findSDK(sdks, "project-taboo") == nil {
		t.Errorf("project-taboo sdk missing, want preserved; got %v", sdks)
	}

	// The injected agent SDK is appended with NAMESPACED mount plugs: the two
	// relocatable targets move under /taboo/...; gitcommon stays at the host .git
	// absolute path (the two-mount mechanism, un-namespaced).
	agentSDK := findSDK(sdks, "project-opencode")
	if agentSDK == nil {
		t.Fatalf("project-opencode sdk missing, want appended; got %v", sdks)
	}
	plugs, ok := agentSDK["plugs"].(map[string]any)
	if !ok {
		t.Fatalf("agent sdk plugs = %v, want a mapping", agentSDK["plugs"])
	}
	assertMountPlug(t, plugs, "workspace", "/taboo/workspace")
	assertMountPlug(t, plugs, "sessions", "/taboo/sessions")
	assertMountPlug(t, plugs, "gitcommon", "/srv/taboo/.git")
}

// A sessionless agent gets NO sessions plug on its injected SDK: there is nothing
// to persist, so taboo mounts no sessions dir. This pins the negative branch of
// agentPlugs, re-homing the guarantee the retired renderDefinition tests held.
func TestDeriveDefinition_OmitsSessionsForSessionlessAgent(t *testing.T) {
	source := []byte("name: p\nbase: ubuntu@24.04\n")
	cfg := Config{
		Workshop: "taboo-run",
		Base:     "ubuntu@24.04",
		Agent:    stdinProfile{}, // Sessions() ok == false
		RepoPath: "/home/dev/repos/myproject",
	}

	out, err := deriveDefinition(cfg, source)
	if err != nil {
		t.Fatalf("deriveDefinition: %v", err)
	}
	var def map[string]any
	if err := yaml.Unmarshal([]byte(out), &def); err != nil {
		t.Fatalf("derived definition is not valid YAML: %v\n%s", err, out)
	}

	sdks, _ := def["sdks"].([]any)
	agentSDK := findSDK(sdks, "project-opencode")
	if agentSDK == nil {
		t.Fatalf("agent sdk missing; got %v", sdks)
	}
	plugs, ok := agentSDK["plugs"].(map[string]any)
	if !ok {
		t.Fatalf("agent sdk plugs = %v, want a mapping", agentSDK["plugs"])
	}
	if _, ok := plugs["sessions"]; ok {
		t.Error("sessions plug present for a sessionless agent, want none")
	}
	// The two always-on mounts are still there.
	assertMountPlug(t, plugs, "workspace", "/taboo/workspace")
	assertMountPlug(t, plugs, "gitcommon", "/home/dev/repos/myproject/.git")
}

// A source that is not a YAML mapping (an empty/comment-only file, a bare scalar,
// a top-level list) carries no name/sdks, so deriveDefinition rejects it with a
// clear error rather than panicking on the document node or emitting a malformed
// definition that only breaks later inside `workshop launch`.
func TestDeriveDefinition_RejectsNonMappingSource(t *testing.T) {
	cfg := Config{Workshop: "w", Base: "ubuntu@24.04", Agent: OpenCode(openCodeModel), RepoPath: "/r"}
	for _, tc := range []struct{ name, src string }{
		{"empty", ""},
		{"comment only", "# just a comment\n"},
		{"scalar root", "123\n"},
		{"sequence root", "- a\n- b\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := deriveDefinition(cfg, []byte(tc.src)); err == nil {
				t.Errorf("deriveDefinition(%q) error = nil, want a non-mapping error", tc.src)
			}
		})
	}
}
