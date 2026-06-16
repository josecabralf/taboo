package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	taboo "github.com/josecabralf/taboo/pkg/taboo"
)

// runValidate executes a freshly built validate command with the given env and
// extra flags, returning the captured stdout and the execute error. Mirror of
// runDoctor (doctor_test.go).
func runValidate(t *testing.T, env Env, args ...string) (string, error) {
	t.Helper()
	cmd := newValidateCmd(env)
	cmd.SetArgs(args)
	err := cmd.Execute()
	out, _ := env.Stdout.(*bytes.Buffer)
	if out == nil {
		t.Fatal("runValidate: env.Stdout must be a *bytes.Buffer")
	}
	return out.String(), err
}

// writePromptFile creates a file under the project's .taboo dir (next to
// taboo.yaml) so a relative prompt-file in the config resolves to a real file.
func writePromptFile(t *testing.T, root, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, ".taboo", name), []byte(body), 0o600); err != nil {
		t.Fatalf("write prompt file %q: %v", name, err)
	}
}

// TestValidate_TracerValidConfig is the tracer bullet: a fully valid taboo.yaml
// (known agent, well-formed model, a present prompt file, and a repo on
// persistent storage that the git probe accepts) validates clean — no error,
// result OK, and every emitted check is ok.
func TestValidate_TracerValidConfig(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	body := "" +
		"workshop: demo\n" +
		"base: ubuntu@24.04\n" +
		"agent: opencode\n" +
		"model: openrouter/qwen/qwen3-coder-plus\n" +
		"repo: /home/me/repo\n" +
		"defaults:\n" +
		"  prompt-file: prompt.md\n"
	writeTabooProject(t, root, body)
	writePromptFile(t, root, "prompt.md", "do the thing\n")
	fake := &fakeCommander{stdoutFn: okHostStdout}
	env := configEnv(t, fake, root, nil)

	out, err := runValidate(t, env)
	if err != nil {
		t.Fatalf("validate error = %v, want nil\n%s", err, out)
	}
	if !strings.Contains(out, "config correctness") {
		t.Errorf("output missing the validate title:\n%s", out)
	}
	if !strings.Contains(out, "result: OK") {
		t.Errorf("output missing 'result: OK':\n%s", out)
	}
	for _, name := range []string{"config", "agent", "prompt-file/prompt.md", "repo-path", "repo-git"} {
		if got := findStatus(out, name); got != "ok" {
			t.Errorf("check %q status = %q, want ok\nfull output:\n%s", name, got, out)
		}
	}
}

// TestValidate_RejectsBadConfig asserts validate treats a config it cannot use as
// a single terminal error — exactly one config/error check, no downstream groups.
// Like LoadConfig, taboo.yaml is a strict single document, so malformed YAML, an
// unknown field, an empty document, and a multi-document file are all rejected.
func TestValidate_RejectsBadConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		body    string
		wantMsg string // substring the config error message must contain
	}{
		// wantMsg uses multi-word phrases: t.TempDir() embeds the subtest name in the
		// path, so a single bare word (e.g. "empty") could match the path rather than
		// the message. Phrases with spaces cannot appear in a temp path.
		{name: "malformed yaml", body: "workshop: demo\n  bad-indent: : :\n", wantMsg: "invalid taboo.yaml"},
		{name: "unknown field", body: "workshop: demo\nbogus: nope\n", wantMsg: "invalid taboo.yaml"},
		{name: "empty config", body: "", wantMsg: "config is empty"},
		{name: "multiple documents", body: "workshop: demo\n---\nworkshop: other\n", wantMsg: "multiple YAML documents"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeTabooProject(t, root, tt.body)
			fake := &fakeCommander{stdoutFn: okHostStdout}
			env := configEnv(t, fake, root, nil)
			checks := validateChecks(context.Background(), env, realStat)
			if len(checks) != 1 {
				t.Fatalf("validateChecks = %d checks, want 1 (terminal config error only): %+v", len(checks), checks)
			}
			if checks[0].Name != "config" || checks[0].Status != statusError {
				t.Fatalf("checks[0] = %+v, want config/error", checks[0])
			}
			if !strings.Contains(checks[0].Message, tt.wantMsg) {
				t.Errorf("config error message = %q, want it to contain %q", checks[0].Message, tt.wantMsg)
			}
		})
	}
}

// TestValidate_UnknownAgent asserts an unknown referenced agent hard-fails with a
// precise "did you mean <closest>" suggestion and a non-zero exit — whether the
// agent is the top-level default or a workflow override.
func TestValidate_UnknownAgent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		body           string
		badCheck       string
		wantSuggestion string
	}{
		{
			name:           "top-level typo",
			body:           "workshop: demo\nagent: claud\nmodel: claude-sonnet-4-6\nrepo: /home/me/repo\n",
			badCheck:       "agent/claud",
			wantSuggestion: "claude-code",
		},
		{
			name: "workflow-level typo",
			body: "workshop: demo\nagent: opencode\nmodel: openrouter/qwen/q\nrepo: /home/me/repo\n" +
				"workflows:\n  fix:\n    agent: opencpde\n",
			badCheck:       "agent/opencpde",
			wantSuggestion: "opencode",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeTabooProject(t, root, tt.body)
			fake := &fakeCommander{stdoutFn: okHostStdout}
			env := configEnv(t, fake, root, nil)

			// Command exits non-zero on the failure and renders the failing check.
			out, err := runValidate(t, env)
			if !errors.Is(err, errValidateFailed) {
				t.Fatalf("validate error = %v, want errValidateFailed\n%s", err, out)
			}
			if got := findStatus(out, tt.badCheck); got != "error" {
				t.Errorf("%s status = %q, want error\n%s", tt.badCheck, got, out)
			}
			// The precise "did you mean <closest>" is bound to the offending check's
			// own message, not merely present somewhere in the output.
			checks := validateChecks(context.Background(), env, realStat)
			c := findCheck(checks, tt.badCheck)
			if c == nil {
				t.Fatalf("no %s check emitted\nchecks: %+v", tt.badCheck, checks)
			}
			if !strings.Contains(c.Message, "did you mean") || !strings.Contains(c.Message, tt.wantSuggestion) {
				t.Errorf("%s message = %q, want it to suggest %q", tt.badCheck, c.Message, tt.wantSuggestion)
			}
		})
	}
}

// TestValidate_EmptyModel asserts a referenced agent with no model hard-fails:
// model is required. The workflow inherits the (known) top-level agent but no
// model is configured anywhere, so its effective model is empty too.
func TestValidate_EmptyModel(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	body := "" +
		"workshop: demo\n" +
		"agent: opencode\n" +
		"repo: /home/me/repo\n" +
		"workflows:\n  fix:\n    prompt: do it\n"
	writeTabooProject(t, root, body)
	fake := &fakeCommander{stdoutFn: okHostStdout}
	env := configEnv(t, fake, root, nil)

	out, err := runValidate(t, env)
	if !errors.Is(err, errValidateFailed) {
		t.Fatalf("validate error = %v, want errValidateFailed\n%s", err, out)
	}
	if got := findStatus(out, "model/opencode"); got != "error" {
		t.Errorf("model/opencode status = %q, want error\n%s", got, out)
	}
	if !strings.Contains(out, "model is required") {
		t.Errorf("output missing 'model is required' message:\n%s", out)
	}
}

// findCheck returns the check with the given name, or nil if absent. Unlike
// statusOf it exposes the whole check so a test can assert the message too.
func findCheck(checks []check, name string) *check {
	for i := range checks {
		if checks[i].Name == name {
			return &checks[i]
		}
	}
	return nil
}

// TestValidate_BadModelFormatWarns asserts a model that does not match the
// agent's format hint produces a WARN, never an error, and the command still
// exits 0: the heuristic is advisory, so a deliberate but unusual model is
// allowed through. A copilot model (no opinion) never warns even when foreign.
func TestValidate_BadModelFormatWarns(t *testing.T) {
	t.Parallel()

	t.Run("foreign model for claude-code warns", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeTabooProject(t, root, "workshop: demo\nagent: claude-code\nmodel: gpt-5\nrepo: /home/me/repo\n")
		fake := &fakeCommander{stdoutFn: okHostStdout}
		env := configEnv(t, fake, root, nil)

		// A format mismatch is advisory: the command must still exit 0.
		out, err := runValidate(t, env)
		if err != nil {
			t.Fatalf("validate error = %v, want nil (a format mismatch must not fail)\n%s", err, out)
		}
		// The warn is keyed by agent AND model so two bad models for one agent stay
		// distinct checks; its message quotes the agent's expected format and how to
		// silence it (that advisory text is the whole value of the warn).
		checks := validateChecks(context.Background(), env, realStat)
		c := findCheck(checks, "model/claude-code/gpt-5")
		if c == nil || c.Status != statusWarn {
			t.Fatalf("model/claude-code/gpt-5 = %+v, want a warn check\nchecks: %+v", c, checks)
		}
		if !strings.Contains(c.Message, "a Claude model id") || !strings.Contains(c.Message, "set it intentionally") {
			t.Errorf("warn message = %q, want it to quote the expected format and the silence hint", c.Message)
		}
	})

	t.Run("any model for copilot is fine", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeTabooProject(t, root, "workshop: demo\nagent: copilot\nmodel: some-exotic-model\nrepo: /home/me/repo\n")
		fake := &fakeCommander{stdoutFn: okHostStdout}
		env := configEnv(t, fake, root, nil)

		checks := validateChecks(context.Background(), env, realStat)
		for _, c := range checks {
			if strings.HasPrefix(c.Name, "model/") {
				t.Errorf("copilot emitted %+v, want no model check (copilot has no format opinion)", c)
			}
		}
		if anyError(checks) {
			t.Errorf("anyError = true, want false (a clean copilot config)\nchecks: %+v", checks)
		}
	})
}

// TestValidate_WorkflowModelOverrideWarns pins the model half of the
// workflow-then-top-level precedence (orElse(wf.Model, cfg.Model)) end-to-end and
// the per-(agent,model) check naming. The top-level model is valid for
// claude-code, but two workflows override it with foreign models: each override
// must surface as its OWN warn keyed by the workflow's model. An inverted
// precedence (top model winning) would drop the warns; a name omitting the model
// would collapse the two warns into one. The agent side already has this coverage
// (TestValidate_UnknownAgent's workflow case); this is its model-side twin.
func TestValidate_WorkflowModelOverrideWarns(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	body := "" +
		"workshop: demo\n" +
		"agent: claude-code\n" +
		"model: sonnet\n" + // valid for claude-code → must not warn
		"repo: /home/me/repo\n" +
		"workflows:\n" +
		"  a:\n    model: gpt-5\n" +
		"  b:\n    model: gpt-4\n"
	writeTabooProject(t, root, body)
	fake := &fakeCommander{stdoutFn: okHostStdout}
	env := configEnv(t, fake, root, nil)

	checks := validateChecks(context.Background(), env, realStat)
	for _, name := range []string{"model/claude-code/gpt-5", "model/claude-code/gpt-4"} {
		if c := findCheck(checks, name); c == nil || c.Status != statusWarn {
			t.Errorf("%s = %+v, want a warn check\nchecks: %+v", name, c, checks)
		}
	}
	if c := findCheck(checks, "model/claude-code/sonnet"); c != nil {
		t.Errorf("model/claude-code/sonnet = %+v, want absent (sonnet is valid for claude-code)", c)
	}
	if anyError(checks) {
		t.Errorf("anyError = true, want false (format mismatches are advisory warnings)\nchecks: %+v", checks)
	}
}

// TestReferencedModels pins the effective (agent, model) bindings the config
// produces: workflow-then-top-level precedence for both fields, inheritance,
// dedup of identical bindings, dropping a binding with no agent, and sorted
// order. It guards the policy independently of rendering — an inverted orElse at
// the model call site fails the "overrides model" case here.
func TestReferencedModels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  taboo.ProjectConfig
		want []agentModel
	}{
		{
			name: "top-level only",
			cfg:  taboo.ProjectConfig{Agent: "opencode", Model: "openrouter/x"},
			want: []agentModel{{"opencode", "openrouter/x"}},
		},
		{
			name: "workflow overrides model, inherits agent",
			cfg: taboo.ProjectConfig{
				Agent: "claude-code", Model: "sonnet",
				Workflows: map[string]taboo.Workflow{"a": {Model: "opus"}},
			},
			// workflow model wins (orElse(wf.Model, cfg.Model)); agent inherited.
			want: []agentModel{{"claude-code", "opus"}, {"claude-code", "sonnet"}},
		},
		{
			name: "workflow overrides agent, inherits model",
			cfg: taboo.ProjectConfig{
				Agent: "claude-code", Model: "sonnet",
				Workflows: map[string]taboo.Workflow{"a": {Agent: "opencode"}},
			},
			want: []agentModel{{"claude-code", "sonnet"}, {"opencode", "sonnet"}},
		},
		{
			name: "identical bindings deduped",
			cfg: taboo.ProjectConfig{
				Agent: "opencode", Model: "m",
				Workflows: map[string]taboo.Workflow{"a": {}, "b": {}},
			},
			want: []agentModel{{"opencode", "m"}},
		},
		{
			name: "binding with no agent dropped",
			cfg: taboo.ProjectConfig{
				Model:     "orphan",
				Workflows: map[string]taboo.Workflow{"a": {Model: "another"}},
			},
			want: nil,
		},
		{
			name: "sorted by agent then model",
			cfg: taboo.ProjectConfig{
				Agent: "opencode", Model: "z",
				Workflows: map[string]taboo.Workflow{
					"w1": {Agent: "claude-code", Model: "b"},
					"w2": {Agent: "claude-code", Model: "a"},
				},
			},
			want: []agentModel{{"claude-code", "a"}, {"claude-code", "b"}, {"opencode", "z"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := referencedModels(tt.cfg); !slices.Equal(got, tt.want) {
				t.Errorf("referencedModels() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestReferencedAgents pins the distinct, sorted set of referenced agent names:
// top-level plus each workflow's effective agent (its own, falling back to the
// top level), deduped, with empties dropped.
func TestReferencedAgents(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  taboo.ProjectConfig
		want []string
	}{
		{name: "top-level only", cfg: taboo.ProjectConfig{Agent: "opencode"}, want: []string{"opencode"}},
		{
			name: "workflow override deduped and sorted",
			cfg: taboo.ProjectConfig{
				Agent: "opencode",
				Workflows: map[string]taboo.Workflow{
					"a": {Agent: "claude-code"},
					"b": {}, // inherits opencode
				},
			},
			want: []string{"claude-code", "opencode"},
		},
		{name: "no agent anywhere", cfg: taboo.ProjectConfig{Model: "m"}, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := referencedAgents(tt.cfg); !slices.Equal(got, tt.want) {
				t.Errorf("referencedAgents() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestValidate_PromptFile asserts referenced prompt files are confirmed to exist,
// resolved relative to the config's directory: a present file is ok, a missing
// one hard-fails.
func TestValidate_PromptFile(t *testing.T) {
	t.Parallel()

	t.Run("present file ok", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		body := "workshop: demo\nagent: opencode\nmodel: openrouter/qwen/q\nrepo: /home/me/repo\n" +
			"defaults:\n  prompt-file: run.md\n"
		writeTabooProject(t, root, body)
		writePromptFile(t, root, "run.md", "go\n")
		fake := &fakeCommander{stdoutFn: okHostStdout}
		env := configEnv(t, fake, root, nil)

		out, err := runValidate(t, env)
		if err != nil {
			t.Fatalf("validate error = %v, want nil\n%s", err, out)
		}
		if got := findStatus(out, "prompt-file/run.md"); got != "ok" {
			t.Errorf("prompt-file/run.md status = %q, want ok\n%s", got, out)
		}
	})

	t.Run("missing file fails", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		body := "workshop: demo\nagent: opencode\nmodel: openrouter/qwen/q\nrepo: /home/me/repo\n" +
			"workflows:\n  fix:\n    prompt-file: nope.md\n"
		writeTabooProject(t, root, body) // nope.md is never created
		fake := &fakeCommander{stdoutFn: okHostStdout}
		env := configEnv(t, fake, root, nil)

		out, err := runValidate(t, env)
		if !errors.Is(err, errValidateFailed) {
			t.Fatalf("validate error = %v, want errValidateFailed\n%s", err, out)
		}
		if got := findStatus(out, "prompt-file/nope.md"); got != "error" {
			t.Errorf("prompt-file/nope.md status = %q, want error\n%s", got, out)
		}
	})
}

// TestValidate_Repo asserts the configured repo is checked: it must be set, on
// persistent storage (not tmpfs), and a git work tree. The location and git
// checks are the same leaves doctor uses; the missing-repo failure is validate's
// own (a repo is required to run).
func TestValidate_Repo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		repoLine string // the "repo: ..." line, or "" to omit it entirely
		gitFails bool
		badCheck string
	}{
		{name: "no repo configured", repoLine: "", gitFails: false, badCheck: "repo"},
		{name: "repo under tmpfs", repoLine: "repo: /tmp/my-repo\n", gitFails: false, badCheck: "repo-path"},
		{name: "repo not a git work tree", repoLine: "repo: /home/me/not-git\n", gitFails: true, badCheck: "repo-git"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			body := "workshop: demo\nagent: opencode\nmodel: openrouter/qwen/q\n" + tt.repoLine
			writeTabooProject(t, root, body)
			fake := &fakeCommander{stdoutFn: okHostStdout}
			if tt.gitFails {
				fake.errFn = failOnArgs("git", "-C")
			}
			env := configEnv(t, fake, root, nil)

			// Inspect the checks directly (statusOf matches the exact check name);
			// findStatus on rendered output is unreliable here because t.TempDir()
			// embeds "repo" (the subtest name) in the parsed-config path line.
			checks := validateChecks(context.Background(), env, realStat)
			if got := statusOf(checks, tt.badCheck); got != "error" {
				t.Errorf("%s status = %q, want error\nchecks: %+v", tt.badCheck, got, checks)
			}
			if !anyError(checks) {
				t.Errorf("anyError = false, want true (a repo failure must drive a non-zero exit)\nchecks: %+v", checks)
			}
		})
	}
}

// TestValidate_NoConfigFound asserts that with no taboo.yaml discoverable from the
// working directory, validate fails with a single config error pointing the user
// at `taboo init`, and exits non-zero.
func TestValidate_NoConfigFound(t *testing.T) {
	t.Parallel()
	fake := &fakeCommander{stdoutFn: okHostStdout}
	env := configEnv(t, fake, t.TempDir(), nil) // a bare temp dir holds no taboo.yaml

	out, err := runValidate(t, env)
	if !errors.Is(err, errValidateFailed) {
		t.Fatalf("validate error = %v, want errValidateFailed\n%s", err, out)
	}
	if !strings.Contains(out, "no taboo.yaml found") {
		t.Errorf("output missing 'no taboo.yaml found':\n%s", out)
	}
	if !strings.Contains(out, "taboo init") {
		t.Errorf("output missing the 'taboo init' hint:\n%s", out)
	}
}

// TestValidate_JSON asserts --json emits the generic report document: ok=true on a
// clean config (exit 0), and ok=false with the offending check marked error on a
// broken one (exit non-zero).
func TestValidate_JSON(t *testing.T) {
	t.Parallel()

	t.Run("clean config ok true", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeTabooProject(t, root, "workshop: demo\nagent: opencode\nmodel: openrouter/qwen/q\nrepo: /home/me/repo\n")
		fake := &fakeCommander{stdoutFn: okHostStdout}
		env := configEnv(t, fake, root, nil)

		out, err := runValidate(t, env, "--json")
		if err != nil {
			t.Fatalf("validate --json error = %v, want nil\n%s", err, out)
		}
		rep := decodeJSONReport(t, out)
		if !rep.OK {
			t.Errorf("rep.OK = false, want true\n%s", out)
		}
		if len(rep.Checks) == 0 {
			t.Fatal("rep.Checks empty, want config checks")
		}
	})

	t.Run("broken config ok false", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeTabooProject(t, root, "workshop: demo\nagent: claud\nmodel: claude-sonnet-4-6\nrepo: /home/me/repo\n")
		fake := &fakeCommander{stdoutFn: okHostStdout}
		env := configEnv(t, fake, root, nil)

		out, err := runValidate(t, env, "--json")
		if !errors.Is(err, errValidateFailed) {
			t.Fatalf("validate --json error = %v, want errValidateFailed\n%s", err, out)
		}
		rep := decodeJSONReport(t, out)
		if rep.OK {
			t.Errorf("rep.OK = true, want false (unknown agent)\n%s", out)
		}
		var agentStatus string
		for _, c := range rep.Checks {
			if c.Name == "agent/claud" {
				agentStatus = c.Status
			}
		}
		if agentStatus != "error" {
			t.Errorf("agent/claud status = %q, want error\n%s", agentStatus, out)
		}
	})
}
