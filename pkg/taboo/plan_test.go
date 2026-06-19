package taboo

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPlan_ResolvesNamedWorkflow is the end-to-end tracer for the pure resolver:
// a named workflow with an inline prompt under the adopter layout (config in a
// .taboo/ subdir of the repo, as opposed to a bare taboo.yaml at the repo root)
// resolves into a fully populated *Plan, with the config-anchored RepoPath made
// absolute and the ProjectDir == RepoPath/.taboo invariant holding.
func TestPlan_ResolvesNamedWorkflow(t *testing.T) {
	cfg := &ProjectConfig{
		Workshop: "ws",
		Base:     "ubuntu@24.04",
		Agent:    "opencode",
		Model:    "some-model",
		Workflows: map[string]Workflow{
			"implement": {
				Prompt:        "do the thing",
				MaxIterations: 3,
				Timeout:       Duration(30 * time.Minute),
			},
		},
	}

	configDir := filepath.Join(t.TempDir(), "repo", ".taboo")

	plan, err := cfg.Plan(configDir, "implement", nil, PlanOverrides{Branch: "my-branch"})
	if err != nil {
		t.Fatalf("Plan: unexpected error: %v", err)
	}

	if plan.Workflow != "implement" {
		t.Errorf("Workflow = %q, want %q", plan.Workflow, "implement")
	}
	if plan.Model != "some-model" {
		t.Errorf("Model = %q, want %q", plan.Model, "some-model")
	}

	if got := plan.Config.Agent.Name(); got != "opencode" {
		t.Errorf("Config.Agent.Name() = %q, want %q", got, "opencode")
	}
	if want := WorkshopName("ws", "opencode"); plan.Config.Workshop != want {
		t.Errorf("Config.Workshop = %q, want %q", plan.Config.Workshop, want)
	}
	if plan.Config.Base != "ubuntu@24.04" {
		t.Errorf("Config.Base = %q, want %q", plan.Config.Base, "ubuntu@24.04")
	}

	if !filepath.IsAbs(plan.Config.RepoPath) {
		t.Errorf("Config.RepoPath = %q, want absolute", plan.Config.RepoPath)
	}
	wantRepo, err := filepath.Abs(filepath.Dir(configDir))
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	if plan.Config.RepoPath != wantRepo {
		t.Errorf("Config.RepoPath = %q, want %q", plan.Config.RepoPath, wantRepo)
	}
	if want := filepath.Join(plan.Config.RepoPath, ".taboo"); plan.Config.ProjectDir != want {
		t.Errorf("Config.ProjectDir = %q, want %q", plan.Config.ProjectDir, want)
	}

	if plan.Request.Branch != "my-branch" {
		t.Errorf("Request.Branch = %q, want %q", plan.Request.Branch, "my-branch")
	}
	if plan.Request.Prompt != "do the thing" {
		t.Errorf("Request.Prompt = %q, want %q", plan.Request.Prompt, "do the thing")
	}
	if plan.Request.MaxIterations != 3 {
		t.Errorf("Request.MaxIterations = %d, want %d", plan.Request.MaxIterations, 3)
	}
	if plan.Request.Timeout != 30*time.Minute {
		t.Errorf("Request.Timeout = %v, want %v", plan.Request.Timeout, 30*time.Minute)
	}
}

// TestPlan_OverridesBeatWorkflowAndTopLevel pins the full precedence ordering for
// the per-call override layer: overrides beat the workflow, which beats
// top-level/defaults. A single config sets values at every layer; the first
// sub-test supplies every override and expects the override values; the second
// passes no precedence overrides and expects the middle (workflow, else
// defaults) layer to win.
func TestPlan_OverridesBeatWorkflowAndTopLevel(t *testing.T) {
	cfg := &ProjectConfig{
		Agent:            "opencode",
		Model:            "top-model",
		SourceDefinition: "src-top",
		Defaults: &RunDefaults{
			Timeout:          Duration(10 * time.Minute),
			MaxIterations:    2,
			CompletionSignal: "TOP_DONE",
		},
		Workflows: map[string]Workflow{
			"implement": {
				Prompt:        "p",
				Agent:         "claude-code",
				Model:         "wf-model",
				Timeout:       Duration(20 * time.Minute),
				MaxIterations: 5,
			},
		},
	}

	adopterConfigDir := filepath.Join(t.TempDir(), "repo", ".taboo")

	t.Run("overrides win", func(t *testing.T) {
		plan, err := cfg.Plan(adopterConfigDir, "implement", nil, PlanOverrides{
			Agent:            "copilot",
			Model:            "ov-model",
			Timeout:          45 * time.Minute,
			MaxIterations:    9,
			CompletionSignal: "OV_DONE",
			From:             "src-ov",
			Branch:           "b",
		})
		if err != nil {
			t.Fatalf("Plan: unexpected error: %v", err)
		}

		if plan.Model != "ov-model" {
			t.Errorf("Model = %q, want %q", plan.Model, "ov-model")
		}
		if got := plan.Config.Agent.Name(); got != "copilot" {
			t.Errorf("Config.Agent.Name() = %q, want %q", got, "copilot")
		}
		if plan.Request.Timeout != 45*time.Minute {
			t.Errorf("Request.Timeout = %v, want %v", plan.Request.Timeout, 45*time.Minute)
		}
		if plan.Request.MaxIterations != 9 {
			t.Errorf("Request.MaxIterations = %d, want %d", plan.Request.MaxIterations, 9)
		}
		if plan.Request.CompletionSignal != "OV_DONE" {
			t.Errorf("Request.CompletionSignal = %q, want %q", plan.Request.CompletionSignal, "OV_DONE")
		}
		if plan.Config.SourceDefinition != "src-ov" {
			t.Errorf("Config.SourceDefinition = %q, want %q", plan.Config.SourceDefinition, "src-ov")
		}
	})

	t.Run("workflow wins when no override", func(t *testing.T) {
		plan, err := cfg.Plan(adopterConfigDir, "implement", nil, PlanOverrides{Branch: "b"})
		if err != nil {
			t.Fatalf("Plan: unexpected error: %v", err)
		}

		if plan.Model != "wf-model" {
			t.Errorf("Model = %q, want %q", plan.Model, "wf-model")
		}
		if got := plan.Config.Agent.Name(); got != "claude-code" {
			t.Errorf("Config.Agent.Name() = %q, want %q", got, "claude-code")
		}
		if plan.Request.Timeout != 20*time.Minute {
			t.Errorf("Request.Timeout = %v, want %v", plan.Request.Timeout, 20*time.Minute)
		}
		if plan.Request.MaxIterations != 5 {
			t.Errorf("Request.MaxIterations = %d, want %d", plan.Request.MaxIterations, 5)
		}
		// No workflow layer for CompletionSignal: it falls through to defaults.
		if plan.Request.CompletionSignal != "TOP_DONE" {
			t.Errorf("Request.CompletionSignal = %q, want %q", plan.Request.CompletionSignal, "TOP_DONE")
		}
		if plan.Config.SourceDefinition != "src-top" {
			t.Errorf("Config.SourceDefinition = %q, want %q", plan.Config.SourceDefinition, "src-top")
		}
	})
}

// TestPlan_RepoPathConfigAnchored pins the RepoPath resolution rule and the
// structural ProjectDir == RepoPath/.taboo invariant. RepoPath is ALWAYS
// absolute. The cases: adopter layout (configDir under .taboo, no override →
// parent of .taboo), bare taboo.yaml (configDir not under .taboo, no override →
// configDir itself, so ProjectDir != configDir), a relative repo: override
// (resolved against configDir), and an absolute repo: override (used as-is, not
// nested under configDir).
func TestPlan_RepoPathConfigAnchored(t *testing.T) {
	// newCfg builds a minimal valid ProjectConfig, varying only Repo per case.
	newCfg := func(repo string) *ProjectConfig {
		return &ProjectConfig{
			Agent: "opencode",
			Model: "m",
			Repo:  repo,
			Workflows: map[string]Workflow{
				"implement": {Prompt: "p"},
			},
		}
	}

	t.Run("adopter layout", func(t *testing.T) {
		configDir := filepath.Join(t.TempDir(), "myrepo", ".taboo")

		plan, err := newCfg("").Plan(configDir, "implement", nil, PlanOverrides{})
		if err != nil {
			t.Fatalf("Plan: unexpected error: %v", err)
		}

		if !filepath.IsAbs(plan.Config.RepoPath) {
			t.Errorf("Config.RepoPath = %q, want absolute", plan.Config.RepoPath)
		}
		if want := filepath.Dir(configDir); plan.Config.RepoPath != want {
			t.Errorf("Config.RepoPath = %q, want %q", plan.Config.RepoPath, want)
		}
		// In the adopter layout the configDir IS the project dir.
		if plan.Config.ProjectDir != configDir {
			t.Errorf("Config.ProjectDir = %q, want %q", plan.Config.ProjectDir, configDir)
		}
		if want := filepath.Join(plan.Config.RepoPath, ".taboo"); plan.Config.ProjectDir != want {
			t.Errorf("invariant: ProjectDir = %q, want %q", plan.Config.ProjectDir, want)
		}
	})

	t.Run("bare taboo.yaml", func(t *testing.T) {
		configDir := filepath.Join(t.TempDir(), "proj")

		plan, err := newCfg("").Plan(configDir, "implement", nil, PlanOverrides{})
		if err != nil {
			t.Fatalf("Plan: unexpected error: %v", err)
		}

		wantRepo, err := filepath.Abs(configDir)
		if err != nil {
			t.Fatalf("filepath.Abs: %v", err)
		}
		if !filepath.IsAbs(plan.Config.RepoPath) {
			t.Errorf("Config.RepoPath = %q, want absolute", plan.Config.RepoPath)
		}
		if plan.Config.RepoPath != wantRepo {
			t.Errorf("Config.RepoPath = %q, want %q", plan.Config.RepoPath, wantRepo)
		}
		// Not under .taboo: ProjectDir is RepoPath/.taboo, which is NOT configDir.
		if want := filepath.Join(configDir, ".taboo"); plan.Config.ProjectDir != want {
			t.Errorf("Config.ProjectDir = %q, want %q", plan.Config.ProjectDir, want)
		}
		if want := filepath.Join(plan.Config.RepoPath, ".taboo"); plan.Config.ProjectDir != want {
			t.Errorf("invariant: ProjectDir = %q, want %q", plan.Config.ProjectDir, want)
		}
	})

	t.Run("repo override", func(t *testing.T) {
		configDir := filepath.Join(t.TempDir(), "myrepo", ".taboo")

		plan, err := newCfg("../elsewhere").Plan(configDir, "implement", nil, PlanOverrides{})
		if err != nil {
			t.Fatalf("Plan: unexpected error: %v", err)
		}

		wantRepo, err := filepath.Abs(filepath.Join(configDir, "../elsewhere"))
		if err != nil {
			t.Fatalf("filepath.Abs: %v", err)
		}
		if !filepath.IsAbs(plan.Config.RepoPath) {
			t.Errorf("Config.RepoPath = %q, want absolute", plan.Config.RepoPath)
		}
		if plan.Config.RepoPath != wantRepo {
			t.Errorf("Config.RepoPath = %q, want %q", plan.Config.RepoPath, wantRepo)
		}
		if want := filepath.Join(plan.Config.RepoPath, ".taboo"); plan.Config.ProjectDir != want {
			t.Errorf("invariant: ProjectDir = %q, want %q", plan.Config.ProjectDir, want)
		}
	})

	t.Run("absolute repo override", func(t *testing.T) {
		configDir := filepath.Join(t.TempDir(), "myrepo", ".taboo")
		// An absolute repo: must be used verbatim, never nested under configDir
		// (the bug a naive filepath.Join(configDir, repo) would introduce).
		absRepo := filepath.Join(t.TempDir(), "elsewhere")

		plan, err := newCfg(absRepo).Plan(configDir, "implement", nil, PlanOverrides{})
		if err != nil {
			t.Fatalf("Plan: unexpected error: %v", err)
		}

		if !filepath.IsAbs(plan.Config.RepoPath) {
			t.Errorf("Config.RepoPath = %q, want absolute", plan.Config.RepoPath)
		}
		if plan.Config.RepoPath != absRepo {
			t.Errorf("Config.RepoPath = %q, want %q (absolute repo used as-is)", plan.Config.RepoPath, absRepo)
		}
		if want := filepath.Join(plan.Config.RepoPath, ".taboo"); plan.Config.ProjectDir != want {
			t.Errorf("invariant: ProjectDir = %q, want %q", plan.Config.ProjectDir, want)
		}
	})
}

// TestPlan_PromptResolutionAndVars pins the prompt-resolution slice of the
// resolver: a workflow *-file value is read relative to configDir and used
// verbatim; vars substitute {{...}} placeholders when present; a prompt with
// literal braces is left intact when no vars are supplied (Substitute is
// skipped); and an inline override beats a workflow prompt-file (precedence head).
func TestPlan_PromptResolutionAndVars(t *testing.T) {
	t.Run("workflow prompt-file read, relative to configDir", func(t *testing.T) {
		configDir := filepath.Join(t.TempDir(), "repo", ".taboo")
		if err := os.MkdirAll(filepath.Join(configDir, "prompts"), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(filepath.Join(configDir, "prompts", "impl.md"), []byte("file body"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		cfg := &ProjectConfig{
			Agent: "opencode",
			Model: "m",
			Workflows: map[string]Workflow{
				"implement": {PromptFile: "prompts/impl.md"},
			},
		}

		plan, err := cfg.Plan(configDir, "implement", nil, PlanOverrides{Branch: "b"})
		if err != nil {
			t.Fatalf("Plan: unexpected error: %v", err)
		}
		if plan.Request.Prompt != "file body" {
			t.Errorf("Request.Prompt = %q, want %q", plan.Request.Prompt, "file body")
		}
	})

	t.Run("vars substituted", func(t *testing.T) {
		configDir := filepath.Join(t.TempDir(), "repo", ".taboo")
		cfg := &ProjectConfig{
			Agent: "opencode",
			Model: "m",
			Workflows: map[string]Workflow{
				"implement": {Prompt: "hello {{NAME}}"},
			},
		}

		plan, err := cfg.Plan(configDir, "implement", map[string]string{"NAME": "world"}, PlanOverrides{Branch: "b"})
		if err != nil {
			t.Fatalf("Plan: unexpected error: %v", err)
		}
		if plan.Request.Prompt != "hello world" {
			t.Errorf("Request.Prompt = %q, want %q", plan.Request.Prompt, "hello world")
		}
	})

	t.Run("no vars leaves literal braces", func(t *testing.T) {
		configDir := filepath.Join(t.TempDir(), "repo", ".taboo")
		cfg := &ProjectConfig{
			Agent: "opencode",
			Model: "m",
			Workflows: map[string]Workflow{
				"implement": {Prompt: "raw {{X}}"},
			},
		}

		plan, err := cfg.Plan(configDir, "implement", nil, PlanOverrides{Branch: "b"})
		if err != nil {
			t.Fatalf("Plan: unexpected error: %v", err)
		}
		if plan.Request.Prompt != "raw {{X}}" {
			t.Errorf("Request.Prompt = %q, want %q", plan.Request.Prompt, "raw {{X}}")
		}
	})

	t.Run("inline overrides file across layers", func(t *testing.T) {
		configDir := filepath.Join(t.TempDir(), "repo", ".taboo")
		cfg := &ProjectConfig{
			Agent: "opencode",
			Model: "m",
			Workflows: map[string]Workflow{
				"implement": {PromptFile: "prompts/impl.md"}, // never read: inline ov wins
			},
		}

		plan, err := cfg.Plan(configDir, "implement", nil, PlanOverrides{Branch: "b", Prompt: "inline-ov"})
		if err != nil {
			t.Fatalf("Plan: unexpected error: %v", err)
		}
		if plan.Request.Prompt != "inline-ov" {
			t.Errorf("Request.Prompt = %q, want %q", plan.Request.Prompt, "inline-ov")
		}
	})
}

// TestPlan_ErrorContract locks the error contract of the resolver: every failure
// mode surfaces out of Plan, and the named failures are errors.Is-matchable
// against their sentinels (so callers can branch on them), while the distinct
// failures (substitution, unreadable prompt-file) are NOT mistaken for the
// "nothing configured" sentinel. The config dir is irrelevant to these paths,
// so a bare temp dir suffices throughout.
func TestPlan_ErrorContract(t *testing.T) {
	configDir := t.TempDir()
	ov := PlanOverrides{Branch: "b"}

	t.Run("unknown workflow wraps ErrUnknownWorkflow", func(t *testing.T) {
		cfg := &ProjectConfig{
			Agent: "opencode",
			Model: "m",
			Workflows: map[string]Workflow{
				"implement": {Prompt: "p"},
			},
		}

		_, err := cfg.Plan(configDir, "nope", nil, ov)
		if !errors.Is(err, ErrUnknownWorkflow) {
			t.Errorf("err = %v, want errors.Is ErrUnknownWorkflow", err)
		}
	})

	t.Run("no prompt configured returns ErrNoPrompt", func(t *testing.T) {
		// Agent present, but no prompt anywhere: workflow has empty
		// Prompt/PromptFile and there are no defaults.
		cfg := &ProjectConfig{
			Agent: "opencode",
			Model: "m",
			Workflows: map[string]Workflow{
				"implement": {},
			},
		}

		_, err := cfg.Plan(configDir, "implement", nil, ov)
		if !errors.Is(err, ErrNoPrompt) {
			t.Errorf("err = %v, want errors.Is ErrNoPrompt", err)
		}
	})

	t.Run("unknown agent via override wraps ErrUnknownAgent", func(t *testing.T) {
		// Valid config, but a bogus agent name forced through the override.
		// NewProfile must wrap ErrUnknownAgent and Plan must return it intact.
		cfg := &ProjectConfig{
			Agent: "opencode",
			Model: "m",
			Workflows: map[string]Workflow{
				"implement": {Prompt: "p"},
			},
		}

		_, err := cfg.Plan(configDir, "implement", nil, PlanOverrides{Agent: "gemni", Branch: "b"})
		if !errors.Is(err, ErrUnknownAgent) {
			t.Errorf("err = %v, want errors.Is ErrUnknownAgent", err)
		}
	})

	t.Run("substitute error flows out and is distinct", func(t *testing.T) {
		// Inline prompt references {{MISSING}}; vars are present but lack that
		// key, so Substitute errors. The error must surface and must be neither
		// ErrNoPrompt (a prompt WAS configured) nor ErrUnknownWorkflow.
		cfg := &ProjectConfig{
			Agent: "opencode",
			Model: "m",
			Workflows: map[string]Workflow{
				"implement": {Prompt: "hi {{MISSING}}"},
			},
		}

		_, err := cfg.Plan(configDir, "implement", map[string]string{"OTHER": "x"}, ov)
		if err == nil {
			t.Fatal("err = nil, want a substitution error")
		}
		if errors.Is(err, ErrNoPrompt) {
			t.Errorf("err = %v, want NOT ErrNoPrompt", err)
		}
		if errors.Is(err, ErrUnknownWorkflow) {
			t.Errorf("err = %v, want NOT ErrUnknownWorkflow", err)
		}
		if !strings.Contains(err.Error(), "MISSING") {
			t.Errorf("err = %v, want message mentioning the unresolved key %q", err, "MISSING")
		}
	})

	t.Run("prompt-file read error is not ErrNoPrompt", func(t *testing.T) {
		// A prompt-file IS configured but cannot be read: a distinct, surfaced
		// error, explicitly NOT the "nothing configured" sentinel.
		cfg := &ProjectConfig{
			Agent: "opencode",
			Model: "m",
			Workflows: map[string]Workflow{
				"implement": {PromptFile: "does-not-exist.md"},
			},
		}

		_, err := cfg.Plan(configDir, "implement", nil, ov)
		if err == nil {
			t.Fatal("err = nil, want a prompt-file read error")
		}
		if errors.Is(err, ErrNoPrompt) {
			t.Errorf("err = %v, want NOT ErrNoPrompt", err)
		}
	})
}

// TestFindConfig pins the upward config-discovery walk: at each ancestor dir the
// bare taboo.yaml is probed before .taboo/taboo.yaml, the walk ascends to the
// filesystem root, and the first existing path wins. Real temp dirs are used so
// the os.Stat probe exercises the actual filesystem.
func TestFindConfig(t *testing.T) {
	t.Run("bare taboo.yaml in start dir", func(t *testing.T) {
		tmp := t.TempDir()
		want := filepath.Join(tmp, "taboo.yaml")
		if err := os.WriteFile(want, []byte("agent: opencode\n"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		got, found := FindConfig(tmp)
		if !found {
			t.Fatalf("FindConfig(%q) found = false, want true", tmp)
		}
		if got != want {
			t.Errorf("FindConfig path = %q, want %q", got, want)
		}
	})

	t.Run(".taboo/taboo.yaml in start dir", func(t *testing.T) {
		tmp := t.TempDir()
		if err := os.MkdirAll(filepath.Join(tmp, ".taboo"), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		want := filepath.Join(tmp, ".taboo", "taboo.yaml")
		if err := os.WriteFile(want, []byte("agent: opencode\n"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		got, found := FindConfig(tmp)
		if !found {
			t.Fatalf("FindConfig(%q) found = false, want true", tmp)
		}
		if got != want {
			t.Errorf("FindConfig path = %q, want %q", got, want)
		}
	})

	t.Run("found by ascending from a subdir", func(t *testing.T) {
		tmp := t.TempDir()
		if err := os.MkdirAll(filepath.Join(tmp, ".taboo"), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		want := filepath.Join(tmp, ".taboo", "taboo.yaml")
		if err := os.WriteFile(want, []byte("agent: opencode\n"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		start := filepath.Join(tmp, "a", "b", "c")
		if err := os.MkdirAll(start, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		got, found := FindConfig(start)
		if !found {
			t.Fatalf("FindConfig(%q) found = false, want true", start)
		}
		if got != want {
			t.Errorf("FindConfig path = %q, want %q", got, want)
		}
	})

	t.Run("bare taboo.yaml takes precedence over .taboo/taboo.yaml", func(t *testing.T) {
		tmp := t.TempDir()
		if err := os.MkdirAll(filepath.Join(tmp, ".taboo"), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		bare := filepath.Join(tmp, "taboo.yaml")
		if err := os.WriteFile(bare, []byte("agent: opencode\n"), 0o644); err != nil {
			t.Fatalf("WriteFile bare: %v", err)
		}
		if err := os.WriteFile(filepath.Join(tmp, ".taboo", "taboo.yaml"), []byte("agent: opencode\n"), 0o644); err != nil {
			t.Fatalf("WriteFile .taboo: %v", err)
		}

		got, found := FindConfig(tmp)
		if !found {
			t.Fatalf("FindConfig(%q) found = false, want true", tmp)
		}
		if got != bare {
			t.Errorf("FindConfig path = %q, want bare %q (bare check runs first)", got, bare)
		}
	})

	t.Run("not found", func(t *testing.T) {
		// A deeply nested empty subtree with no config: the walk ascends to the
		// filesystem root without a hit and reports not-found.
		start := filepath.Join(t.TempDir(), "x", "y", "z")
		if err := os.MkdirAll(start, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		got, found := FindConfig(start)
		if found {
			t.Errorf("FindConfig(%q) found = true, want false", start)
		}
		if got != "" {
			t.Errorf("FindConfig path = %q, want empty", got)
		}
	})
}

// TestPlanRunAndRunWorkflow drives the run side of the bridge over the fake
// Commander seam: Plan.Run delegates to the orchestrator (the loop actually
// runs), nil output sinks resolve to io.Discard, and RunWorkflow composes the
// full locate -> load -> plan -> run pipeline (plus its not-found error path).
func TestPlanRunAndRunWorkflow(t *testing.T) {
	t.Run("Plan.Run drives the loop", func(t *testing.T) {
		// A ready Plan (testConfig gives a real RepoPath whose project def the
		// runner materializes from) looped to MaxIterations over the fake: this
		// mirrors TestOrchestrator_LoopsToMaxIterations but reaches the loop
		// through Plan.Run, proving the delegation to NewOrchestrator(New(...)).
		p := &Plan{
			Config: testConfig(t),
			Request: OrchestratedRequest{
				RunRequest:       RunRequest{Branch: "agent/x", Prompt: "go"},
				MaxIterations:    3,
				CompletionSignal: "DONE",
			},
		}
		fc := &fakeCommander{}

		res, err := p.Run(context.Background(), fc)
		if err != nil {
			t.Fatalf("Plan.Run: %v", err)
		}
		if res.Iterations != 3 {
			t.Errorf("Iterations = %d, want 3", res.Iterations)
		}
		if res.StopReason != StopMaxIterations {
			t.Errorf("StopReason = %q, want %q", res.StopReason, StopMaxIterations)
		}
		if got := fc.countVerb("exec"); got != 3 {
			t.Errorf("exec count = %d, want 3 (one per iteration)", got)
		}
		if got := fc.countVerb("worktree"); got != 1 {
			t.Errorf("worktree count = %d, want 1 (Setup runs once, then Exec loops)", got)
		}
	})

	t.Run("nil Stdout/Stderr resolve to io.Discard", func(t *testing.T) {
		// The resolver must default unset output sinks to io.Discard (a package
		// var, so == identity holds), so a Plan.Run with no caller-supplied
		// writers never panics on a nil writer.
		cfg := &ProjectConfig{
			Agent: "opencode",
			Model: "m",
			Workflows: map[string]Workflow{
				"implement": {Prompt: "p"},
			},
		}
		adopterConfigDir := filepath.Join(t.TempDir(), "repo", ".taboo")

		plan, err := cfg.Plan(adopterConfigDir, "implement", nil, PlanOverrides{Branch: "b"})
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if plan.Request.Stdout != io.Discard {
			t.Errorf("Request.Stdout = %v, want io.Discard", plan.Request.Stdout)
		}
		if plan.Request.Stderr != io.Discard {
			t.Errorf("Request.Stderr = %v, want io.Discard", plan.Request.Stderr)
		}
	})

	t.Run("RunWorkflow locates, loads, plans and runs", func(t *testing.T) {
		// A real on-disk adopter layout: the project's own workshop.yaml at the
		// repo root (what the runner materializes from) plus a .taboo/taboo.yaml
		// config. RunWorkflow must walk from repo, load the config, resolve the
		// plan, and run it over the fake — the whole bridge end-to-end.
		repo := t.TempDir()
		writeProjectDef(t, repo, "name: myproject\nbase: ubuntu@24.04\nsdks:\n  - name: go\n")
		if err := os.MkdirAll(filepath.Join(repo, ".taboo"), 0o755); err != nil {
			t.Fatalf("MkdirAll .taboo: %v", err)
		}
		cfgYAML := "" +
			"workshop: taboo-run\n" +
			"base: ubuntu@24.04\n" +
			"agent: opencode\n" +
			"model: " + openCodeModel + "\n" +
			"workflows:\n" +
			"  implement:\n" +
			"    prompt: go\n"
		if err := os.WriteFile(filepath.Join(repo, ".taboo", "taboo.yaml"), []byte(cfgYAML), 0o644); err != nil {
			t.Fatalf("WriteFile taboo.yaml: %v", err)
		}

		fc := &fakeCommander{}
		res, err := RunWorkflow(context.Background(), repo, "implement", nil, PlanOverrides{Branch: "agent/x"}, fc)
		if err != nil {
			t.Fatalf("RunWorkflow: %v", err)
		}
		if res.Branch != "agent/x" {
			t.Errorf("Branch = %q, want %q", res.Branch, "agent/x")
		}
		if res.Iterations != 1 {
			t.Errorf("Iterations = %d, want 1 (zero MaxIterations -> one run)", res.Iterations)
		}
		if got := fc.countVerb("exec"); got != 1 {
			t.Errorf("exec count = %d, want 1", got)
		}
	})

	t.Run("RunWorkflow errors when no config is found", func(t *testing.T) {
		// A fresh empty tempdir under /tmp has no taboo.yaml up its tree, so the
		// locate step fails before any command is dispatched.
		fc := &fakeCommander{}
		_, err := RunWorkflow(context.Background(), t.TempDir(), "implement", nil, PlanOverrides{}, fc)
		if err == nil {
			t.Fatal("RunWorkflow: want a not-found error, got nil")
		}
		if !strings.Contains(err.Error(), "no taboo.yaml") {
			t.Errorf("err = %v, want it to mention %q", err, "no taboo.yaml")
		}
	})
}

// reviewResult is a local result type for the typed-bridge tests: the agent's
// structured output decodes into this shape via JSONResult[reviewResult].
type reviewResult struct {
	Approved string `json:"approved"`
}

// TestRunWorkflowAs drives the typed one-call bridge over the fake Commander
// seam. RunWorkflowAs[T] threads JSONResult[T] into the plan so extraction runs
// in-loop and yields a statically typed value with NO caller assertion: the
// happy path returns the decoded reviewResult directly; the extractor's
// sentinels (ErrNoResult / ErrInvalidResult) surface as the Run error with the
// typed zero value; and a locate/load failure short-circuits before Run, leaving
// the OrchestratedResult its zero value.
func TestRunWorkflowAs(t *testing.T) {
	// writeAdopter materializes the on-disk adopter layout: the project's own
	// workshop.yaml at the repo root plus a .taboo/taboo.yaml whose implement
	// workflow prompts the agent. Returns the repo root to walk from.
	writeAdopter := func(t *testing.T) string {
		t.Helper()
		repo := t.TempDir()
		writeProjectDef(t, repo, "name: myproject\nbase: ubuntu@24.04\nsdks:\n  - name: go\n")
		if err := os.MkdirAll(filepath.Join(repo, ".taboo"), 0o755); err != nil {
			t.Fatalf("MkdirAll .taboo: %v", err)
		}
		cfgYAML := "" +
			"workshop: taboo-run\n" +
			"base: ubuntu@24.04\n" +
			"agent: opencode\n" +
			"model: " + openCodeModel + "\n" +
			"workflows:\n" +
			"  implement:\n" +
			"    prompt: go\n"
		if err := os.WriteFile(filepath.Join(repo, ".taboo", "taboo.yaml"), []byte(cfgYAML), 0o644); err != nil {
			t.Fatalf("WriteFile taboo.yaml: %v", err)
		}
		return repo
	}

	t.Run("typed result, no caller assertion", func(t *testing.T) {
		// The agent's exec stdout carries a well-formed result block; the in-loop
		// extractor decodes it into reviewResult. got is statically reviewResult,
		// so there is NO .(reviewResult) at the call site.
		repo := writeAdopter(t)
		fc := &fakeCommander{
			stdoutFn: func(c Cmd) string {
				if verbOf(c) == "exec" {
					return "thinking...\n<result>{\"approved\":\"yes\"}</result>\n"
				}
				return ""
			},
		}

		got, res, err := RunWorkflowAs[reviewResult](context.Background(), repo, "implement", nil, PlanOverrides{Branch: "agent/x"}, fc)
		if err != nil {
			t.Fatalf("RunWorkflowAs: %v", err)
		}
		if got.Approved != "yes" {
			t.Errorf("got.Approved = %q, want %q", got.Approved, "yes")
		}
		if res.Iterations != 1 {
			t.Errorf("Iterations = %d, want 1", res.Iterations)
		}
		// The any-typed Result still carries the same decoded value.
		if res.Result.(reviewResult).Approved != "yes" {
			t.Errorf("res.Result.(reviewResult).Approved = %q, want %q", res.Result.(reviewResult).Approved, "yes")
		}
	})

	t.Run("ErrNoResult surfaces", func(t *testing.T) {
		// No result block in the exec output: the in-loop extractor returns
		// ErrNoResult, which surfaces as the Run error, and got is the zero value.
		repo := writeAdopter(t)
		fc := &fakeCommander{
			stdoutFn: func(c Cmd) string {
				if verbOf(c) == "exec" {
					return "no block here\n"
				}
				return ""
			},
		}

		got, _, err := RunWorkflowAs[reviewResult](context.Background(), repo, "implement", nil, PlanOverrides{Branch: "agent/x"}, fc)
		if !errors.Is(err, ErrNoResult) {
			t.Errorf("err = %v, want errors.Is ErrNoResult", err)
		}
		if got != (reviewResult{}) {
			t.Errorf("got = %+v, want zero reviewResult", got)
		}
	})

	t.Run("ErrInvalidResult surfaces", func(t *testing.T) {
		// A result block IS present but its payload is not valid JSON: the in-loop
		// extractor returns ErrInvalidResult and got is the zero value.
		repo := writeAdopter(t)
		fc := &fakeCommander{
			stdoutFn: func(c Cmd) string {
				if verbOf(c) == "exec" {
					return "<result>not json</result>\n"
				}
				return ""
			},
		}

		got, _, err := RunWorkflowAs[reviewResult](context.Background(), repo, "implement", nil, PlanOverrides{Branch: "agent/x"}, fc)
		if !errors.Is(err, ErrInvalidResult) {
			t.Errorf("err = %v, want errors.Is ErrInvalidResult", err)
		}
		if got != (reviewResult{}) {
			t.Errorf("got = %+v, want zero reviewResult", got)
		}
	})

	t.Run("locate/load error short-circuits before Run", func(t *testing.T) {
		// A fresh empty dir has no taboo.yaml up its tree, so locate fails before
		// any extractor is wired or any command dispatched: got is zero AND the
		// OrchestratedResult is its zero value (Run never ran).
		fc := &fakeCommander{}
		got, res, err := RunWorkflowAs[reviewResult](context.Background(), t.TempDir(), "implement", nil, PlanOverrides{}, fc)
		if err == nil {
			t.Fatal("RunWorkflowAs: want a not-found error, got nil")
		}
		if got != (reviewResult{}) {
			t.Errorf("got = %+v, want zero reviewResult", got)
		}
		if res != (OrchestratedResult{}) {
			t.Errorf("res = %+v, want zero OrchestratedResult", res)
		}
	})
}
