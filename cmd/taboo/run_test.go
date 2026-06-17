package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/pflag"

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

// argAfter returns the element following verb in c.Args (the workshop target
// name sits right after its verb, e.g. "launch" -> "demo-opencode"), or "" when
// verb is absent or is the final element.
func argAfter(c taboo.Cmd, verb string) string {
	i := slices.Index(c.Args, verb)
	if i < 0 || i+1 >= len(c.Args) {
		return ""
	}
	return c.Args[i+1]
}

// newStatefulRunFake builds a fake commander that models workshop persistence
// across runs so reuse is observable. Its errFn records the workshop name that
// follows a "launch" verb into a set, and answers an "info" probe by reporting
// success only for a workshop that was previously launched (else errInfoMiss).
// Every other call succeeds, reusing the shared runFakeStdout. Because the
// launched set lives on the closure, a single stateful fake reused across two
// runCmd calls sees the first run's launch when the second run probes info.
func newStatefulRunFake() *fakeCommander {
	launched := map[string]bool{}
	return &fakeCommander{
		stdoutFn: runFakeStdout,
		errFn: func(c taboo.Cmd) error {
			if c.Name != "workshop" {
				return nil
			}
			if elemsContain(c.Args, "launch") {
				launched[argAfter(c, "launch")] = true
				return nil
			}
			if elemsContain(c.Args, "info") {
				if launched[argAfter(c, "info")] {
					return nil
				}
				return errInfoMiss
			}
			return nil
		},
	}
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

// TestRun_PerAgentWorkshopProvisioning locks the provisioning behavior across
// runs that share a workshop world (one stateful fake that remembers which
// workshops were launched): two workflows with different agents each provision
// their own derived workshop, while two runs of the same agent's workflow share
// one — the second reuses it via a successful info probe instead of relaunching.
func TestRun_PerAgentWorkshopProvisioning(t *testing.T) {
	t.Run("different agents get distinct workshops", func(t *testing.T) {
		root := t.TempDir()
		body := runProjectBody +
			"  refactor:\n    agent: claude-code\n    prompt: refactor it\n"
		writeTabooProject(t, root, body)
		fake := newStatefulRunFake()
		env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

		if _, _, err := runCmd(t, env, "fix"); err != nil {
			t.Fatalf("run fix error = %v, want nil", err)
		}
		if _, _, err := runCmd(t, env, "refactor"); err != nil {
			t.Fatalf("run refactor error = %v, want nil", err)
		}

		const opencodeWS, claudeWS = "demo-opencode", "demo-claude-code"
		if got := countInvocations(fake, "launch", opencodeWS); got != 1 {
			t.Errorf("launches of %q = %d, want 1; calls: %v", opencodeWS, got, invocations(fake))
		}
		if got := countInvocations(fake, "launch", claudeWS); got != 1 {
			t.Errorf("launches of %q = %d, want 1; calls: %v", claudeWS, got, invocations(fake))
		}
	})

	t.Run("same agent reuses one workshop", func(t *testing.T) {
		root := t.TempDir()
		writeTabooProject(t, root, runProjectBody)
		fake := newStatefulRunFake()
		env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

		if _, _, err := runCmd(t, env, "fix"); err != nil {
			t.Fatalf("first run fix error = %v, want nil", err)
		}
		if _, _, err := runCmd(t, env, "fix"); err != nil {
			t.Fatalf("second run fix error = %v, want nil", err)
		}

		if got := countInvocations(fake, "launch", "demo-opencode"); got != 1 {
			t.Errorf("launches of demo-opencode = %d, want 1 (second run reuses); calls: %v", got, invocations(fake))
		}
		if findInvocation(fake, "info", "demo-opencode") == nil {
			t.Errorf("expected an info probe of demo-opencode; calls: %v", invocations(fake))
		}
	})
}

// TestRun_ModelVariationReusesWorkshop locks the acceptance criterion that two
// workflows of the same agent differing only in model share one workshop, since
// model is excluded from WorkshopName by design. The "fix" workflow uses the
// top-level model anthropic/claude; a "tune" workflow inherits the same opencode
// agent but pins openai/gpt-4o. Running both produces exactly one launch of
// demo-opencode (the second reuses it), and no launch carries a model string.
func TestRun_ModelVariationReusesWorkshop(t *testing.T) {
	root := t.TempDir()
	body := runProjectBody +
		"  tune:\n    model: openai/gpt-4o\n    prompt: tune it\n"
	writeTabooProject(t, root, body)
	fake := newStatefulRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	if _, _, err := runCmd(t, env, "fix"); err != nil {
		t.Fatalf("run fix error = %v, want nil", err)
	}
	if _, _, err := runCmd(t, env, "tune"); err != nil {
		t.Fatalf("run tune error = %v, want nil", err)
	}

	if got := countInvocations(fake, "launch", "demo-opencode"); got != 1 {
		t.Errorf("launches of demo-opencode = %d, want 1 (model variation reuses one workshop); calls: %v", got, invocations(fake))
	}
	for _, model := range []string{"anthropic/claude", "openai/gpt-4o"} {
		if findInvocation(fake, "launch", model) != nil {
			t.Errorf("a launch carried model %q; model must not influence the workshop name; calls: %v", model, invocations(fake))
		}
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

// TestRun_ModelFlagOverridesConfig asserts --model is the highest-precedence
// layer of the model chain (top-level -> workflow -> flag): the agent is exec'd
// with the flag's model (-m <model>), and the configured top-level model never
// reaches the exec.
func TestRun_ModelFlagOverridesConfig(t *testing.T) {
	root := t.TempDir()
	writeTabooProject(t, root, runProjectBody) // top-level model anthropic/claude
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	const want = "openrouter/qwen/qwen3-coder"
	if _, _, err := runCmd(t, env, "fix", "--model", want); err != nil {
		t.Fatalf("run error = %v, want nil", err)
	}
	if findInvocation(fake, "exec", "-m", want) == nil {
		t.Errorf("no exec carried the --model override %q; calls: %v", want, invocations(fake))
	}
	if findInvocation(fake, "exec", "anthropic/claude") != nil {
		t.Errorf("exec used the configured model, not the --model override; calls: %v", invocations(fake))
	}
}

// TestRun_PromptFlagOverridesConfig asserts --prompt is the highest-precedence
// prompt source: its text becomes the agent's instruction even when the selected
// workflow configures its own prompt.
func TestRun_PromptFlagOverridesConfig(t *testing.T) {
	root := t.TempDir()
	writeTabooProject(t, root, runProjectBody) // fix workflow prompt: "please fix the failing tests"
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	const want = "FLAG-PROMPT: do the thing"
	if _, _, err := runCmd(t, env, "fix", "--prompt", want); err != nil {
		t.Fatalf("run error = %v, want nil", err)
	}
	if findInvocation(fake, "exec", want) == nil {
		t.Errorf("no exec carried the --prompt override; calls: %v", invocations(fake))
	}
	if findInvocation(fake, "exec", "please fix the failing tests") != nil {
		t.Errorf("exec used the workflow prompt, not --prompt; calls: %v", invocations(fake))
	}
}

// TestRun_PromptFileFlagOverridesConfig asserts --prompt-file is read (relative
// to the .taboo dir) and overrides the workflow's own inline prompt; its contents
// become the agent's instruction.
func TestRun_PromptFileFlagOverridesConfig(t *testing.T) {
	root := t.TempDir()
	writeTabooProject(t, root, runProjectBody) // fix workflow inline prompt
	const contents = "FILE-FLAG-PROMPT: refactor the parser"
	writePromptFile(t, root, "adhoc.md", contents)
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	if _, _, err := runCmd(t, env, "fix", "--prompt-file", "adhoc.md"); err != nil {
		t.Fatalf("run error = %v, want nil", err)
	}
	if findInvocation(fake, "exec", contents) == nil {
		t.Errorf("no exec carried the --prompt-file contents; calls: %v", invocations(fake))
	}
}

// TestRun_TimeoutFlagOverridesConfig asserts --timeout overrides the workflow
// timeout: the agent exec is bounded by the flag's duration, visible as
// `--timeout <dur>` at the workshop exec seam, and the configured value is unused.
func TestRun_TimeoutFlagOverridesConfig(t *testing.T) {
	root := t.TempDir()
	writeTabooProject(t, root, runProjectBody+"    timeout: 30m\n") // fix workflow timeout 30m
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	if _, _, err := runCmd(t, env, "fix", "--timeout", "5m"); err != nil {
		t.Fatalf("run error = %v, want nil", err)
	}
	if findInvocation(fake, "exec", "--timeout", "5m0s") == nil {
		t.Errorf("no exec bounded by the --timeout override; calls: %v", invocations(fake))
	}
	if findInvocation(fake, "exec", "30m0s") != nil {
		t.Errorf("exec used the configured timeout, not --timeout; calls: %v", invocations(fake))
	}
}

// TestRun_IterationsFlagOverridesConfig asserts --iterations overrides the
// configured iteration cap: with the workflow capped at 1 but --iterations 3 and
// no completion signal, the agent is exec'd exactly three times.
func TestRun_IterationsFlagOverridesConfig(t *testing.T) {
	root := t.TempDir()
	writeTabooProject(t, root, runProjectBody+"    max-iterations: 1\n") // fix capped at 1
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	if _, _, err := runCmd(t, env, "fix", "--iterations", "3"); err != nil {
		t.Fatalf("run error = %v, want nil", err)
	}
	if got := countInvocations(fake, "exec"); got != 3 {
		t.Errorf("exec calls = %d, want 3 (flag override); calls: %v", got, invocations(fake))
	}
}

// TestRun_SignalFlagOverridesConfig asserts --signal sets the completion signal:
// with --iterations 5 and --signal FLAG-DONE, an exec whose output contains
// FLAG-DONE stops the loop after the first exec.
func TestRun_SignalFlagOverridesConfig(t *testing.T) {
	root := t.TempDir()
	writeTabooProject(t, root, runProjectBody)
	fake := &fakeCommander{
		errFn: runFakeErr,
		stdoutFn: func(c taboo.Cmd) string {
			if c.Name == "workshop" && elemsContain(c.Args, "exec") {
				return "working... FLAG-DONE\n"
			}
			return runFakeStdout(c)
		},
	}
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	if _, _, err := runCmd(t, env, "fix", "--iterations", "5", "--signal", "FLAG-DONE"); err != nil {
		t.Fatalf("run error = %v, want nil", err)
	}
	if got := countInvocations(fake, "exec"); got != 1 {
		t.Errorf("exec calls = %d, want 1 (stopped on --signal); calls: %v", got, invocations(fake))
	}
}

// TestRun_AgentFlagOverridesConfig asserts --agent overrides the resolved agent:
// the dry-run plan shows the flag's agent, not the configured top-level one.
func TestRun_AgentFlagOverridesConfig(t *testing.T) {
	root := t.TempDir()
	writeTabooProject(t, root, runProjectBody) // top-level agent opencode
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	stdout, _, err := runCmd(t, env, "fix", "--agent", "claude-code", "--dry-run")
	if err != nil {
		t.Fatalf("run --dry-run error = %v, want nil", err)
	}
	if !strings.Contains(stdout, "claude-code") {
		t.Errorf("plan agent not overridden to claude-code:\n%s", stdout)
	}
	if strings.Contains(stdout, "opencode") {
		t.Errorf("plan still shows the configured agent opencode:\n%s", stdout)
	}
}

// TestRun_UnknownAgentFlag asserts an unknown --agent is refused with a fuzzy
// "did you mean" suggestion drawn from the registry, and never executes. This is
// the only path that reaches NewProfile's unknown-agent error, since LoadConfig
// rejects unknown config agents at load time.
func TestRun_UnknownAgentFlag(t *testing.T) {
	root := t.TempDir()
	writeTabooProject(t, root, runProjectBody)
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	_, _, err := runCmd(t, env, "fix", "--agent", "claud")
	if err == nil {
		t.Fatal("run --agent claud error = nil, want an unknown-agent error")
	}
	if !strings.Contains(err.Error(), "unknown agent") || !strings.Contains(err.Error(), "claude-code") {
		t.Errorf("error = %q, want unknown agent with a claude-code suggestion", err.Error())
	}
	if len(invocations(fake)) != 0 {
		t.Errorf("unknown --agent must not execute; calls: %v", invocations(fake))
	}
}

// TestRun_AdHocPrompt asserts an ad-hoc run — `taboo run --prompt …` with no
// workflow — runs off the top-level defaults: it executes carrying the flag
// prompt, on a branch slugged for an ad-hoc run.
func TestRun_AdHocPrompt(t *testing.T) {
	root := t.TempDir()
	writeTabooProject(t, root, runProjectBody) // top-level agent/model/repo + branch-prefix
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	const want = "ADHOC: tidy the imports"
	if _, _, err := runCmd(t, env, "--prompt", want); err != nil {
		t.Fatalf("ad-hoc run error = %v, want nil", err)
	}
	if findInvocation(fake, "exec", want) == nil {
		t.Errorf("no exec carried the ad-hoc prompt; calls: %v", invocations(fake))
	}
	wt := findInvocation(fake, "git", "-C", "/home/dev/repos/myproject", "worktree", "add", "-b")
	if wt == nil {
		t.Fatalf("no worktree-add invocation; calls: %v", invocations(fake))
	}
	if branch := branchOfWorktreeAdd(wt); !strings.HasPrefix(branch, "taboo/adhoc-") {
		t.Errorf("ad-hoc branch = %q, want prefix %q", branch, "taboo/adhoc-")
	}
}

// TestRun_AdHocWithoutTopLevelDefaults asserts an ad-hoc run is refused when the
// top-level defaults are absent (here, no top-level agent): the error names the
// missing top-level config and nothing executes, so a one-off never runs
// half-configured.
func TestRun_AdHocWithoutTopLevelDefaults(t *testing.T) {
	root := t.TempDir()
	// No top-level agent: only the workflow pins one, so cfg.Profile is nil.
	body := "" +
		"workshop: demo\n" +
		"base: ubuntu@24.04\n" +
		"repo: /home/dev/repos/myproject\n" +
		"workflows:\n" +
		"  fix:\n" +
		"    agent: opencode\n" +
		"    model: anthropic/claude\n" +
		"    prompt: fix it\n"
	writeTabooProject(t, root, body)
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	_, _, err := runCmd(t, env, "--prompt", "do a quick thing")
	if err == nil {
		t.Fatal("ad-hoc run error = nil, want a top-level-defaults error")
	}
	if !strings.Contains(err.Error(), "ad-hoc") || !strings.Contains(err.Error(), "top-level") {
		t.Errorf("error = %q, want it to name the missing top-level defaults", err.Error())
	}
	if len(invocations(fake)) != 0 {
		t.Errorf("a half-configured ad-hoc run must not execute; calls: %v", invocations(fake))
	}
}

// TestRun_AdHocSkipsUnusedPromptFileCheck asserts run's preflight is run-scoped:
// an ad-hoc --prompt run proceeds even when defaults.prompt-file points at a
// missing file, because that file is never consumed. Before the preflight was
// narrowed to runConfigChecks the full validate set would stat the stale
// prompt-file and abort the run with errRunFailed; whole-config prompt-file
// linting stays the job of `taboo validate`.
func TestRun_AdHocSkipsUnusedPromptFileCheck(t *testing.T) {
	root := t.TempDir()
	body := "" +
		"workshop: demo\n" +
		"base: ubuntu@24.04\n" +
		"agent: opencode\n" +
		"model: anthropic/claude\n" +
		"repo: /home/dev/repos/myproject\n" +
		"defaults:\n" +
		"  branch-prefix: taboo/\n" +
		"  prompt-file: gone.md\n" + // referenced but never created — and never consumed by an ad-hoc run
		"workflows:\n" +
		"  fix:\n" +
		"    prompt: fix it\n"
	writeTabooProject(t, root, body)
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	const want = "ADHOC: tidy the imports"
	if _, _, err := runCmd(t, env, "--prompt", want); err != nil {
		t.Fatalf("ad-hoc run with a stale unused defaults.prompt-file error = %v, want nil", err)
	}
	if findInvocation(fake, "exec", want) == nil {
		t.Errorf("ad-hoc run did not execute despite the stale prompt-file being unused; calls: %v", invocations(fake))
	}
}

// TestRun_BareRunDefaultWorkflow asserts a bare `taboo run` (no positional, no
// prompt flag) runs the configured default-workflow.
func TestRun_BareRunDefaultWorkflow(t *testing.T) {
	root := t.TempDir()
	body := runProjectBody +
		"  refactor:\n    prompt: refactor it\n" +
		"default-workflow: refactor\n"
	writeTabooProject(t, root, body)
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	if _, _, err := runCmd(t, env); err != nil {
		t.Fatalf("bare run error = %v, want nil", err)
	}
	if findInvocation(fake, "exec", "refactor it") == nil {
		t.Errorf("bare run did not execute the default-workflow; calls: %v", invocations(fake))
	}
	wt := findInvocation(fake, "git", "-C", "/home/dev/repos/myproject", "worktree", "add", "-b")
	if wt == nil {
		t.Fatalf("no worktree-add invocation; calls: %v", invocations(fake))
	}
	if branch := branchOfWorktreeAdd(wt); !strings.HasPrefix(branch, "taboo/refactor-") {
		t.Errorf("branch = %q, want prefix taboo/refactor-", branch)
	}
}

// TestRun_BareRunNoDefaultErrors asserts a bare run with workflows but no
// default-workflow refuses (listing the available workflows) and never executes —
// the tool never guesses which workflow the user meant.
func TestRun_BareRunNoDefaultErrors(t *testing.T) {
	root := t.TempDir()
	writeTabooProject(t, root, runProjectBody+"  refactor:\n    prompt: refactor it\n")
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	_, _, err := runCmd(t, env)
	if err == nil {
		t.Fatal("bare run error = nil, want an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "default-workflow") {
		t.Errorf("error = %q, want it to mention default-workflow", msg)
	}
	if !strings.Contains(msg, "fix") || !strings.Contains(msg, "refactor") {
		t.Errorf("error = %q, want it to list the available workflows", msg)
	}
	if len(invocations(fake)) != 0 {
		t.Errorf("a bare run with no default must not execute; calls: %v", invocations(fake))
	}
}

// TestRun_BareRunNoWorkflowsErrors asserts a bare run against a config with no
// workflows at all refuses with guidance to add a workflow or pass --prompt, and
// never executes.
func TestRun_BareRunNoWorkflowsErrors(t *testing.T) {
	root := t.TempDir()
	body := "" +
		"workshop: demo\n" +
		"base: ubuntu@24.04\n" +
		"agent: opencode\n" +
		"model: anthropic/claude\n" +
		"repo: /home/dev/repos/myproject\n"
	writeTabooProject(t, root, body)
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	_, _, err := runCmd(t, env)
	if err == nil {
		t.Fatal("bare run error = nil, want an error")
	}
	if !strings.Contains(err.Error(), "--prompt") {
		t.Errorf("error = %q, want it to suggest --prompt", err.Error())
	}
	if len(invocations(fake)) != 0 {
		t.Errorf("must not execute; calls: %v", invocations(fake))
	}
}

// TestRun_DefaultWorkflowUndefined asserts a default-workflow naming a workflow
// that is not defined is refused (naming the bad default and the real workflows)
// rather than silently doing nothing.
func TestRun_DefaultWorkflowUndefined(t *testing.T) {
	root := t.TempDir()
	writeTabooProject(t, root, runProjectBody+"default-workflow: ghost\n")
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	_, _, err := runCmd(t, env)
	if err == nil {
		t.Fatal("bare run error = nil, want an undefined-default error")
	}
	if !strings.Contains(err.Error(), "ghost") || !strings.Contains(err.Error(), "fix") {
		t.Errorf("error = %q, want it to name the bad default and the real workflows", err.Error())
	}
	if len(invocations(fake)) != 0 {
		t.Errorf("an undefined default-workflow must not execute; calls: %v", invocations(fake))
	}
}

// TestRun_FlagSet asserts the run command registers exactly the documented
// flag-shaped subset and nothing more — no structural or typed flags. The set is
// the acceptance-criterion list; cobra's auto-added "help" flag is ignored.
func TestRun_FlagSet(t *testing.T) {
	want := []string{
		"prompt", "prompt-file", "branch", "model", "agent",
		"timeout", "iterations", "signal", "dry-run", "yes", "json",
	}
	var got []string
	newRunCmd(Env{}).Flags().VisitAll(func(f *pflag.Flag) {
		if f.Name == "help" {
			return
		}
		got = append(got, f.Name)
	})
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("run flag set = %v, want exactly %v", got, want)
	}
}

// TestRunNeedsConfirm covers the pure decision behind --yes: a real run pauses
// for confirmation only at a TTY without --yes; --yes and any non-interactive
// caller (a pipe, CI) proceed without prompting.
func TestRunNeedsConfirm(t *testing.T) {
	cases := []struct {
		name        string
		interactive bool
		yes         bool
		want        bool
	}{
		{"interactive without --yes confirms", true, false, true},
		{"--yes skips confirm at a TTY", true, true, false},
		{"non-interactive proceeds", false, false, false},
		{"non-interactive with --yes proceeds", false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runNeedsConfirm(tc.interactive, &runOptions{yes: tc.yes}); got != tc.want {
				t.Errorf("runNeedsConfirm(interactive=%v, yes=%v) = %v, want %v", tc.interactive, tc.yes, got, tc.want)
			}
		})
	}
}

// TestPromptConfirm covers the y/N read: only an explicit yes proceeds; a blank
// line (the default), a no, EOF, or junk all decline, so an accidental Enter
// never launches a run.
func TestPromptConfirm(t *testing.T) {
	plan := runPlan{
		workflow: "fix",
		branch:   "taboo/fix-x",
		runnerConfig: taboo.Config{
			Workshop: "demo",
			Agent:    taboo.OpenCode("anthropic/claude"),
		},
	}
	cases := []struct {
		in   string
		want bool
	}{
		{"y\n", true}, {"yes\n", true}, {"Y\n", true}, {" yes \n", true},
		{"n\n", false}, {"\n", false}, {"", false}, {"nope\n", false},
	}
	for _, tc := range cases {
		env := Env{Stdin: strings.NewReader(tc.in), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
		got, err := promptConfirm(env, plan)
		if err != nil {
			t.Fatalf("promptConfirm(%q) err = %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("promptConfirm(%q) = %v, want %v", tc.in, got, tc.want)
		}
		if errBuf, _ := env.Stderr.(*bytes.Buffer); !strings.Contains(errBuf.String(), "Continue?") {
			t.Errorf("promptConfirm(%q) did not print a confirmation prompt to stderr", tc.in)
		}
	}
}

// TestRun_ConfirmDeclineAborts drives the integrated decline path: at an
// (injected) TTY without --yes, answering "n" to the confirmation prints
// "Aborted.", exits cleanly (nil), and runs nothing — no worktree is added and no
// agent is exec'd. It complements the pure TestRunNeedsConfirm/TestPromptConfirm
// by covering runRun's abort branch end to end.
func TestRun_ConfirmDeclineAborts(t *testing.T) {
	root := t.TempDir()
	writeTabooProject(t, root, runProjectBody)
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})
	env.Interactive = func() bool { return true }
	env.Stdin = strings.NewReader("n\n")

	_, stderr, err := runCmd(t, env, "fix")
	if err != nil {
		t.Fatalf("declined run error = %v, want nil", err)
	}
	if !strings.Contains(stderr, "Aborted.") {
		t.Errorf("stderr missing the abort notice:\n%s", stderr)
	}
	if findInvocation(fake, "worktree") != nil || findInvocation(fake, "exec") != nil {
		t.Errorf("a declined run must not execute; calls: %v", invocations(fake))
	}
}

// TestRun_ModelPrecedenceLadder verifies the full model precedence ladder
// (top-level config -> workflow -> --model flag) through the dry-run plan: the
// top-level model applies when nothing overrides it, a workflow model overrides
// the top level, and --model overrides both. It is the precedence-unit-test
// rendering of the acceptance criterion against one representative param.
func TestRun_ModelPrecedenceLadder(t *testing.T) {
	const topLevel = "anthropic/claude"
	cases := []struct {
		name    string
		wfModel string // workflow-level model ("" = unset)
		flag    string // --model flag ("" = unset)
		want    string
	}{
		{"top-level applies", "", "", topLevel},
		{"workflow overrides top-level", "openrouter/wf-model", "", "openrouter/wf-model"},
		{"flag overrides workflow", "openrouter/wf-model", "openrouter/flag-model", "openrouter/flag-model"},
		{"flag overrides top-level", "", "openrouter/flag-model", "openrouter/flag-model"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			body := "" +
				"workshop: demo\nbase: ubuntu@24.04\nagent: opencode\nmodel: " + topLevel + "\n" +
				"repo: /home/dev/repos/myproject\n" +
				"workflows:\n  fix:\n    prompt: fix it\n"
			if tc.wfModel != "" {
				body += "    model: " + tc.wfModel + "\n"
			}
			writeTabooProject(t, root, body)
			fake := newRunFake()
			env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

			args := []string{"fix", "--dry-run"}
			if tc.flag != "" {
				args = append(args, "--model", tc.flag)
			}
			stdout, _, err := runCmd(t, env, args...)
			if err != nil {
				t.Fatalf("run --dry-run error = %v, want nil", err)
			}
			if !strings.Contains(stdout, tc.want) {
				t.Errorf("resolved model = not %q\n%s", tc.want, stdout)
			}
			if tc.want != topLevel && strings.Contains(stdout, topLevel) {
				t.Errorf("top-level model %q leaked despite an override:\n%s", topLevel, stdout)
			}
		})
	}
}

// TestRun_AdHocAgentFromFlag asserts an ad-hoc run is allowed when --agent
// supplies the agent even with no top-level agent: --agent is the highest agent
// precedence layer, so a run that resolves fully through it must not be refused by
// the ad-hoc gate.
func TestRun_AdHocAgentFromFlag(t *testing.T) {
	root := t.TempDir()
	// No top-level agent, but everything else an ad-hoc run needs is present.
	body := "" +
		"workshop: demo\n" +
		"base: ubuntu@24.04\n" +
		"model: anthropic/claude\n" +
		"repo: /home/dev/repos/myproject\n"
	writeTabooProject(t, root, body)
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	stdout, _, err := runCmd(t, env, "--prompt", "tidy the imports", "--agent", "opencode", "--dry-run")
	if err != nil {
		t.Fatalf("ad-hoc run with --agent error = %v, want nil", err)
	}
	if !strings.Contains(stdout, "opencode") {
		t.Errorf("dry-run plan missing the --agent agent:\n%s", stdout)
	}
	if !strings.Contains(stdout, adhocLabel) {
		t.Errorf("dry-run plan missing the ad-hoc label:\n%s", stdout)
	}
}

// TestRun_WorkflowBeatsDefaults locks the middle precedence rung for the two
// params with a workflow level: with no flag, a workflow timeout and
// max-iterations override the defaults: block. It guards resolveTimeout and
// resolveMaxIterations against a regression that read defaults before the
// workflow (a swap the flag-override tests alone would not catch).
func TestRun_WorkflowBeatsDefaults(t *testing.T) {
	root := t.TempDir()
	body := "" +
		"workshop: demo\nbase: ubuntu@24.04\nagent: opencode\nmodel: anthropic/claude\n" +
		"repo: /home/dev/repos/myproject\n" +
		"defaults:\n  timeout: 30m\n  max-iterations: 2\n" +
		"workflows:\n  fix:\n    prompt: fix it\n    timeout: 5m\n    max-iterations: 4\n"
	writeTabooProject(t, root, body)
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	if _, _, err := runCmd(t, env, "fix"); err != nil {
		t.Fatalf("run error = %v, want nil", err)
	}
	if findInvocation(fake, "exec", "--timeout", "5m0s") == nil {
		t.Errorf("workflow timeout did not beat the defaults timeout; calls: %v", invocations(fake))
	}
	if findInvocation(fake, "exec", "--timeout", "30m0s") != nil {
		t.Errorf("defaults timeout leaked past the workflow value; calls: %v", invocations(fake))
	}
	if got := countInvocations(fake, "exec"); got != 4 {
		t.Errorf("exec calls = %d, want 4 (workflow max-iterations beats defaults); calls: %v", got, invocations(fake))
	}
}

// TestRun_PromptFlagBeatsWorkflowPromptFile closes the one prompt-precedence edge
// the flag-override tests miss: --prompt must beat a workflow prompt-file, not
// just a workflow inline prompt. Guards resolvePrompt against reading the workflow
// file before the flag.
func TestRun_PromptFlagBeatsWorkflowPromptFile(t *testing.T) {
	root := t.TempDir()
	writeTabooProject(t, root, "") // create the .taboo dir first
	writePromptFile(t, root, "wf.md", "FILE: from the workflow prompt-file")
	body := "" +
		"workshop: demo\nbase: ubuntu@24.04\nagent: opencode\nmodel: anthropic/claude\n" +
		"repo: /home/dev/repos/myproject\n" +
		"workflows:\n  fix:\n    prompt-file: wf.md\n"
	writeTabooProject(t, root, body)
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	const want = "INLINE: from the --prompt flag"
	if _, _, err := runCmd(t, env, "fix", "--prompt", want); err != nil {
		t.Fatalf("run error = %v, want nil", err)
	}
	if findInvocation(fake, "exec", want) == nil {
		t.Errorf("--prompt did not beat the workflow prompt-file; calls: %v", invocations(fake))
	}
	if findInvocation(fake, "exec", "FILE: from the workflow prompt-file") != nil {
		t.Errorf("workflow prompt-file leaked past --prompt; calls: %v", invocations(fake))
	}
}

// TestRun_BareRunDefaultWorkflowFlagOverride asserts a flag layers over a
// bare-run default-workflow selection just as it does over a named one. It uses
// --model (not --prompt) deliberately: --prompt would route to the ad-hoc branch
// instead of selecting the default-workflow, so it would not exercise this cell of
// the selection x precedence matrix.
func TestRun_BareRunDefaultWorkflowFlagOverride(t *testing.T) {
	root := t.TempDir()
	body := runProjectBody +
		"  refactor:\n    prompt: refactor it\n" +
		"default-workflow: refactor\n"
	writeTabooProject(t, root, body)
	fake := newRunFake()
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-x"})

	const wantModel = "openrouter/override-model"
	if _, _, err := runCmd(t, env, "--model", wantModel); err != nil {
		t.Fatalf("bare run error = %v, want nil", err)
	}
	if findInvocation(fake, "exec", "refactor it") == nil {
		t.Errorf("bare run did not select the default-workflow; calls: %v", invocations(fake))
	}
	if findInvocation(fake, "exec", "-m", wantModel) == nil {
		t.Errorf("--model did not override on a bare default-workflow run; calls: %v", invocations(fake))
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
