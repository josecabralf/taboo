package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	taboo "github.com/josecabralf/taboo/pkg/taboo"
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

// TestRenderGitignore_Entries asserts .gitignore contains exactly the four
// ignore entries, each on its own line.
func TestRenderGitignore_Entries(t *testing.T) {
	t.Parallel()
	data := renderGitignore()
	lines := map[string]bool{}
	for _, l := range strings.Split(string(data), "\n") {
		lines[strings.TrimSpace(l)] = true
	}
	for _, want := range []string{"worktrees/", ".workshop/", ".env", "logs/"} {
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
