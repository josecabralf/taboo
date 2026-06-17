package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	taboo "github.com/josecabralf/taboo/pkg/taboo"
)

// runProjectBody is a complete, valid taboo.yaml the run tests build on. The
// repo deliberately points at a non-/tmp absolute path so validate's
// repo-location check passes (t.TempDir() lives under /tmp), and the agent +
// model resolve cleanly (opencode + a provider/model slug). A defaults block
// supplies the branch-prefix the auto-branch tests assert.
const runProjectBody = "" +
	"workshop: demo\n" +
	"base: ubuntu@24.04\n" +
	"agent: opencode\n" +
	"model: anthropic/claude\n" +
	"repo: /home/dev/repos/myproject\n" +
	"defaults:\n" +
	"  branch-prefix: taboo/\n" +
	"workflows:\n" +
	"  fix:\n" +
	"    prompt: please fix the failing tests\n"

// runFakeStdout programs the fake commander so a real run can proceed: workshop
// --version reports a healthy version (preflight), workshop info FAILS (so the
// lazy launch path runs), and `git rev-parse HEAD` yields a fake commit. The
// commit/version live in stdoutFn; the info failure lives in runFakeErr.
func runFakeStdout(c taboo.Cmd) string {
	if c.Name == "workshop" && len(c.Args) > 0 && c.Args[0] == "--version" {
		return "0.9.1\n"
	}
	if c.Name == "git" && elemsContain(c.Args, "rev-parse", "HEAD") {
		return "deadbeefcafe\n"
	}
	return ""
}

// runFakeErr fails the `workshop info` probe so ensureWorkshop takes the launch
// path; every other call succeeds.
func runFakeErr(c taboo.Cmd) error {
	if c.Name == "workshop" && elemsContain(c.Args, "info") {
		return errInfoMiss
	}
	return nil
}

// errInfoMiss is the sentinel runFakeErr returns for the workshop-info probe.
var errInfoMiss = errors.New("workshop info: no such workshop")

// newRunFake builds the standard fake commander for a successful run.
func newRunFake() *fakeCommander {
	return &fakeCommander{stdoutFn: runFakeStdout, errFn: runFakeErr}
}

// findInvocation returns the first recorded call whose [name, args...] contains
// every substr (exact element match), or nil when none matches.
func findInvocation(fake *fakeCommander, substrs ...string) []string {
	for _, inv := range invocations(fake) {
		if elemsContain(inv, substrs...) {
			return inv
		}
	}
	return nil
}

// elemsContain reports whether elems contains every wanted element. It backs
// both the invocation finders and the command-arg probes (an arg list is just
// another []string), so there is one membership predicate, not two.
func elemsContain(elems []string, wanted ...string) bool {
	for _, w := range wanted {
		if !slices.Contains(elems, w) {
			return false
		}
	}
	return true
}

// indexOfInvocation returns the index of the first recorded call containing
// every substr, or -1 when none matches. Used to assert ordering (info before
// launch).
func indexOfInvocation(fake *fakeCommander, substrs ...string) int {
	for i, inv := range invocations(fake) {
		if elemsContain(inv, substrs...) {
			return i
		}
	}
	return -1
}

// runCmd builds a run command with env, runs it with args, and returns the
// captured stdout/stderr buffers and the execute error.
func runCmd(t *testing.T, env Env, args ...string) (string, string, error) {
	t.Helper()
	cmd := newRunCmd(env)
	cmd.SetArgs(args)
	err := cmd.Execute()
	out, _ := env.Stdout.(*bytes.Buffer)
	errBuf, _ := env.Stderr.(*bytes.Buffer)
	if out == nil || errBuf == nil {
		t.Fatal("runCmd: env.Stdout and env.Stderr must be *bytes.Buffer")
	}
	return out.String(), errBuf.String(), err
}

// TestRun_TracerBullet drives a named workflow end-to-end through the real
// orchestrator with a fake commander: the workshop is lazily launched (info
// then launch), a worktree is added on an auto-generated branch under the
// configured prefix, and the agent is exec'd with the workflow's prompt.
func TestRun_TracerBullet(t *testing.T) {
	root := t.TempDir()
	writeTabooProject(t, root, runProjectBody)
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	_, _, err := runCmd(t, env, "fix")
	if err != nil {
		t.Fatalf("run fix error = %v, want nil", err)
	}

	infoIdx := indexOfInvocation(fake, "info")
	launchIdx := indexOfInvocation(fake, "launch")
	if infoIdx < 0 || launchIdx < 0 {
		t.Fatalf("expected info and launch calls; calls: %v", invocations(fake))
	}
	if infoIdx >= launchIdx {
		t.Errorf("info (idx %d) must precede launch (idx %d): %v", infoIdx, launchIdx, invocations(fake))
	}

	wt := findInvocation(fake, "git", "-C", "/home/dev/repos/myproject", "worktree", "add", "-b")
	if wt == nil {
		t.Fatalf("no worktree-add invocation; calls: %v", invocations(fake))
	}
	branch := branchOfWorktreeAdd(wt)
	if branch == "" || !strings.HasPrefix(branch, "taboo/fix-") {
		t.Errorf("worktree branch = %q, want prefix %q", branch, "taboo/fix-")
	}

	exec := findInvocation(fake, "exec", "please fix the failing tests")
	if exec == nil {
		t.Errorf("no exec invocation carrying the prompt; calls: %v", invocations(fake))
	}
}

// TestRun_BranchOverride asserts --branch is used verbatim as the worktree
// branch, bypassing the auto-generated name.
func TestRun_BranchOverride(t *testing.T) {
	root := t.TempDir()
	writeTabooProject(t, root, runProjectBody)
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	if _, _, err := runCmd(t, env, "fix", "--branch", "agent/custom"); err != nil {
		t.Fatalf("run error = %v, want nil", err)
	}

	wt := findInvocation(fake, "git", "-C", "/home/dev/repos/myproject", "worktree", "add", "-b")
	if wt == nil {
		t.Fatalf("no worktree-add invocation; calls: %v", invocations(fake))
	}
	if got := branchOfWorktreeAdd(wt); got != "agent/custom" {
		t.Errorf("worktree branch = %q, want %q", got, "agent/custom")
	}
}

// TestRun_MachineResultOnStdout asserts the run result (branch + commit) lands on
// stdout while the agent's streamed output does not, so a caller can parse stdout
// cleanly.
func TestRun_MachineResultOnStdout(t *testing.T) {
	root := t.TempDir()
	writeTabooProject(t, root, runProjectBody)
	fake := &fakeCommander{
		errFn: runFakeErr,
		stdoutFn: func(c taboo.Cmd) string {
			if c.Name == "workshop" && elemsContain(c.Args, "exec") {
				return "AGENT-NOISE: thinking...\n"
			}
			return runFakeStdout(c)
		},
	}
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	stdout, stderr, err := runCmd(t, env, "fix", "--branch", "agent/custom")
	if err != nil {
		t.Fatalf("run error = %v, want nil", err)
	}
	if !strings.Contains(stdout, "branch: agent/custom") {
		t.Errorf("stdout missing branch line:\n%s", stdout)
	}
	if !strings.Contains(stdout, "commit: deadbeefcafe") {
		t.Errorf("stdout missing commit line:\n%s", stdout)
	}
	if strings.Contains(stdout, "AGENT-NOISE") {
		t.Errorf("agent output leaked to stdout:\n%s", stdout)
	}
	if !strings.Contains(stderr, "AGENT-NOISE") {
		t.Errorf("agent output missing from stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "Running workflow") {
		t.Errorf("start line missing from stderr:\n%s", stderr)
	}
}

// TestRun_UnknownWorkflow asserts selecting a workflow not in the config errors
// with a message naming the bad workflow and the available names, and never
// reaches execution (no worktree/exec calls).
func TestRun_UnknownWorkflow(t *testing.T) {
	root := t.TempDir()
	body := runProjectBody +
		"  refactor:\n    prompt: refactor it\n"
	writeTabooProject(t, root, body)
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	_, stderr, err := runCmd(t, env, "nope")
	if err == nil {
		t.Fatal("run nope error = nil, want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, `unknown workflow "nope"`) {
		t.Errorf("error = %q, want it to mention unknown workflow", msg)
	}
	if !strings.Contains(msg, "fix") || !strings.Contains(msg, "refactor") {
		t.Errorf("error = %q, want it to list available workflows (fix, refactor)", msg)
	}
	if findInvocation(fake, "worktree") != nil || findInvocation(fake, "exec") != nil {
		t.Errorf("unknown workflow must not execute; calls: %v\nstderr:\n%s", invocations(fake), stderr)
	}
}

// TestRun_PreflightWorkshopDown asserts that when the workshop probe fails, run
// refuses with errRunFailed, reports the failure on stderr, and never executes.
func TestRun_PreflightWorkshopDown(t *testing.T) {
	root := t.TempDir()
	writeTabooProject(t, root, runProjectBody)
	fake := &fakeCommander{
		stdoutFn: runFakeStdout,
		errFn: func(c taboo.Cmd) error {
			if c.Name == "workshop" && elemsContain(c.Args, "--version") {
				return errors.New("workshop --version failed")
			}
			return runFakeErr(c)
		},
	}
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	_, stderr, err := runCmd(t, env, "fix")
	if !errors.Is(err, errRunFailed) {
		t.Fatalf("run error = %v, want errRunFailed", err)
	}
	if !strings.Contains(stderr, "workshop") {
		t.Errorf("stderr missing a workshop failure line:\n%s", stderr)
	}
	if findInvocation(fake, "worktree") != nil || findInvocation(fake, "exec") != nil {
		t.Errorf("failed preflight must not execute; calls: %v", invocations(fake))
	}
}

// TestRun_PreflightInvalidConfig asserts an invalid config (repo on tmpfs fails
// validate's repo-location check) makes run refuse with errRunFailed before any
// execution, even though the workshop probe itself is healthy — proving validate
// is wired into the preflight.
func TestRun_PreflightInvalidConfig(t *testing.T) {
	root := t.TempDir()
	body := "" +
		"workshop: demo\n" +
		"base: ubuntu@24.04\n" +
		"agent: opencode\n" +
		"model: anthropic/claude\n" +
		"repo: /tmp/on-tmpfs\n" + // tmpfs path: validate's repo-location check fails
		"workflows:\n" +
		"  fix:\n" +
		"    prompt: fix it\n"
	writeTabooProject(t, root, body)
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	_, _, err := runCmd(t, env, "fix")
	if !errors.Is(err, errRunFailed) {
		t.Fatalf("run error = %v, want errRunFailed", err)
	}
	if findInvocation(fake, "worktree") != nil || findInvocation(fake, "exec") != nil {
		t.Errorf("invalid config must not execute; calls: %v", invocations(fake))
	}
}

// TestRun_DryRun asserts --dry-run prints the resolved plan (workflow, branch
// prefix, agent) to stdout and is completely side-effect free: no workshop or git
// calls reach the commander.
func TestRun_DryRun(t *testing.T) {
	root := t.TempDir()
	writeTabooProject(t, root, runProjectBody)
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	stdout, _, err := runCmd(t, env, "fix", "--dry-run")
	if err != nil {
		t.Fatalf("run --dry-run error = %v, want nil", err)
	}
	if !strings.Contains(stdout, "fix") {
		t.Errorf("plan missing workflow name:\n%s", stdout)
	}
	if !strings.Contains(stdout, "taboo/fix-") {
		t.Errorf("plan missing auto-branch prefix:\n%s", stdout)
	}
	if !strings.Contains(stdout, "opencode") {
		t.Errorf("plan missing agent:\n%s", stdout)
	}
	if len(invocations(fake)) != 0 {
		t.Errorf("--dry-run must not touch the commander; calls: %v", invocations(fake))
	}
}

// TestPromptSummary covers the one-line preview promptSummary renders for the
// dry-run plan: a short single-line prompt is shown verbatim, a multi-line or
// over-long prompt is collapsed to its (truncated) first line plus a correctly
// pluralized line count, and the count is singular for a truncated single line.
func TestPromptSummary(t *testing.T) {
	long := strings.Repeat("x", 80) // 80 runes, exceeds the 60-rune cap
	cases := []struct {
		name   string
		prompt string
		want   string
	}{
		{"short single line shown verbatim", "fix the failing tests", "fix the failing tests"},
		{"multiline appends line count", "first line\nsecond\nthird", "first line (3 lines)"},
		{"truncated single line is singular", long, strings.Repeat("x", 60) + "… (1 line)"},
		{"exactly 60 runes not truncated", strings.Repeat("y", 60), strings.Repeat("y", 60)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := promptSummary(tc.prompt); got != tc.want {
				t.Errorf("promptSummary(%q) = %q, want %q", tc.prompt, got, tc.want)
			}
		})
	}
}

// TestRun_PromptFile asserts a workflow's prompt-file is read (relative to the
// config dir) and its contents become the agent's prompt in the exec call. This
// also exercises validate's prompt-file existence check passing in the preflight.
func TestRun_PromptFile(t *testing.T) {
	root := t.TempDir()
	const promptContents = "FILE-PROMPT: refactor the parser"
	writeTabooProject(t, root, "") // create the .taboo dir first
	writePromptFile(t, root, "fix.md", promptContents)
	body := "" +
		"workshop: demo\n" +
		"base: ubuntu@24.04\n" +
		"agent: opencode\n" +
		"model: anthropic/claude\n" +
		"repo: /home/dev/repos/myproject\n" +
		"defaults:\n" +
		"  branch-prefix: taboo/\n" +
		"workflows:\n" +
		"  fix:\n" +
		"    prompt-file: fix.md\n"
	writeTabooProject(t, root, body)
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	if _, _, err := runCmd(t, env, "fix"); err != nil {
		t.Fatalf("run error = %v, want nil", err)
	}
	if findInvocation(fake, "exec", promptContents) == nil {
		t.Errorf("no exec invocation carried the prompt-file contents; calls: %v", invocations(fake))
	}
}

// TestRun_PromptFileMissing asserts a workflow whose prompt-file is configured
// but absent surfaces a precise read error (not the generic "no prompt"
// message, which would mislead a user who clearly did set prompt-file) and never
// reaches execution.
func TestRun_PromptFileMissing(t *testing.T) {
	root := t.TempDir()
	body := "" +
		"workshop: demo\n" +
		"base: ubuntu@24.04\n" +
		"agent: opencode\n" +
		"model: anthropic/claude\n" +
		"repo: /home/dev/repos/myproject\n" +
		"workflows:\n" +
		"  fix:\n" +
		"    prompt-file: missing.md\n"
	writeTabooProject(t, root, body)
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	_, _, err := runCmd(t, env, "fix")
	if err == nil {
		t.Fatal("run error = nil, want a prompt-file read error")
	}
	if !strings.Contains(err.Error(), "prompt-file") {
		t.Errorf("error = %q, want it to name the unreadable prompt-file", err.Error())
	}
	if len(invocations(fake)) != 0 {
		t.Errorf("missing prompt-file must not execute; calls: %v", invocations(fake))
	}
}

// countInvocations counts the recorded calls containing every substr.
func countInvocations(fake *fakeCommander, substrs ...string) int {
	n := 0
	for _, inv := range invocations(fake) {
		if elemsContain(inv, substrs...) {
			n++
		}
	}
	return n
}

// TestRun_IterationLoop asserts max-iterations drives the exec loop: with
// max-iterations 2 and no completion signal the agent is exec'd exactly twice
// (Setup once, Exec twice), and with a completion signal present in the exec
// output the loop stops after the first exec.
func TestRun_IterationLoop(t *testing.T) {
	t.Run("two iterations without signal", func(t *testing.T) {
		root := t.TempDir()
		body := "" +
			"workshop: demo\n" +
			"base: ubuntu@24.04\n" +
			"agent: opencode\n" +
			"model: anthropic/claude\n" +
			"repo: /home/dev/repos/myproject\n" +
			"defaults:\n" +
			"  branch-prefix: taboo/\n" +
			"  max-iterations: 2\n" +
			"workflows:\n" +
			"  fix:\n" +
			"    prompt: fix it\n"
		writeTabooProject(t, root, body)
		fake := newRunFake()
		env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

		if _, _, err := runCmd(t, env, "fix"); err != nil {
			t.Fatalf("run error = %v, want nil", err)
		}
		if got := countInvocations(fake, "exec"); got != 2 {
			t.Errorf("exec calls = %d, want 2; calls: %v", got, invocations(fake))
		}
	})

	t.Run("completion signal stops after one exec", func(t *testing.T) {
		root := t.TempDir()
		body := "" +
			"workshop: demo\n" +
			"base: ubuntu@24.04\n" +
			"agent: opencode\n" +
			"model: anthropic/claude\n" +
			"repo: /home/dev/repos/myproject\n" +
			"defaults:\n" +
			"  branch-prefix: taboo/\n" +
			"  max-iterations: 5\n" +
			"  completion-signal: ALL-DONE\n" +
			"workflows:\n" +
			"  fix:\n" +
			"    prompt: fix it\n"
		writeTabooProject(t, root, body)
		fake := &fakeCommander{
			errFn: runFakeErr,
			stdoutFn: func(c taboo.Cmd) string {
				if c.Name == "workshop" && elemsContain(c.Args, "exec") {
					return "working... ALL-DONE\n"
				}
				return runFakeStdout(c)
			},
		}
		env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

		if _, _, err := runCmd(t, env, "fix"); err != nil {
			t.Fatalf("run error = %v, want nil", err)
		}
		if got := countInvocations(fake, "exec"); got != 1 {
			t.Errorf("exec calls = %d, want 1 (stopped on signal); calls: %v", got, invocations(fake))
		}
	})
}

// TestRootCmd_WiresRun asserts the root command exposes run and dispatches to it
// (no "unknown command"), exercising the production wiring path. It uses
// --dry-run so the wiring check stays side-effect free. Mirrors
// TestRootCmd_WiresDoctor.
func TestRootCmd_WiresRun(t *testing.T) {
	root := t.TempDir()
	writeTabooProject(t, root, runProjectBody)
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	cmd := newRootCmd(env)
	cmd.SetArgs([]string{"run", "fix", "--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("root run error = %v, want nil", err)
	}
	out, _ := env.Stdout.(*bytes.Buffer)
	if !strings.Contains(out.String(), "fix") {
		t.Errorf("root run output missing plan:\n%s", out.String())
	}
}

// TestRun_NoConfig asserts that with no taboo.yaml discoverable, run errors with
// a message pointing at `taboo init` and never executes.
func TestRun_NoConfig(t *testing.T) {
	empty := t.TempDir() // no .taboo here
	fake := newRunFake()
	env := configEnv(t, fake, empty, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	_, _, err := runCmd(t, env, "fix")
	if err == nil {
		t.Fatal("run error = nil, want error")
	}
	if !strings.Contains(err.Error(), "taboo init") {
		t.Errorf("error = %q, want it to mention `taboo init`", err.Error())
	}
	if len(invocations(fake)) != 0 {
		t.Errorf("no-config run must not touch the commander; calls: %v", invocations(fake))
	}
}

// TestRun_JSONResult asserts --json emits a parseable object carrying the run's
// branch, commit, a non-empty worktree, at least one iteration, and a stop
// reason.
func TestRun_JSONResult(t *testing.T) {
	root := t.TempDir()
	writeTabooProject(t, root, runProjectBody)
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	stdout, _, err := runCmd(t, env, "fix", "--json", "--branch", "agent/custom")
	if err != nil {
		t.Fatalf("run --json error = %v, want nil", err)
	}
	var res jsonRunResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\nraw:\n%s", err, stdout)
	}
	if res.Branch != "agent/custom" {
		t.Errorf("branch = %q, want %q", res.Branch, "agent/custom")
	}
	if res.Commit != "deadbeefcafe" {
		t.Errorf("commit = %q, want %q", res.Commit, "deadbeefcafe")
	}
	if res.Worktree == "" {
		t.Errorf("worktree = %q, want non-empty", res.Worktree)
	}
	if res.Iterations != 1 {
		t.Errorf("iterations = %d, want 1", res.Iterations)
	}
	if res.StopReason != "max-iterations" {
		t.Errorf("stopReason = %q, want %q", res.StopReason, "max-iterations")
	}
}

// TestRun_ExecFailureSurfaced asserts a failure inside the run (the agent exec
// erroring) propagates out of executePlan and is reported on stderr, rather than
// being swallowed into a clean exit. Preflight and launch still succeed so the
// flow reaches the exec; only the exec call fails.
func TestRun_ExecFailureSurfaced(t *testing.T) {
	root := t.TempDir()
	writeTabooProject(t, root, runProjectBody)
	fake := &fakeCommander{
		stdoutFn: runFakeStdout,
		errFn: func(c taboo.Cmd) error {
			if c.Name == "workshop" && elemsContain(c.Args, "exec") {
				return errors.New("agent exec blew up")
			}
			return runFakeErr(c)
		},
	}
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	_, stderr, err := runCmd(t, env, "fix")
	if err == nil {
		t.Fatal("run error = nil, want the exec failure to propagate")
	}
	if !strings.Contains(err.Error(), "agent exec blew up") {
		t.Errorf("error = %q, want it to carry the underlying exec failure", err.Error())
	}
	if !strings.Contains(stderr, "agent exec blew up") {
		t.Errorf("stderr missing the underlying exec failure:\n%s", stderr)
	}
}

// TestRun_NoPromptConfigured asserts a workflow with neither prompt nor
// prompt-file (and no defaults prompt) is refused with the friendly "set prompt
// or prompt-file" message before any worktree or exec call happens.
func TestRun_NoPromptConfigured(t *testing.T) {
	root := t.TempDir()
	body := "" +
		"workshop: demo\n" +
		"base: ubuntu@24.04\n" +
		"agent: opencode\n" +
		"model: anthropic/claude\n" +
		"repo: /home/dev/repos/myproject\n" +
		"workflows:\n" +
		"  fix:\n" +
		"    max-iterations: 1\n"
	writeTabooProject(t, root, body)
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	_, _, err := runCmd(t, env, "fix")
	if err == nil {
		t.Fatal("run error = nil, want a no-prompt error")
	}
	if !strings.Contains(err.Error(), "set prompt or prompt-file") {
		t.Errorf("error = %q, want the friendly set-prompt message", err.Error())
	}
	if findInvocation(fake, "worktree") != nil || findInvocation(fake, "exec") != nil {
		t.Errorf("a no-prompt workflow must not execute; calls: %v", invocations(fake))
	}
}

// TestRun_AutoBranchNoPrefix asserts that with no defaults block (so no
// branch-prefix), the auto-generated branch still names the workflow ("fix-")
// and is not accidentally prefixed with the demo "taboo/".
func TestRun_AutoBranchNoPrefix(t *testing.T) {
	root := t.TempDir()
	body := "" +
		"workshop: demo\n" +
		"base: ubuntu@24.04\n" +
		"agent: opencode\n" +
		"model: anthropic/claude\n" +
		"repo: /home/dev/repos/myproject\n" +
		"workflows:\n" +
		"  fix:\n" +
		"    prompt: fix it\n"
	writeTabooProject(t, root, body)
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	if _, _, err := runCmd(t, env, "fix"); err != nil {
		t.Fatalf("run error = %v, want nil", err)
	}
	wt := findInvocation(fake, "git", "-C", "/home/dev/repos/myproject", "worktree", "add", "-b")
	if wt == nil {
		t.Fatalf("no worktree-add invocation; calls: %v", invocations(fake))
	}
	branch := branchOfWorktreeAdd(wt)
	if !strings.HasPrefix(branch, "fix-") {
		t.Errorf("auto branch = %q, want prefix %q", branch, "fix-")
	}
	if strings.HasPrefix(branch, "taboo/") {
		t.Errorf("auto branch = %q, want no %q prefix when no defaults block", branch, "taboo/")
	}
}

// TestRun_JSONCarriesOutput asserts the --json result carries the captured agent
// output (the field the plain form deliberately omits): a known exec stdout
// string surfaces in res.Output.
func TestRun_JSONCarriesOutput(t *testing.T) {
	root := t.TempDir()
	writeTabooProject(t, root, runProjectBody)
	fake := &fakeCommander{
		errFn: runFakeErr,
		stdoutFn: func(c taboo.Cmd) string {
			if c.Name == "workshop" && elemsContain(c.Args, "exec") {
				return "AGENT-RESULT: done\n"
			}
			return runFakeStdout(c)
		},
	}
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	stdout, _, err := runCmd(t, env, "fix", "--json", "--branch", "agent/x")
	if err != nil {
		t.Fatalf("run --json error = %v, want nil", err)
	}
	var res jsonRunResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\nraw:\n%s", err, stdout)
	}
	if !strings.Contains(res.Output, "AGENT-RESULT: done") {
		t.Errorf("res.Output = %q, want it to carry the captured agent output", res.Output)
	}
}

// branchOfWorktreeAdd returns the branch argument that follows -b in a
// `git worktree add -b <branch> <path>` invocation, or "" if absent.
func branchOfWorktreeAdd(inv []string) string {
	i := slices.Index(inv, "-b")
	if i < 0 || i+1 >= len(inv) {
		return ""
	}
	return inv[i+1]
}
