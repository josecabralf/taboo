package taboo

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func indexOfVerb(fc *fakeCommander, verb string) int {
	for i, c := range fc.calls {
		if verbOf(c) == verb {
			return i
		}
	}
	return -1
}

// indexOfExecContaining finds the first `workshop exec` call whose argv contains
// token, used to tell the agent exec apart from an in-workshop hook exec (both
// have verb "exec").
func indexOfExecContaining(fc *fakeCommander, token string) int {
	for i, c := range fc.calls {
		if verbOf(c) == "exec" && slices.Contains(c.Args, token) {
			return i
		}
	}
	return -1
}

func indexOfName(fc *fakeCommander, name string) int {
	for i, c := range fc.calls {
		if c.Name == name {
			return i
		}
	}
	return -1
}

func TestHookCmd(t *testing.T) {
	sessionEnv := []envAssignment{{Name: "XDG_DATA_HOME", Value: sessionsTarget}}

	// Host hook: the bare executable, no workshop wrapping, run in the worktree.
	// The session redirect is a workshop path, so a host hook ignores it.
	host := hookCmd("/proj", "ws", "/wt", []string{"OPENROUTER_API_KEY"}, sessionEnv, 0,
		Hook{Command: []string{"make", "deps"}})
	if host.Name != "make" || !slices.Equal(host.Args, []string{"deps"}) {
		t.Errorf("host hook = %+v, want make [deps]", host)
	}
	if host.Dir != "/wt" {
		t.Errorf("host hook Dir = %q, want /wt (the run's worktree)", host.Dir)
	}

	// In-workshop hook: a `workshop exec` carrying cwd, timeout, the credential
	// env keys, and the session-dir redirect — the same context as the agent.
	in := hookCmd("/proj", "ws", "/wt", []string{"OPENROUTER_API_KEY"}, sessionEnv, 30*time.Second,
		Hook{Command: []string{"go", "build"}, InWorkshop: true})
	want := execArgs("/proj", "ws",
		execOptions{cwd: workspaceTarget, timeout: 30 * time.Second, envKeys: []string{"OPENROUTER_API_KEY"}, env: sessionEnv},
		[]string{"go", "build"})
	if in.Name != "workshop" || !slices.Equal(in.Args, want) {
		t.Errorf("in-workshop hook = %+v, want workshop %v", in, want)
	}
}

func TestRun_OnWorkshopReadyEmptyHookSkipped(t *testing.T) {
	// A hook with no command is skipped, not run, and must not panic on the
	// empty Command slice. A following non-empty hook still runs.
	fc := &fakeCommander{}
	cfg := testConfig(t)
	r := New(cfg, fc)

	_, err := r.Run(context.Background(), RunRequest{
		Branch: "agent/x",
		Prompt: "do the task",
		Hooks: Hooks{
			OnWorkshopReady: []Hook{
				{Command: nil},                   // skipped
				{Command: []string{"real-hook"}}, // runs
			},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if idx := indexOfName(fc, "real-hook"); idx < 0 {
		t.Errorf("non-empty hook after an empty one did not run; verbs=%v", fc.verbs())
	}
}

// Hook stdout/stderr are wired to the run's Stderr so setup output and failures
// are diagnosable rather than discarded.
func TestRun_OnWorkshopReadyHookOutputWiredToRunStderr(t *testing.T) {
	var errBuf strings.Builder
	fc := &fakeCommander{}
	cfg := testConfig(t)
	r := New(cfg, fc)

	_, err := r.Run(context.Background(), RunRequest{
		Branch: "agent/x",
		Prompt: "do the task",
		Stderr: &errBuf,
		Hooks: Hooks{
			OnWorkshopReady: []Hook{{Command: []string{"setup"}}},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	idx := indexOfName(fc, "setup")
	if idx < 0 {
		t.Fatalf("hook not run; verbs=%v", fc.verbs())
	}
	if got := fc.calls[idx]; got.Stdout != &errBuf || got.Stderr != &errBuf {
		t.Errorf("hook output not wired to run Stderr: stdout=%v stderr=%v", got.Stdout, got.Stderr)
	}
}

// When the run is time-bounded, an in-workshop hook carries --timeout so it
// cannot hang the run before the agent execs.
func TestRun_OnWorkshopReadyInWorkshopHookHonorsTimeout(t *testing.T) {
	fc := &fakeCommander{}
	cfg := testConfig(t)
	r := New(cfg, fc)

	_, err := r.Run(context.Background(), RunRequest{
		Branch:  "agent/x",
		Prompt:  "do the task",
		Timeout: 30 * time.Second,
		Hooks: Hooks{
			OnWorkshopReady: []Hook{
				{Command: []string{"go", "mod", "download"}, InWorkshop: true},
			},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	idx := indexOfExecContaining(fc, "download")
	if idx < 0 {
		t.Fatalf("in-workshop hook not run; verbs=%v", fc.verbs())
	}
	if !slices.Contains(fc.calls[idx].Args, "--timeout") {
		t.Errorf("in-workshop hook missing --timeout: %v", fc.calls[idx].Args)
	}
}

func TestRun_OnWorkshopReadyHooksRunInOrder(t *testing.T) {
	fc := &fakeCommander{}
	cfg := testConfig(t)
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
	a, b := indexOfName(fc, "first"), indexOfName(fc, "second")
	if ordered := a >= 0 && a < b; !ordered {
		t.Errorf("hooks out of order: first=%d second=%d", a, b)
	}
}

func TestRun_OnWorkshopReadyHostHookRunsOnHost(t *testing.T) {
	fc := &fakeCommander{}
	cfg := testConfig(t)
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
	if ordered := startIdx < idx && idx < agentIdx; !ordered {
		t.Errorf("want start(%d) < hook(%d) < agent exec(%d)", startIdx, idx, agentIdx)
	}
}

func TestRun_OnWorkshopReadyInWorkshopHookInheritsAgentContext(t *testing.T) {
	fc := &fakeCommander{}
	cfg := testConfig(t)
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

	// The in-workshop hook is a `workshop exec` with the same cwd, credential env
	// keys, and session-dir redirect the agent gets, so setup runs in the agent's
	// context and a hook preparing session state writes where the agent reads.
	// OpenCode (the test config agent) is session-capable, so the redirect is
	// present. Assert the whole argv against the builder the production path uses.
	spec, ok := cfg.Agent.Sessions()
	if !ok {
		t.Fatal("test config agent is not session-capable")
	}
	idx := indexOfExecContaining(fc, "download")
	want := execArgs(cfg.ProjectDir, cfg.Workshop,
		execOptions{
			cwd:     workspaceTarget,
			envKeys: cfg.Agent.CredentialEnvKeys(),
			env:     []envAssignment{{Name: spec.DirEnv, Value: sessionsTarget}},
		},
		[]string{"go", "mod", "download"})
	if got := fc.calls[idx].Args; !slices.Equal(got, want) {
		t.Errorf("in-workshop hook args =\n  %v\nwant\n  %v", got, want)
	}
}

// A sessionless agent's in-workshop hook carries no session-dir redirect, just
// as its agent exec does not. Pins the negative branch of the hook env wiring.
func TestRun_OnWorkshopReadyInWorkshopHook_SessionlessAgentNoRedirect(t *testing.T) {
	fc := &fakeCommander{}
	cfg := testConfig(t)
	cfg.Agent = stdinProfile{} // Sessions() ok == false
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

	idx := indexOfExecContaining(fc, "download")
	for _, a := range fc.calls[idx].Args {
		if strings.Contains(a, "/sessions") {
			t.Errorf("sessionless agent's in-workshop hook carries a session redirect: %v", fc.calls[idx].Args)
		}
	}
}

func TestRun_OnWorkshopReadyHookFailureAbortsBeforeAgent(t *testing.T) {
	fc := &fakeCommander{
		errFn: func(c Cmd) error {
			if c.Name == "false" {
				return fmt.Errorf("boom")
			}
			return nil
		},
	}
	cfg := testConfig(t)
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
	if ordered := startIdx < hookIdx && hookIdx < agentIdx; !ordered {
		t.Fatalf("want start(%d) < hook(%d) < agent exec(%d); verbs=%v", startIdx, hookIdx, agentIdx, fc.verbs())
	}
}
