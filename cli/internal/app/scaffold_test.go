package app

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	taboo "github.com/josecabralf/taboo/pkg"
)

// newScaffoldInputs builds scaffoldInputs for agent/model with a resolved
// profile, failing the test if the agent is unknown.
func newScaffoldInputs(t *testing.T, agent, model string) scaffoldInputs {
	t.Helper()
	profile, err := taboo.NewProfile(agent, model)
	if err != nil {
		t.Fatalf("NewProfile(%q, %q): %v", agent, model, err)
	}
	return scaffoldInputs{
		Workshop: "demo",
		Base:     "ubuntu@24.04",
		Repo:     "/home/me/demo",
		Agent:    agent,
		Model:    model,
		Profile:  profile,
	}
}

// TestPlan_StableOrder asserts plan() returns the three scaffold files in the
// fixed order taboo.yaml, .gitignore, .env.example.
func TestPlan_StableOrder(t *testing.T) {
	t.Parallel()
	in := newScaffoldInputs(t, "opencode", "some/model")
	files, err := in.plan()
	if err != nil {
		t.Fatalf("plan(): %v", err)
	}
	want := []string{"taboo.yaml", ".gitignore", ".env.example"}
	if len(files) != len(want) {
		t.Fatalf("plan() = %d files, want %d: %+v", len(files), len(want), files)
	}
	for i, w := range want {
		if files[i].Path != w {
			t.Errorf("plan()[%d].Path = %q, want %q", i, files[i].Path, w)
		}
	}
}

// TestPlan_SeedsPromptFiles asserts plan() appends non-empty prompts/fix.md and
// prompts/refactor.md when SeedWorkflows is set, and emits no prompts/ files when
// it is not.
func TestPlan_SeedsPromptFiles(t *testing.T) {
	t.Parallel()
	in := newScaffoldInputs(t, "opencode", "some/model")
	in.SeedWorkflows = true
	files, err := in.plan()
	if err != nil {
		t.Fatalf("plan(): %v", err)
	}
	byPath := map[string][]byte{}
	for _, f := range files {
		byPath[f.Path] = f.Contents
	}
	for _, want := range []string{"prompts/fix.md", "prompts/refactor.md"} {
		if len(byPath[want]) == 0 {
			t.Errorf("plan() missing or empty %q", want)
		}
	}

	off := newScaffoldInputs(t, "opencode", "some/model")
	offFiles, err := off.plan()
	if err != nil {
		t.Fatalf("plan() (no seed): %v", err)
	}
	for _, f := range offFiles {
		if strings.HasPrefix(f.Path, "prompts/") {
			t.Errorf("plan() without SeedWorkflows emitted %q", f.Path)
		}
	}
}

// TestPlan_TemplateSingle asserts plan() adds a parseable main.go (importing
// pkg and calling RunWorkflow, the one-call bridge) and a go.mod that names the module after the
// workshop, carries the go directive, and pins the taboo library to the exact
// libraryVersion with no replace and no @-pinned/latest version when Template is
// "single", and adds neither file when Template is "none" or empty.
func TestPlan_TemplateSingle(t *testing.T) {
	t.Parallel()
	in := newScaffoldInputs(t, "opencode", "some/model")
	in.Template = "single"
	files, err := in.plan()
	if err != nil {
		t.Fatalf("plan(): %v", err)
	}
	byPath := map[string][]byte{}
	for _, f := range files {
		byPath[f.Path] = f.Contents
	}

	if byPath["main.go"] == nil {
		t.Fatalf("plan() missing main.go")
	}
	fset := token.NewFileSet()
	if _, perr := parser.ParseFile(fset, "main.go", byPath["main.go"], parser.AllErrors); perr != nil {
		t.Errorf("main.go does not parse: %v", perr)
	}
	main := string(byPath["main.go"])
	if !strings.Contains(main, "github.com/josecabralf/taboo/pkg") {
		t.Errorf("main.go missing taboo import\nfull:\n%s", main)
	}
	if !strings.Contains(main, "RunWorkflow") {
		t.Errorf("main.go missing RunWorkflow call\nfull:\n%s", main)
	}
	// A scaffolded main.go must be a runnable main package: package main, not the
	// CLI's own package app (which would compile as a library and break go run .).
	if !strings.Contains(main, "\npackage main\n") {
		t.Errorf("main.go must declare package main, got:\n%s", main)
	}

	if byPath["go.mod"] == nil {
		t.Fatalf("plan() missing go.mod")
	}
	mod := string(byPath["go.mod"])
	// newScaffoldInputs uses Workshop "demo", and renderGoMod names the module
	// after the workshop — lock that naming contract, not just any module line.
	if !strings.Contains(mod, "module demo\n") {
		t.Errorf("go.mod should name the module after the workshop (module demo)\nfull:\n%s", mod)
	}
	if !strings.Contains(mod, "go "+scaffoldGoVersion) {
		t.Errorf("go.mod missing go directive for %q\nfull:\n%s", scaffoldGoVersion, mod)
	}
	// The reproducibility contract: the require line pins the exact libraryVersion
	// — no @-pinned pseudo-version, no @latest, no replace override.
	wantRequire := "require github.com/josecabralf/taboo/pkg " + libraryVersion + "\n"
	if !strings.Contains(mod, wantRequire) {
		t.Errorf("go.mod must pin the exact library version %q\nfull:\n%s", libraryVersion, mod)
	}
	if strings.Contains(mod, "replace") {
		t.Errorf("go.mod must not contain replace\nfull:\n%s", mod)
	}
	if strings.Contains(mod, "@") {
		t.Errorf("go.mod must not @-pin a version\nfull:\n%s", mod)
	}
	if strings.Contains(mod, "latest") {
		t.Errorf("go.mod must not contain latest\nfull:\n%s", mod)
	}

	// none: emits no Go files.
	off := newScaffoldInputs(t, "opencode", "some/model")
	off.Template = "none"
	offFiles, err := off.plan()
	if err != nil {
		t.Fatalf("plan() (none): %v", err)
	}
	for _, f := range offFiles {
		if f.Path == "main.go" || f.Path == "go.mod" {
			t.Errorf("plan() with Template none emitted %q", f.Path)
		}
	}

	// Empty (the zero value from newScaffoldInputs) is treated like "none": plan()
	// guards on Template != "" && != "none", so neither Go file is emitted.
	zero := newScaffoldInputs(t, "opencode", "some/model")
	if zero.Template != "" {
		t.Fatalf("newScaffoldInputs Template = %q, want zero value", zero.Template)
	}
	zeroFiles, err := zero.plan()
	if err != nil {
		t.Fatalf("plan() (empty template): %v", err)
	}
	for _, f := range zeroFiles {
		if f.Path == "main.go" || f.Path == "go.mod" {
			t.Errorf("plan() with empty Template emitted %q", f.Path)
		}
	}
}

// TestPlan_TemplateFanout asserts plan() adds a parseable main.go that
// demonstrates parallel fan-out (taboo.NewPool) and structured output
// (taboo.JSONResult) when Template is "fanout".
func TestPlan_TemplateFanout(t *testing.T) {
	t.Parallel()
	in := newScaffoldInputs(t, "opencode", "some/model")
	in.Template = "fanout"
	files, err := in.plan()
	if err != nil {
		t.Fatalf("plan(): %v", err)
	}
	byPath := map[string][]byte{}
	for _, f := range files {
		byPath[f.Path] = f.Contents
	}

	if byPath["main.go"] == nil {
		t.Fatalf("plan() missing main.go")
	}
	fset := token.NewFileSet()
	if _, perr := parser.ParseFile(fset, "main.go", byPath["main.go"], parser.AllErrors); perr != nil {
		t.Errorf("main.go does not parse: %v", perr)
	}
	main := string(byPath["main.go"])
	if !strings.Contains(main, "NewPool") {
		t.Errorf("main.go missing NewPool\nfull:\n%s", main)
	}
	if !strings.Contains(main, "JSONResult") {
		t.Errorf("main.go missing JSONResult\nfull:\n%s", main)
	}
	// A scaffolded main.go must be a runnable main package: package main, not the
	// CLI's own package app (which would compile as a library and break go run .).
	if !strings.Contains(main, "\npackage main\n") {
		t.Errorf("main.go must declare package main, got:\n%s", main)
	}
}

// TestRenderTabooYAML_RoundTrips asserts the marshaled taboo.yaml loads back
// through taboo.LoadConfig with every scalar preserved and strategy defaulted to
// "branch".
func TestRenderTabooYAML_RoundTrips(t *testing.T) {
	t.Parallel()
	in := newScaffoldInputs(t, "opencode", "some/model")
	data, err := renderTabooYAML(in)
	if err != nil {
		t.Fatalf("renderTabooYAML: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "taboo.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write taboo.yaml: %v", err)
	}
	cfg, err := taboo.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Agent != "opencode" {
		t.Errorf("cfg.Agent = %q, want opencode", cfg.Agent)
	}
	if cfg.Model != "some/model" {
		t.Errorf("cfg.Model = %q, want some/model", cfg.Model)
	}
	if cfg.Workshop != "demo" {
		t.Errorf("cfg.Workshop = %q, want demo", cfg.Workshop)
	}
	if cfg.Base != "ubuntu@24.04" {
		t.Errorf("cfg.Base = %q, want ubuntu@24.04", cfg.Base)
	}
	if cfg.Repo != "/home/me/demo" {
		t.Errorf("cfg.Repo = %q, want /home/me/demo", cfg.Repo)
	}
	if cfg.Strategy != "branch" {
		t.Errorf("cfg.Strategy = %q, want branch", cfg.Strategy)
	}
}

// TestRenderTabooYAML_RoundTripsSourceDefinition asserts that a SourceDefinition
// name set on the inputs is written into taboo.yaml and round-trips back through
// taboo.LoadConfig as ProjectConfig.SourceDefinition.
func TestRenderTabooYAML_RoundTripsSourceDefinition(t *testing.T) {
	t.Parallel()
	in := newScaffoldInputs(t, "opencode", "some/model")
	in.SourceDefinition = "api"
	data, err := renderTabooYAML(in)
	if err != nil {
		t.Fatalf("renderTabooYAML: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "taboo.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write taboo.yaml: %v", err)
	}
	cfg, err := taboo.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.SourceDefinition != "api" {
		t.Errorf("cfg.SourceDefinition = %q, want api", cfg.SourceDefinition)
	}
}

// TestRenderTabooYAML_SeedsWorkflows asserts that with SeedWorkflows set, the
// marshaled taboo.yaml carries a real workflows: block (fix and refactor, each
// pointing at a prompt file) and default-workflow: fix, all round-tripping
// through taboo.LoadConfig.
func TestRenderTabooYAML_SeedsWorkflows(t *testing.T) {
	t.Parallel()
	in := newScaffoldInputs(t, "opencode", "some/model")
	in.SeedWorkflows = true
	data, err := renderTabooYAML(in)
	if err != nil {
		t.Fatalf("renderTabooYAML: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "taboo.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write taboo.yaml: %v", err)
	}
	cfg, err := taboo.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.DefaultWorkflow != "fix" {
		t.Errorf("cfg.DefaultWorkflow = %q, want fix", cfg.DefaultWorkflow)
	}
	if got := cfg.Workflows["fix"].PromptFile; got != "prompts/fix.md" {
		t.Errorf("cfg.Workflows[fix].PromptFile = %q, want prompts/fix.md", got)
	}
	if got := cfg.Workflows["refactor"].PromptFile; got != "prompts/refactor.md" {
		t.Errorf("cfg.Workflows[refactor].PromptFile = %q, want prompts/refactor.md", got)
	}
}

// TestRenderGitignore_Entries asserts .gitignore contains exactly the six
// ignore entries, each on its own line.
func TestRenderGitignore_Entries(t *testing.T) {
	t.Parallel()
	data := renderGitignore()
	lines := map[string]bool{}
	for _, l := range strings.Split(string(data), "\n") {
		lines[strings.TrimSpace(l)] = true
	}
	for _, want := range []string{"worktrees/", ".workshop/", "/workshop.yaml", "/workshop.fingerprint", ".env", "logs/"} {
		if !lines[want] {
			t.Errorf(".gitignore missing entry %q\nfull:\n%s", want, data)
		}
	}
}

// TestRenderEnvExample_Keys asserts .env.example lists the chosen agent's
// credential env keys, one KEY= line each. One representative multi-key agent
// suffices; the full per-agent key sets are owned by pkg/taboo/agent_*_test.go.
func TestRenderEnvExample_Keys(t *testing.T) {
	t.Parallel()
	in := newScaffoldInputs(t, "claude-code", "some/model")
	data := string(renderEnvExample(in))
	for _, key := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if !strings.Contains(data, key+"=") {
			t.Errorf(".env.example missing %q=\nfull:\n%s", key, data)
		}
	}
}

// TestWriteScaffold_WritesTreeWithModes asserts writeScaffold creates the
// project dir and every planned file with mode 0600.
func TestWriteScaffold_WritesTreeWithModes(t *testing.T) {
	t.Parallel()
	in := newScaffoldInputs(t, "opencode", "some/model")
	files, err := in.plan()
	if err != nil {
		t.Fatalf("plan(): %v", err)
	}
	projectDir := filepath.Join(t.TempDir(), ".taboo")
	if err := writeScaffold(projectDir, files); err != nil {
		t.Fatalf("writeScaffold: %v", err)
	}
	for _, name := range []string{"taboo.yaml", ".gitignore", ".env.example"} {
		info, err := os.Stat(filepath.Join(projectDir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if got := info.Mode().Perm(); got != fs.FileMode(0o600) {
			t.Errorf("%s mode = %o, want 600", name, got)
		}
	}
}

// TestDeriveWorkshopName covers slugification: case-folding, separator
// collapsing, trimming, and the empty fallback.
func TestDeriveWorkshopName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want string
	}{
		{in: "/home/me/My_Repo", want: "my-repo"},
		{in: "/home/me/demo", want: "demo"},
		{in: "/home/me/demo/", want: "demo"},
		{in: "/home/me/Weird!!Name@@", want: "weird-name"},
		{in: "/home/me/__leading", want: "leading"},
		{in: "/home/me/UPPER", want: "upper"},
		{in: "/", want: "taboo"},
		{in: "///", want: "taboo"},
		{in: "/home/me/123abc", want: "123abc"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			if got := deriveWorkshopName(tt.in); got != tt.want {
				t.Errorf("deriveWorkshopName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
