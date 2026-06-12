package taboo

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// indexOfVerb returns the index of the first recorded call whose verb matches,
// or -1.
func indexOfVerb(fc *fakeCommander, verb string) int {
	for i, c := range fc.calls {
		if verbOf(c) == verb {
			return i
		}
	}
	return -1
}

// indexOfExecContaining returns the index of the first `workshop exec` call
// whose argv contains token, or -1. Used to tell the agent exec apart from an
// in-workshop hook exec (both have verb "exec").
func indexOfExecContaining(fc *fakeCommander, token string) int {
	for i, c := range fc.calls {
		if verbOf(c) == "exec" && slices.Contains(c.Args, token) {
			return i
		}
	}
	return -1
}

// indexOfName returns the index of the first recorded call whose executable
// name matches, or -1.
func indexOfName(fc *fakeCommander, name string) int {
	for i, c := range fc.calls {
		if c.Name == name {
			return i
		}
	}
	return -1
}

func TestHookCmd(t *testing.T) {
	// Host hook: the bare executable, no workshop wrapping.
	host := hookCmd("/proj", "ws", []string{"OPENROUTER_API_KEY"},
		Hook{Command: []string{"make", "deps"}})
	if host.Name != "make" || !slices.Equal(host.Args, []string{"deps"}) {
		t.Errorf("host hook = %+v, want make [deps]", host)
	}

	// In-workshop hook: a `workshop exec` carrying cwd and the env keys.
	in := hookCmd("/proj", "ws", []string{"OPENROUTER_API_KEY"},
		Hook{Command: []string{"go", "build"}, InWorkshop: true})
	want := execArgs("/proj", "ws",
		execOptions{cwd: workspaceTarget, envKeys: []string{"OPENROUTER_API_KEY"}},
		[]string{"go", "build"})
	if in.Name != "workshop" || !slices.Equal(in.Args, want) {
		t.Errorf("in-workshop hook = %+v, want workshop %v", in, want)
	}
}

func TestRun_OnWorkshopReadyHooksRunInOrder(t *testing.T) {
	fc := &fakeCommander{}
	cfg := testConfig(t)
	cfg.AgentCmd = []string{"opencode", "run"}
	r := New(cfg, fc)

	_, err := r.Run(context.Background(), RunRequest{
		Branch: "agent/x",
		Prompt: "do the task",
		Hooks: Hooks{
			OnWorkshopReady: []Hook{
				{Command: []string{"first"}},
				{Command: []string{"second"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if a, b := indexOfName(fc, "first"), indexOfName(fc, "second"); !(a >= 0 && a < b) {
		t.Errorf("hooks out of order: first=%d second=%d", a, b)
	}
}

func TestRun_OnWorkshopReadyHostHookRunsOnHost(t *testing.T) {
	fc := &fakeCommander{}
	cfg := testConfig(t)
	cfg.AgentCmd = []string{"opencode", "run"}
	r := New(cfg, fc)

	_, err := r.Run(context.Background(), RunRequest{
		Branch: "agent/x",
		Prompt: "do the task",
		Hooks: Hooks{
			OnWorkshopReady: []Hook{
				{Command: []string{"make", "deps"}}, // host hook (InWorkshop false)
			},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	idx := indexOfName(fc, "make")
	if idx < 0 {
		t.Fatalf("host hook not run as a host command; calls=%v", fc.verbs())
	}
	// A host hook runs the bare executable, not `workshop exec`.
	if got := fc.calls[idx]; !slices.Equal(got.Args, []string{"deps"}) {
		t.Errorf("host hook args = %v, want [deps]", got.Args)
	}
	// It still lands after start and before the agent exec.
	startIdx := indexOfVerb(fc, "start")
	agentIdx := indexOfExecContaining(fc, "do the task")
	if !(startIdx < idx && idx < agentIdx) {
		t.Errorf("want start(%d) < hook(%d) < agent exec(%d)", startIdx, idx, agentIdx)
	}
}

func TestRun_OnWorkshopReadyInWorkshopHookInheritsCwdAndEnvKeys(t *testing.T) {
	fc := &fakeCommander{}
	cfg := testConfig(t)
	cfg.AgentCmd = []string{"opencode", "run"}
	cfg.EnvKeys = []string{"OPENROUTER_API_KEY"}
	r := New(cfg, fc)

	_, err := r.Run(context.Background(), RunRequest{
		Branch: "agent/x",
		Prompt: "do the task",
		Hooks: Hooks{
			OnWorkshopReady: []Hook{
				{Command: []string{"go", "mod", "download"}, InWorkshop: true},
			},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The in-workshop hook is a `workshop exec` with the same cwd and credential
	// env keys the agent gets, so setup runs with the agent's context. Assert the
	// whole argv against the builder the production path uses.
	idx := indexOfExecContaining(fc, "download")
	want := execArgs(cfg.ProjectDir, cfg.Workshop,
		execOptions{cwd: workspaceTarget, envKeys: cfg.EnvKeys},
		[]string{"go", "mod", "download"})
	if got := fc.calls[idx].Args; !slices.Equal(got, want) {
		t.Errorf("in-workshop hook args =\n  %v\nwant\n  %v", got, want)
	}
}

func TestRun_OnWorkshopReadyHookFailureAbortsBeforeAgent(t *testing.T) {
	// A failing hook fails the run with context and the agent never execs.
	fc := &fakeCommander{
		errFn: func(c Cmd) error {
			if c.Name == "false" {
				return fmt.Errorf("boom")
			}
			return nil
		},
	}
	cfg := testConfig(t)
	cfg.AgentCmd = []string{"opencode", "run"}
	r := New(cfg, fc)

	_, err := r.Run(context.Background(), RunRequest{
		Branch: "agent/x",
		Prompt: "do the task",
		Hooks: Hooks{
			OnWorkshopReady: []Hook{{Command: []string{"false"}}},
		},
	})
	if err == nil {
		t.Fatal("expected hook failure to surface as a run error")
	}
	if !strings.Contains(err.Error(), "on-workshop-ready hook") {
		t.Errorf("error missing hook context: %v", err)
	}
	if !strings.Contains(err.Error(), "false") {
		t.Errorf("error missing offending command: %v", err)
	}
	// The agent must not have run after the hook failed.
	if idx := indexOfExecContaining(fc, "do the task"); idx >= 0 {
		t.Errorf("agent exec ran despite hook failure (call %d)", idx)
	}
}

func TestRun_OnWorkshopReadyHookRunsAfterStartBeforeExec(t *testing.T) {
	fc := &fakeCommander{errFn: failOnVerb("info")} // workshop absent -> launches
	cfg := testConfig(t)
	cfg.AgentCmd = []string{"opencode", "run"}
	r := New(cfg, fc)

	_, err := r.Run(context.Background(), RunRequest{
		Branch: "agent/x",
		Prompt: "do the task",
		Hooks: Hooks{
			OnWorkshopReady: []Hook{
				{Command: []string{"go", "mod", "download"}, InWorkshop: true},
			},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The on-workshop-ready hook runs once the worktree is mounted and the
	// workshop is started, but before the agent execs.
	startIdx := indexOfVerb(fc, "start")
	hookIdx := indexOfExecContaining(fc, "download")
	agentIdx := indexOfExecContaining(fc, "do the task")
	if startIdx < 0 || hookIdx < 0 || agentIdx < 0 {
		t.Fatalf("missing call: start=%d hook=%d agent=%d; verbs=%v", startIdx, hookIdx, agentIdx, fc.verbs())
	}
	if !(startIdx < hookIdx && hookIdx < agentIdx) {
		t.Fatalf("want start(%d) < hook(%d) < agent exec(%d); verbs=%v", startIdx, hookIdx, agentIdx, fc.verbs())
	}
}
