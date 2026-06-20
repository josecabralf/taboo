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

	taboo "github.com/josecabralf/taboo/pkg"
)

// writeTabooProject lays out <root>/.taboo/taboo.yaml with body and returns the
// project root (the dir an agent would run `taboo` from). It is the real
// filesystem fixture config discovery ascends.
func writeTabooProject(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, ".taboo")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir .taboo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "taboo.yaml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write taboo.yaml: %v", err)
	}
}

// configEnv builds an Env whose Getwd returns wd, env vars come from envMap, and
// the Commander is fake. It is the harness for config-aware tests.
func configEnv(t *testing.T, fake *fakeCommander, wd string, envMap map[string]string) Env {
	t.Helper()
	return Env{
		Cmd:    fake,
		Stdin:  strings.NewReader(""),
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		LookupEnv: func(k string) (string, bool) {
			v, ok := envMap[k]
			return v, ok
		},
		Getwd: func() (string, error) { return wd, nil },
	}
}

// TestFindConfig_AscendsTree verifies discovery finds .taboo/taboo.yaml from a
// nested working directory and reports not-found at a bare temp dir.
func TestFindConfig_AscendsTree(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTabooProject(t, root, "workshop: demo\n")
	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o750); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	stat := func(p string) bool {
		info, err := os.Stat(p)
		return err == nil && !info.IsDir()
	}

	path, found := findConfig(nested, stat)
	if !found {
		t.Fatalf("findConfig(%q) found = false, want true", nested)
	}
	if want := filepath.Join(root, ".taboo", "taboo.yaml"); path != want {
		t.Errorf("findConfig path = %q, want %q", path, want)
	}

	if _, found := findConfig(t.TempDir(), stat); found {
		t.Errorf("findConfig(empty dir) found = true, want false")
	}
}

// TestConfigChecks_NoConfigSkips asserts that outside a taboo project the
// config-aware checks contribute nothing (and do not error).
func TestConfigChecks_NoConfigSkips(t *testing.T) {
	t.Parallel()
	fake := &fakeCommander{stdoutFn: okHostStdout}
	env := configEnv(t, fake, t.TempDir(), nil)
	checks := configChecks(context.Background(), env, statFileExists, taboo.LoadConfig)
	if len(checks) != 0 {
		t.Fatalf("configChecks = %v, want empty (no taboo.yaml)", checks)
	}
}

// realStat is the production existence probe used by config-aware tests that go
// through the real filesystem.
func realStat(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// tabooRepoRoot returns taboo's own repo root: the nearest ancestor of the
// test's working directory that holds go.mod. That directory is itself a
// workshop project, so it carries a real, derivable workshop.yaml on persistent
// storage — the one fixture that lets validate's source-definition and derive
// checks pass end-to-end (a t.TempDir() repo lives under /tmp, which repo-path
// rejects, so it can never be a "fully clean" repo). A missing root or
// workshop.yaml is a hard t.Fatal, never a silent skip. Mirrors
// findRepoWorkshopYAML in pkg/taboo.
func tabooRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "workshop.yaml")); err != nil {
				t.Fatalf("repo root %q has no workshop.yaml: %v", dir, err)
			}
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate taboo repo root (no go.mod above the test working dir)")
		}
		dir = parent
	}
}

// TestConfigChecks_Credentials covers the per-agent credential WARN: with no
// credential env var set the referenced agent warns; with one set it does not.
func TestConfigChecks_Credentials(t *testing.T) {
	t.Parallel()
	body := "" +
		"workshop: demo\n" +
		"base: ubuntu@24.04\n" +
		"agent: opencode\n" +
		"model: anthropic/claude\n"

	t.Run("missing creds warns", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeTabooProject(t, root, body)
		fake := &fakeCommander{stdoutFn: okHostStdout}
		env := configEnv(t, fake, root, nil)
		checks := configChecks(context.Background(), env, realStat, taboo.LoadConfig)
		if got := statusOf(checks, "credentials/opencode"); got != "warn" {
			t.Errorf("credentials/opencode = %q, want warn\nchecks: %+v", got, checks)
		}
	})

	t.Run("creds present no warn", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeTabooProject(t, root, body)
		fake := &fakeCommander{stdoutFn: okHostStdout}
		env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-xxx"})
		checks := configChecks(context.Background(), env, realStat, taboo.LoadConfig)
		if got := statusOf(checks, "credentials/opencode"); got != "" {
			t.Errorf("credentials/opencode = %q, want absent\nchecks: %+v", got, checks)
		}
	})
}

// TestConfigChecks_MultiKeyCredentials exercises anyEnvSet's "any one of several
// keys set passes" semantics for the multi-key agents: claude-code accepts
// either of two keys and copilot any of three, so setting a single one clears
// the warning while an empty environment warns; the single-key opencode agent
// cannot distinguish this branch, hence a dedicated case here.
func TestConfigChecks_MultiKeyCredentials(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		agent string
		check string
		env   map[string]string
		want  string // "warn" when the agent should warn, "" when it should not.
	}{
		{name: "claude-code first key only", agent: "claude-code", check: "credentials/claude-code",
			env: map[string]string{"ANTHROPIC_API_KEY": "sk-ant"}, want: ""},
		{name: "claude-code second key only", agent: "claude-code", check: "credentials/claude-code",
			env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "tok"}, want: ""},
		{name: "claude-code no keys warns", agent: "claude-code", check: "credentials/claude-code",
			env: nil, want: "warn"},
		{name: "copilot third key only", agent: "copilot", check: "credentials/copilot",
			env: map[string]string{"GITHUB_TOKEN": "ghp"}, want: ""},
		{name: "copilot no keys warns", agent: "copilot", check: "credentials/copilot",
			env: nil, want: "warn"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			body := "" +
				"workshop: demo\n" +
				"base: ubuntu@24.04\n" +
				"agent: " + tt.agent + "\n" +
				"model: some-model\n"
			writeTabooProject(t, root, body)
			fake := &fakeCommander{stdoutFn: okHostStdout}
			env := configEnv(t, fake, root, tt.env)
			checks := configChecks(context.Background(), env, realStat, taboo.LoadConfig)
			if got := statusOf(checks, tt.check); got != tt.want {
				t.Errorf("%s = %q, want %q\nchecks: %+v", tt.check, got, tt.want, checks)
			}
		})
	}
}

// TestConfigChecks_Repo covers the configured-repo checks: a tmpfs path errors,
// a non-git path errors, and a real git work tree passes both.
func TestConfigChecks_Repo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		repo     string
		gitFails bool
		wantPath string // status of repo-path check
		wantGit  string // status of repo-git check
	}{
		{name: "repo under /tmp errors", repo: "/tmp/my-repo", gitFails: false,
			wantPath: "error", wantGit: "ok"},
		{name: "repo under /run errors", repo: "/run/user/1000/repo", gitFails: false,
			wantPath: "error", wantGit: "ok"},
		{name: "repo not a git repo errors", repo: "/home/me/not-git", gitFails: true,
			wantPath: "ok", wantGit: "error"},
		{name: "good repo passes both", repo: "/home/me/repo", gitFails: false,
			wantPath: "ok", wantGit: "ok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			body := "" +
				"workshop: demo\n" +
				"base: ubuntu@24.04\n" +
				"agent: opencode\n" +
				"model: anthropic/claude\n" +
				"repo: " + tt.repo + "\n"
			writeTabooProject(t, root, body)
			fake := &fakeCommander{stdoutFn: okHostStdout}
			if tt.gitFails {
				fake.errFn = failOnArgs("git", "-C")
			}
			env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-xxx"})
			checks := configChecks(context.Background(), env, realStat, taboo.LoadConfig)
			if got := statusOf(checks, "repo-path"); got != tt.wantPath {
				t.Errorf("repo-path = %q, want %q\nchecks: %+v", got, tt.wantPath, checks)
			}
			if got := statusOf(checks, "repo-git"); got != tt.wantGit {
				t.Errorf("repo-git = %q, want %q\nchecks: %+v", got, tt.wantGit, checks)
			}
		})
	}
}

// TestConfigChecks_InvalidConfigErrors asserts that an unparseable taboo.yaml
// yields a single config error and skips the remaining config-aware checks.
func TestConfigChecks_InvalidConfigErrors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTabooProject(t, root, "workshop: demo\n  bad-indent: : :\n")
	fake := &fakeCommander{stdoutFn: okHostStdout}
	env := configEnv(t, fake, root, nil)
	checks := configChecks(context.Background(), env, realStat, taboo.LoadConfig)
	if len(checks) != 1 {
		t.Fatalf("configChecks = %d checks, want 1 (config error only): %+v", len(checks), checks)
	}
	if checks[0].Name != "config" || checks[0].Status != statusError {
		t.Errorf("checks[0] = %+v, want config/error", checks[0])
	}
}

// TestRepoGitCheck_ThreadsRepoPath asserts the configured repo path is the value
// threaded into `git -C <repo> rev-parse --is-inside-work-tree` — not a hardcoded
// or wrong path — by inspecting the invocation recorded at the Commander seam.
func TestRepoGitCheck_ThreadsRepoPath(t *testing.T) {
	t.Parallel()
	const repo = "/home/me/somewhere/myrepo"
	root := t.TempDir()
	body := "" +
		"workshop: demo\n" +
		"base: ubuntu@24.04\n" +
		"agent: opencode\n" +
		"model: anthropic/claude\n" +
		"repo: " + repo + "\n"
	writeTabooProject(t, root, body)
	fake := &fakeCommander{stdoutFn: okHostStdout}
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-xxx"})

	configChecks(context.Background(), env, realStat, taboo.LoadConfig)

	want := []string{"git", "-C", repo, "rev-parse", "--is-inside-work-tree"}
	for _, inv := range invocations(fake) {
		if slices.Equal(inv, want) {
			return
		}
	}
	t.Errorf("no %v invocation recorded; calls: %v", want, invocations(fake))
}

// TestConfigChecks_WorkshopProjectOK asserts that in a taboo project whose
// configured repo holds a workshop.yaml, doctor reports workshop-project ok and
// names the resolved source path (presence only, no derive). The source-definition
// check is validate-only, so doctor must not emit it.
func TestConfigChecks_WorkshopProjectOK(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := t.TempDir()
	// doctor is presence-only, so ANY present file satisfies it; content is irrelevant:
	if err := os.WriteFile(filepath.Join(repo, "workshop.yaml"), []byte("name: x\n"), 0o600); err != nil {
		t.Fatalf("write workshop.yaml: %v", err)
	}
	body := "workshop: demo\nbase: ubuntu@24.04\nagent: opencode\nmodel: openrouter/qwen/q\nrepo: " + repo + "\n"
	writeTabooProject(t, root, body)
	fake := &fakeCommander{stdoutFn: okHostStdout}
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-xxx"})
	checks := configChecks(context.Background(), env, realStat, taboo.LoadConfig)
	wp := findCheck(checks, "workshop-project")
	if wp == nil || wp.Status != statusOK || !strings.Contains(wp.Message, "workshop.yaml") {
		t.Errorf("workshop-project = %+v, want ok naming workshop.yaml\nchecks: %+v", wp, checks)
	}
	if sd := findCheck(checks, "source-definition"); sd != nil {
		t.Errorf("doctor emitted source-definition = %+v, want it to be validate-only\nchecks: %+v", sd, checks)
	}
}

// TestConfigChecks_NotAWorkshopProject asserts that when the configured repo has
// no workshop.yaml, doctor reports workshop-project as a hard error and the doctor
// command exits non-zero.
func TestConfigChecks_NotAWorkshopProject(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := t.TempDir() // a real but empty dir: no workshop.yaml in it.
	body := "workshop: demo\nbase: ubuntu@24.04\nagent: opencode\nmodel: openrouter/qwen/q\nrepo: " + repo + "\n"
	writeTabooProject(t, root, body)
	fake := &fakeCommander{stdoutFn: okHostStdout}
	env := configEnv(t, fake, root, map[string]string{"OPENROUTER_API_KEY": "sk-xxx"})

	// Check-level: inspect configChecks directly so this branch is pinned even
	// though the /tmp repo path also fails.
	checks := configChecks(context.Background(), env, realStat, taboo.LoadConfig)
	wp := findCheck(checks, "workshop-project")
	if wp == nil || wp.Status != statusError || !strings.Contains(wp.Message, "workshop.yaml") {
		t.Errorf("workshop-project = %+v, want error mentioning workshop.yaml\nchecks: %+v", wp, checks)
	}
	if sd := findCheck(checks, "source-definition"); sd != nil {
		t.Errorf("doctor emitted source-definition = %+v, want it to be validate-only\nchecks: %+v", sd, checks)
	}

	// Exit wiring: the doctor command exits non-zero.
	out, err := runDoctor(t, env)
	if !errors.Is(err, errChecksFailed) {
		t.Fatalf("doctor error = %v, want errChecksFailed\n%s", err, out)
	}
}

// statusOf returns the status token of the named check, or "" if absent.
func statusOf(checks []check, name string) string {
	for _, c := range checks {
		if c.Name == name {
			return c.Status.token()
		}
	}
	return ""
}
