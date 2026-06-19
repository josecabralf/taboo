package taborun

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/josecabralf/taboo/pkg/taboo"
)

// writeFixture lays down a taboo.yaml mirroring the real .taboo/taboo.yaml plus
// the referenced prompt file in a temp dir, returning the config path. The
// prompt body references the three issue vars the implement workflow fills.
func writeFixture(t *testing.T, prompt string) string {
	t.Helper()
	dir := t.TempDir()

	cfg := `workshop: taboo
base: ubuntu@24.04
repo: .
agent: opencode
model: openrouter/qwen/qwen3.7-max
strategy: branch
defaults:
  branch-prefix: agent/
  timeout: 30m
  max-iterations: 1
workflows:
  implement:
    prompt-file: prompts/implement.md
    model: openrouter/qwen/qwen3.7-max
`
	cfgPath := filepath.Join(dir, "taboo.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write taboo.yaml: %v", err)
	}
	promptDir := filepath.Join(dir, "prompts")
	if err := os.MkdirAll(promptDir, 0o750); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(promptDir, "implement.md"), []byte(prompt), 0o600); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	return cfgPath
}

func loadFixture(t *testing.T, cfgPath string) *taboo.ProjectConfig {
	t.Helper()
	pc, err := taboo.LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	return pc
}

func TestBuildPlanResolvesWorkflow(t *testing.T) {
	cfgPath := writeFixture(t, "{{ISSUE_NUMBER}} {{ISSUE_TITLE}} {{ISSUE_BODY}}")
	pc := loadFixture(t, cfgPath)

	opts := Options{
		ConfigPath: cfgPath,
		Workflow:   "implement",
		Branch:     "agent/issue-42",
		Vars: map[string]string{
			"ISSUE_NUMBER": "42",
			"ISSUE_TITLE":  "My title",
			"ISSUE_BODY":   "My body",
		},
		RepoPath:   "/host/repo",
		ProjectDir: "/host/repo/.taboo",
	}

	p, err := buildPlan(pc, opts)
	if err != nil {
		t.Fatalf("buildPlan: %v", err)
	}

	if got, want := p.cfg.Workshop, "taboo-opencode"; got != want {
		t.Errorf("cfg.Workshop = %q, want %q", got, want)
	}
	if got, want := p.cfg.Base, "ubuntu@24.04"; got != want {
		t.Errorf("cfg.Base = %q, want %q", got, want)
	}
	if p.cfg.Agent == nil {
		t.Fatal("cfg.Agent is nil")
	}
	if got, want := p.cfg.Agent.Name(), "opencode"; got != want {
		t.Errorf("cfg.Agent.Name() = %q, want %q", got, want)
	}
	if got, want := p.cfg.RepoPath, opts.RepoPath; got != want {
		t.Errorf("cfg.RepoPath = %q, want %q", got, want)
	}
	if got, want := p.cfg.ProjectDir, opts.ProjectDir; got != want {
		t.Errorf("cfg.ProjectDir = %q, want %q", got, want)
	}

	if got, want := p.req.Branch, opts.Branch; got != want {
		t.Errorf("req.Branch = %q, want %q", got, want)
	}
	if got, want := p.req.Prompt, "42 My title My body"; got != want {
		t.Errorf("req.Prompt = %q, want %q", got, want)
	}
	if got, want := p.req.MaxIterations, 1; got != want {
		t.Errorf("req.MaxIterations = %d, want %d", got, want)
	}
	if got, want := p.req.Timeout, 30*time.Minute; got != want {
		t.Errorf("req.Timeout = %v, want %v", got, want)
	}
}

func TestBuildPlanUnknownWorkflow(t *testing.T) {
	cfgPath := writeFixture(t, "{{ISSUE_NUMBER}}")
	pc := loadFixture(t, cfgPath)

	_, err := buildPlan(pc, Options{ConfigPath: cfgPath, Workflow: "nope"})
	if err == nil {
		t.Fatal("buildPlan with unknown workflow: want error, got nil")
	}
}

func TestBuildPlanMissingVarErrors(t *testing.T) {
	cfgPath := writeFixture(t, "{{ISSUE_NUMBER}} {{ISSUE_TITLE}}")
	pc := loadFixture(t, cfgPath)

	// ISSUE_TITLE is referenced by the prompt but absent from Vars.
	_, err := buildPlan(pc, Options{
		ConfigPath: cfgPath,
		Workflow:   "implement",
		Vars:       map[string]string{"ISSUE_NUMBER": "42"},
	})
	if err == nil {
		t.Fatal("buildPlan with missing prompt var: want error, got nil")
	}
}
