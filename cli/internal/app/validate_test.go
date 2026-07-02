package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/josecabralf/taboo"
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
		"repo: " + tabooRepoRoot(t) + "\n" +
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

// TestValidate_TracerDeriveOK is the tracer bullet for validate's derive group:
// a valid project with a derivable source workshop.yaml emits an ok
// source-definition + an ok derive pair. It inspects the checks directly because
// the repo lives under /tmp, so validate's repo-path check fails for an unrelated
// reason and the whole command errors — only the two new checks are asserted.
func TestValidate_TracerDeriveOK(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := t.TempDir() // a REAL dir to hold a real source workshop.yaml
	if err := os.WriteFile(filepath.Join(repo, "workshop.yaml"),
		[]byte("name: x\nbase: ubuntu@24.04\nsdks: []\n"), 0o600); err != nil {
		t.Fatalf("write source workshop.yaml: %v", err)
	}
	body := "workshop: demo\nbase: ubuntu@24.04\nagent: opencode\n" +
		"model: openrouter/qwen/qwen3-coder-plus\nrepo: " + repo + "\n"
	writeTabooProject(t, root, body)
	fake := &fakeCommander{stdoutFn: okHostStdout}
	env := configEnv(t, fake, root, nil)

	checks := validateChecks(context.Background(), env, realStat)
	if got := statusOf(checks, "source-definition"); got != "ok" {
		t.Errorf("source-definition status = %q, want ok\nchecks: %+v", got, checks)
	}
	if got := statusOf(checks, "derive"); got != "ok" {
		t.Errorf("derive status = %q, want ok\nchecks: %+v", got, checks)
	}
}

// TestValidate_DeriveMissingSourceFails asserts that when the configured repo has
// no workshop.yaml, validate's derive group reports source-definition as a hard
// error whose remedy names the missing file, and the command exits non-zero. The
// repo lives under /tmp so validate's repo-path check ALSO fails — the non-zero
// exit alone would not prove the source-definition branch fired, so the check is
// inspected directly to pin THIS behavior.
func TestValidate_DeriveMissingSourceFails(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := t.TempDir() // a REAL but EMPTY dir: no workshop.yaml in it
	body := "workshop: demo\nbase: ubuntu@24.04\nagent: opencode\n" +
		"model: openrouter/qwen/qwen3-coder-plus\nrepo: " + repo + "\n"
	writeTabooProject(t, root, body)
	fake := &fakeCommander{stdoutFn: okHostStdout}
	env := configEnv(t, fake, root, nil)

	out, err := runValidate(t, env)
	if !errors.Is(err, errValidateFailed) {
		t.Fatalf("validate error = %v, want errValidateFailed\n%s", err, out)
	}
	checks := validateChecks(context.Background(), env, realStat)
	c := findCheck(checks, "source-definition")
	if c == nil || c.Status != statusError {
		t.Fatalf("source-definition = %+v, want a hard error\nchecks: %+v", c, checks)
	}
	if !strings.Contains(c.Message, "workshop.yaml") {
		t.Errorf("source-definition message = %q, want it to name the missing workshop.yaml", c.Message)
	}
}

// TestDeriveChecks_EmptyRepoEmitsNoChecksAndProbesNothing asserts that with no
// top-level repo: configured, deriveChecks returns no checks and never probes a
// path. Without the empty-repo guard it would stat a workshop.yaml under the
// resolved repo base, a needless probe (and a confusing report) for a config the
// repo check already condemns. Since repoValidateChecks already flags the unset
// repo, deriveChecks must stay silent (mirroring doctor's workshopProjectChecks).
func TestDeriveChecks_EmptyRepoEmitsNoChecksAndProbesNothing(t *testing.T) {
	t.Parallel()
	var probed []string
	statFile := func(p string) bool {
		probed = append(probed, p)
		return true // always "finds" a file: without the empty-repo guard deriveChecks would probe and emit a check; the asserts prove the guard fires first.
	}

	checks := deriveChecks(taboo.ProjectConfig{Agent: "opencode", Model: "x"}, "/proj", statFile)

	if len(checks) != 0 {
		t.Errorf("deriveChecks(empty repo) = %+v, want no checks", checks)
	}
	if len(probed) != 0 {
		t.Errorf("deriveChecks(empty repo) probed %v, want it to stat nothing", probed)
	}
}

// TestValidate_DeriveRejectsBadSource asserts that when the configured repo's
// workshop.yaml is present but malformed, validate's derive group reports
// source-definition as ok (the file resolves) and derive as a hard error whose
// message is the underlying deriveDefinition error verbatim — proving the check
// now actually dry-runs the derivation rather than trusting mere file presence.
func TestValidate_DeriveRejectsBadSource(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		body    string // the malformed source workshop.yaml
		wantMsg string // substring the derive error message must contain
	}{
		// wantMsg uses multi-word phrases: t.TempDir() embeds the subtest name in the
		// resolved path, so findCheck (not a raw-output scan) plus a spaced phrase keep
		// the assertion from matching the temp path instead of the message.
		{name: "scalar root", body: "123\n", wantMsg: "empty or its root is not a mapping"},
		{name: "multi document", body: "name: a\nbase: ubuntu@24.04\n---\nname: b\n", wantMsg: "single document"},
		{name: "duplicate top-level key", body: "name: first\nname: second\nbase: ubuntu@24.04\n", wantMsg: "duplicate top-level key"},
		{name: "root merge key", body: "defaults: &d\n  base: ubuntu@24.04\nname: x\n<<: *d\n", wantMsg: "merge keys"},
		{name: "non-list sdks", body: "name: x\nsdks:\n  go: {}\n", wantMsg: "must be a list"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			repo := t.TempDir() // a REAL dir holding a real but malformed workshop.yaml
			if err := os.WriteFile(filepath.Join(repo, "workshop.yaml"), []byte(tt.body), 0o600); err != nil {
				t.Fatalf("write source workshop.yaml: %v", err)
			}
			writeTabooProject(t, root, "workshop: demo\nbase: ubuntu@24.04\nagent: opencode\nmodel: openrouter/qwen/q\nrepo: "+repo+"\n")
			fake := &fakeCommander{stdoutFn: okHostStdout}
			env := configEnv(t, fake, root, nil)

			checks := validateChecks(context.Background(), env, realStat)
			d := findCheck(checks, "derive")
			if d == nil || d.Status != statusError {
				t.Fatalf("derive = %+v, want a hard error\nchecks: %+v", d, checks)
			}
			if !strings.Contains(d.Message, tt.wantMsg) {
				t.Errorf("derive message = %q, want it to contain %q", d.Message, tt.wantMsg)
			}
			// The file is present; only the derivation failed, so source-definition stays ok.
			if s := findCheck(checks, "source-definition"); s == nil || s.Status != statusOK {
				t.Errorf("source-definition = %+v, want ok (the file is present)\nchecks: %+v", s, checks)
			}
		})
	}
}

// TestValidate_DeriveResolvesRepoRelativeToConfigNotCWD is the regression test
// for the CWD-sensitivity bug. With a dot repo and the config in a .taboo
// subdir, the source workshop.yaml lives at the project ROOT (the parent of
// .taboo, where the dot repo resolves), not under the process CWD. Validate must
// still find that root workshop.yaml and derive cleanly, exactly as a real run
// resolves it (resolveRepoPath in pkg).
//
// Before the fix deriveChecks statted a bare relative "workshop.yaml" via
// os.Stat, which resolves against the *process* CWD — so it found the source
// only when the test binary happened to run beside a workshop.yaml. We t.Chdir
// into an empty dir to pin that down: pre-fix the bare-relative stat fails there
// (red), post-fix the config-anchored absolute path still resolves (green),
// regardless of where `go test` is invoked.
func TestValidate_DeriveResolvesRepoRelativeToConfigNotCWD(t *testing.T) {
	// Not parallel: t.Chdir pins the process CWD, which parallel tests must not share.
	t.Chdir(t.TempDir())
	root := t.TempDir()
	// A dot repo means the project root is the parent of the .taboo config dir.
	writeTabooProject(t, root,
		"workshop: demo\nbase: ubuntu@24.04\nagent: opencode\n"+
			"model: openrouter/qwen/qwen3-coder-plus\nrepo: .\n")
	// The SOURCE workshop.yaml sits at the project root, NOT under .taboo.
	if err := os.WriteFile(filepath.Join(root, "workshop.yaml"),
		[]byte("name: x\nbase: ubuntu@24.04\nsdks: []\n"), 0o600); err != nil {
		t.Fatalf("write source workshop.yaml: %v", err)
	}
	fake := &fakeCommander{stdoutFn: okHostStdout}
	// env.Getwd returns the CONFIG dir (.taboo) — that drives config discovery, the
	// natural place to run validate from. The pre-fix bug was in the separate source
	// stat (process CWD, pinned empty above), not in discovery.
	configDir := filepath.Join(root, ".taboo")
	env := configEnv(t, fake, configDir, nil)

	checks := validateChecks(context.Background(), env, realStat)
	if got := statusOf(checks, "source-definition"); got != "ok" {
		t.Errorf("source-definition status = %q, want ok (must resolve repo: . against the config dir, not the CWD)\nchecks: %+v", got, checks)
	}
	if got := statusOf(checks, "derive"); got != "ok" {
		t.Errorf("derive status = %q, want ok\nchecks: %+v", got, checks)
	}
	// repo-path is intentionally not asserted clean here: with config-anchored
	// resolution, repo: . resolves to the project root (a t.TempDir() under /tmp),
	// which repoLocationCheck correctly flags as tmpfs. This test pins derive
	// resolution; TestValidate_Repo covers the repo-path/repo-git leaves.
}

// TestResolveRepoBase pins every branch of the config-anchored repo resolver: an
// absolute repo stands alone, a relative non-dot repo joins the config dir, a dot
// repo in a .taboo config resolves to the parent, and a dot repo beside a bare
// taboo.yaml resolves to the config dir itself. It mirrors the runtime
// resolveRepoPath, locking the relative-non-dot branch the integration test
// (which only exercises the dot-repo case) leaves uncovered.
func TestResolveRepoBase(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		configDir string
		repo      string
		want      string
	}{
		{"absolute repo stands alone", "/proj/.taboo", "/elsewhere/repo", "/elsewhere/repo"},
		{"relative repo joins config dir", "/proj/.taboo", "../sibling", "/proj/sibling"},
		{"dot repo in .taboo resolves to parent", "/proj/.taboo", ".", "/proj"},
		{"dot repo beside bare taboo.yaml is the config dir", "/proj", ".", "/proj"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := resolveRepoBase(tc.configDir, tc.repo); got != tc.want {
				t.Errorf("resolveRepoBase(%q, %q) = %q, want %q", tc.configDir, tc.repo, got, tc.want)
			}
		})
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

// TestValidate_VarsChecks covers the per-workflow vars/ OK-level check: a
// workflow whose effective prompt references {{VAR}} placeholders emits an ok
// check naming them sorted — from an inline prompt, a prompt-file's contents,
// or the defaults layer the workflow falls back to — while a placeholder-free
// workflow emits nothing (mirroring modelChecks' clean-config silence) and a
// missing prompt-file emits no vars check (promptFileChecks already hard-fails
// it; don't double-report).
func TestValidate_VarsChecks(t *testing.T) {
	t.Parallel()
	base := "" +
		"workshop: demo\n" +
		"agent: opencode\n" +
		"model: openrouter/qwen/qwen3-coder-plus\n" +
		"repo: /home/me/repo\n"

	t.Run("inline prompt placeholders named sorted", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeTabooProject(t, root, base+
			"workflows:\n  triage:\n    prompt: 'B: {{ISSUE_TITLE}} A: {{ISSUE_BODY}}'\n")
		env := configEnv(t, &fakeCommander{stdoutFn: okHostStdout}, root, nil)

		checks := validateChecks(context.Background(), env, realStat)
		c := findCheck(checks, "vars/triage")
		if c == nil {
			t.Fatalf("no vars/triage check emitted\nchecks: %+v", checks)
		}
		if c.Status != statusOK {
			t.Errorf("vars/triage status = %v, want ok", c.Status)
		}
		if !strings.Contains(c.Message, "ISSUE_BODY, ISSUE_TITLE") {
			t.Errorf("vars/triage message = %q, want the sorted placeholder names", c.Message)
		}
	})

	t.Run("prompt-file contents scanned when the file exists", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeTabooProject(t, root, base+
			"workflows:\n  triage:\n    prompt-file: triage.md\n")
		writePromptFile(t, root, "triage.md", "Title: {{ISSUE_TITLE}}\n")
		env := configEnv(t, &fakeCommander{stdoutFn: okHostStdout}, root, nil)

		checks := validateChecks(context.Background(), env, realStat)
		c := findCheck(checks, "vars/triage")
		if c == nil {
			t.Fatalf("no vars/triage check emitted for a prompt-file-backed workflow\nchecks: %+v", checks)
		}
		if !strings.Contains(c.Message, "ISSUE_TITLE") {
			t.Errorf("vars/triage message = %q, want it to name ISSUE_TITLE", c.Message)
		}
	})

	t.Run("defaults prompt backs a bare workflow", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeTabooProject(t, root, base+
			"defaults:\n  prompt: 'do {{TASK}}'\n"+
			"workflows:\n  fix: {}\n")
		env := configEnv(t, &fakeCommander{stdoutFn: okHostStdout}, root, nil)

		checks := validateChecks(context.Background(), env, realStat)
		c := findCheck(checks, "vars/fix")
		if c == nil {
			t.Fatalf("no vars/fix check for a defaults-backed workflow\nchecks: %+v", checks)
		}
		if !strings.Contains(c.Message, "TASK") {
			t.Errorf("vars/fix message = %q, want it to name TASK", c.Message)
		}
	})

	t.Run("placeholder-free workflow emits nothing", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeTabooProject(t, root, base+
			"workflows:\n  fix:\n    prompt: no vars at all\n")
		env := configEnv(t, &fakeCommander{stdoutFn: okHostStdout}, root, nil)

		checks := validateChecks(context.Background(), env, realStat)
		if c := findCheck(checks, "vars/fix"); c != nil {
			t.Errorf("placeholder-free workflow emitted %+v, want no vars check", *c)
		}
	})

	t.Run("missing prompt-file emits no vars check", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeTabooProject(t, root, base+
			"workflows:\n  triage:\n    prompt-file: gone.md\n")
		env := configEnv(t, &fakeCommander{stdoutFn: okHostStdout}, root, nil)

		checks := validateChecks(context.Background(), env, realStat)
		if c := findCheck(checks, "vars/triage"); c != nil {
			t.Errorf("missing prompt-file emitted %+v, want no vars check (promptFileChecks owns the failure)", *c)
		}
		if c := findCheck(checks, "prompt-file/gone.md"); c == nil || c.Status != statusError {
			t.Errorf("prompt-file/gone.md check = %+v, want the existing hard failure", c)
		}
	})

	t.Run("run preflight is unaffected", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeTabooProject(t, root, base+
			"workflows:\n  triage:\n    prompt: 'T: {{ISSUE_TITLE}}'\n")
		env := configEnv(t, &fakeCommander{stdoutFn: okHostStdout}, root, nil)

		checks := runConfigChecks(context.Background(), env, realStat)
		if c := findCheck(checks, "vars/triage"); c != nil {
			t.Errorf("run's preflight emitted %+v; the vars group is validate-only", *c)
		}
	})
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
		writeTabooProject(t, root, "workshop: demo\nagent: claude-code\nmodel: gpt-5\nrepo: "+tabooRepoRoot(t)+"\n")
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

	t.Run("any model for github-copilot is fine", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeTabooProject(t, root, "workshop: demo\nagent: github-copilot\nmodel: some-exotic-model\nrepo: "+tabooRepoRoot(t)+"\n")
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
		"repo: " + tabooRepoRoot(t) + "\n" +
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
		want []taboo.AgentName
	}{
		{name: "top-level only", cfg: taboo.ProjectConfig{Agent: "opencode"}, want: []taboo.AgentName{"opencode"}},
		{
			name: "workflow override deduped and sorted",
			cfg: taboo.ProjectConfig{
				Agent: "opencode",
				Workflows: map[string]taboo.Workflow{
					"a": {Agent: "claude-code"},
					"b": {}, // inherits opencode
				},
			},
			want: []taboo.AgentName{"claude-code", "opencode"},
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
		body := "workshop: demo\nagent: opencode\nmodel: openrouter/qwen/q\nrepo: " + tabooRepoRoot(t) + "\n" +
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
		writeTabooProject(t, root, "workshop: demo\nagent: opencode\nmodel: openrouter/qwen/q\nrepo: "+tabooRepoRoot(t)+"\n")
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
