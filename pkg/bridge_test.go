package taboo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPlanRunAndRunWorkflow drives the run side of the bridge over the fake
// Commander seam: Plan.Run delegates to the orchestrator (the loop actually
// runs), and RunWorkflow composes the full locate -> load -> plan -> run pipeline
// (plus its not-found error path). The resolver's pure behavior (precedence,
// prompt resolution, output-sink defaulting) is covered by the config package's
// white-box tests; here we exercise the bridge free funcs end-to-end through the
// facade.
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
