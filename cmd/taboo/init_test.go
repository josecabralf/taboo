package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	taboo "github.com/josecabralf/taboo/pkg/taboo"
)

// gitRepo makes a temp dir and marks it a git work tree fixture by creating a
// .git dir, returning the repo path. The init command never shells out to git,
// so a bare directory is enough, but this keeps fixtures honest.
func gitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o750); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	return root
}

// initEnv builds an Env for init tests: a fake Commander (so we can assert zero
// calls), buffered streams, non-interactive stdin, and Getwd pointed at repo.
func initEnv(t *testing.T, fake *fakeCommander, repo string) Env {
	t.Helper()
	return Env{
		Cmd:       fake,
		Stdin:     strings.NewReader(""),
		Stdout:    &bytes.Buffer{},
		Stderr:    &bytes.Buffer{},
		LookupEnv: func(string) (string, bool) { return "", false },
		Getwd:     func() (string, error) { return repo, nil },
	}
}

// runInit executes a freshly built init command with env and args, returning
// captured stdout and the execute error.
func runInit(t *testing.T, env Env, args ...string) (string, error) {
	t.Helper()
	cmd := newInitCmd(env)
	cmd.SetArgs(args)
	err := cmd.Execute()
	out, _ := env.Stdout.(*bytes.Buffer)
	if out == nil {
		t.Fatal("runInit: env.Stdout must be a *bytes.Buffer")
	}
	return out.String(), err
}

// TestInit_TracerBullet is the tracer bullet: a non-interactive run writes the
// three scaffold files under <repo>/.taboo.
func TestInit_TracerBullet(t *testing.T) {
	t.Parallel()
	repo := gitRepo(t)
	fake := &fakeCommander{}
	env := initEnv(t, fake, repo)
	_, err := runInit(t, env, "--agent", "opencode", "--model", "some/model", "--repo", repo)
	if err != nil {
		t.Fatalf("init error = %v, want nil", err)
	}
	for _, name := range []string{"taboo.yaml", ".gitignore", ".env.example"} {
		if _, err := os.Stat(filepath.Join(repo, ".taboo", name)); err != nil {
			t.Errorf("expected %s written: %v", name, err)
		}
	}
}

// TestInit_SeedsWorkflowsByDefault asserts init is batteries-included: a default
// run seeds the fix/refactor workflows and their prompt files, while
// --workflows none opts out, leaving no workflows and no prompts/ directory.
func TestInit_SeedsWorkflowsByDefault(t *testing.T) {
	t.Parallel()

	// Default case: workflows and prompt files are seeded.
	root := gitRepo(t)
	fake := &fakeCommander{}
	env := initEnv(t, fake, root)
	if _, err := runInit(t, env, "--agent", "opencode", "--model", "m", "--repo", root); err != nil {
		t.Fatalf("init error = %v, want nil", err)
	}
	cfg, err := taboo.LoadConfig(filepath.Join(root, ".taboo", "taboo.yaml"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.DefaultWorkflow != "fix" {
		t.Errorf("cfg.DefaultWorkflow = %q, want %q", cfg.DefaultWorkflow, "fix")
	}
	for _, key := range []string{"fix", "refactor"} {
		if _, ok := cfg.Workflows[key]; !ok {
			t.Errorf("cfg.Workflows missing %q: %v", key, cfg.Workflows)
		}
	}
	for _, name := range []string{"fix.md", "refactor.md"} {
		if _, statErr := os.Stat(filepath.Join(root, ".taboo", "prompts", name)); statErr != nil {
			t.Errorf("expected prompts/%s written: %v", name, statErr)
		}
	}
	profile, _ := taboo.NewProfile("opencode", "m")
	envExample, err := os.ReadFile(filepath.Join(root, ".taboo", ".env.example"))
	if err != nil {
		t.Fatalf("read .env.example: %v", err)
	}
	for _, k := range profile.CredentialEnvKeys() {
		if !strings.Contains(string(envExample), k+"=") {
			t.Errorf(".env.example missing %q\nfull:\n%s", k+"=", envExample)
		}
	}
	if len(fake.calls) != 0 {
		t.Errorf("init made %d Commander calls, want 0: %v", len(fake.calls), invocations(fake))
	}

	// Opt-out case: --workflows none leaves no workflows and no prompts/ dir.
	root2 := gitRepo(t)
	env2 := initEnv(t, &fakeCommander{}, root2)
	if _, err := runInit(t, env2,
		"--agent", "opencode", "--model", "m", "--repo", root2, "--workflows", "none"); err != nil {
		t.Fatalf("init --workflows none error = %v, want nil", err)
	}
	cfg2, err := taboo.LoadConfig(filepath.Join(root2, ".taboo", "taboo.yaml"))
	if err != nil {
		t.Fatalf("LoadConfig (opt-out): %v", err)
	}
	if len(cfg2.Workflows) != 0 {
		t.Errorf("cfg.Workflows = %v, want empty with --workflows none", cfg2.Workflows)
	}
	if cfg2.DefaultWorkflow != "" {
		t.Errorf("cfg.DefaultWorkflow = %q, want empty with --workflows none", cfg2.DefaultWorkflow)
	}
	if _, statErr := os.Stat(filepath.Join(root2, ".taboo", "prompts")); !os.IsNotExist(statErr) {
		t.Errorf("prompts/ should not exist with --workflows none, stat err = %v", statErr)
	}
}

// TestInit_TemplateScaffoldsGo asserts the --template flag gates the optional Go
// scaffold: default writes no Go, single writes a reproducible main.go + go.mod,
// and a bogus value is rejected before anything is written.
func TestInit_TemplateScaffoldsGo(t *testing.T) {
	t.Parallel()

	// Default: no --template scaffolds neither Go file.
	root := gitRepo(t)
	env := initEnv(t, &fakeCommander{}, root)
	if _, err := runInit(t, env, "--agent", "opencode", "--model", "m", "--repo", root); err != nil {
		t.Fatalf("init error = %v, want nil", err)
	}
	for _, name := range []string{"main.go", "go.mod"} {
		if _, statErr := os.Stat(filepath.Join(root, ".taboo", name)); !os.IsNotExist(statErr) {
			t.Errorf("default run should not write %s, stat err = %v", name, statErr)
		}
	}

	// Single: --template single scaffolds main.go and a reproducible go.mod.
	root2 := gitRepo(t)
	env2 := initEnv(t, &fakeCommander{}, root2)
	if _, err := runInit(t, env2,
		"--agent", "opencode", "--model", "m", "--repo", root2, "--template", "single"); err != nil {
		t.Fatalf("init --template single error = %v, want nil", err)
	}
	for _, name := range []string{"main.go", "go.mod"} {
		if _, statErr := os.Stat(filepath.Join(root2, ".taboo", name)); statErr != nil {
			t.Errorf("--template single should write %s: %v", name, statErr)
		}
	}
	goMod, err := os.ReadFile(filepath.Join(root2, ".taboo", "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	if !strings.Contains(string(goMod), "require github.com/josecabralf/taboo ") {
		t.Errorf("go.mod missing pinned require\nfull:\n%s", goMod)
	}
	if strings.Contains(string(goMod), "replace") {
		t.Errorf("go.mod should not contain replace\nfull:\n%s", goMod)
	}

	// Bogus: an unknown --template fails fast before writing .taboo.
	root3 := gitRepo(t)
	env3 := initEnv(t, &fakeCommander{}, root3)
	_, err = runInit(t, env3,
		"--agent", "opencode", "--model", "m", "--repo", root3, "--template", "bogus")
	if err == nil || !strings.Contains(err.Error(), "template") {
		t.Fatalf("init --template bogus error = %v, want one mentioning 'template'", err)
	}
	if _, statErr := os.Stat(filepath.Join(root3, ".taboo")); !os.IsNotExist(statErr) {
		t.Errorf(".taboo should not exist after a rejected --template, stat err = %v", statErr)
	}
}

// TestInit_DryRunWritesNothing asserts --dry-run lists each target file path and
// writes no files.
func TestInit_DryRunWritesNothing(t *testing.T) {
	t.Parallel()
	repo := gitRepo(t)
	env := initEnv(t, &fakeCommander{}, repo)
	out, err := runInit(t, env,
		"--agent", "opencode", "--model", "m", "--repo", repo, "--dry-run")
	if err != nil {
		t.Fatalf("init --dry-run error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(repo, ".taboo")); !os.IsNotExist(statErr) {
		t.Errorf(".taboo should not exist after --dry-run, stat err = %v", statErr)
	}
	for _, name := range []string{"taboo.yaml", ".gitignore", ".env.example"} {
		if !strings.Contains(out, name) {
			t.Errorf("--dry-run output missing %q\nfull:\n%s", name, out)
		}
	}
}

// TestInit_RefusesExistingWithoutForce asserts a pre-existing .taboo blocks the
// run unless --force is given.
func TestInit_RefusesExistingWithoutForce(t *testing.T) {
	t.Parallel()
	repo := gitRepo(t)
	projectDir := filepath.Join(repo, ".taboo")
	if err := os.MkdirAll(projectDir, 0o750); err != nil {
		t.Fatalf("pre-create .taboo: %v", err)
	}
	marker := filepath.Join(projectDir, "marker")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	env := initEnv(t, &fakeCommander{}, repo)
	_, err := runInit(t, env, "--agent", "opencode", "--model", "m", "--repo", repo)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("init error = %v, want one mentioning --force", err)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Errorf("marker should be untouched, stat err = %v", statErr)
	}

	env2 := initEnv(t, &fakeCommander{}, repo)
	if _, err := runInit(t, env2,
		"--agent", "opencode", "--model", "m", "--repo", repo, "--force"); err != nil {
		t.Fatalf("init --force error = %v, want nil", err)
	}
	if _, statErr := os.Stat(filepath.Join(projectDir, "taboo.yaml")); statErr != nil {
		t.Errorf("--force should write taboo.yaml: %v", statErr)
	}
}

// TestInit_RefusesNonDirTaboo asserts a regular file named .taboo blocks the run
// with a clear "not a directory" error rather than an opaque MkdirAll failure.
func TestInit_RefusesNonDirTaboo(t *testing.T) {
	t.Parallel()
	repo := gitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".taboo"), []byte("x"), 0o600); err != nil {
		t.Fatalf("pre-create .taboo file: %v", err)
	}
	env := initEnv(t, &fakeCommander{}, repo)
	_, err := runInit(t, env, "--agent", "opencode", "--model", "m", "--repo", repo)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("init error = %v, want one mentioning 'not a directory'", err)
	}
}

// TestInit_RelativeRepoUsesInjectedGetwd asserts a relative --repo resolves
// against the injected env.Getwd, not the process working directory.
func TestInit_RelativeRepoUsesInjectedGetwd(t *testing.T) {
	t.Parallel()
	root := gitRepo(t)
	env := initEnv(t, &fakeCommander{}, root) // initEnv wires Getwd -> root
	if _, err := runInit(t, env, "--agent", "opencode", "--model", "m", "--repo", "."); err != nil {
		t.Fatalf("init error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".taboo", "taboo.yaml")); err != nil {
		t.Errorf(".taboo should resolve under the injected wd %s: %v", root, err)
	}
	cfg, err := taboo.LoadConfig(filepath.Join(root, ".taboo", "taboo.yaml"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Repo != root {
		t.Errorf("cfg.Repo = %q, want injected wd %q", cfg.Repo, root)
	}
}

// TestInit_MissingRequired asserts a non-interactive run missing agent or model
// fails fast and names the missing flag.
func TestInit_MissingRequired(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing agent", args: []string{"--model", "m"}, want: "--agent"},
		{name: "missing model", args: []string{"--agent", "opencode"}, want: "--model"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := gitRepo(t)
			env := initEnv(t, &fakeCommander{}, repo)
			args := append([]string{"--repo", repo}, tt.args...)
			_, err := runInit(t, env, args...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("init error = %v, want one mentioning %q", err, tt.want)
			}
		})
	}
}

// TestInit_UnknownAgent asserts an unknown agent errors with the bad name and
// the valid agent names.
func TestInit_UnknownAgent(t *testing.T) {
	t.Parallel()
	repo := gitRepo(t)
	env := initEnv(t, &fakeCommander{}, repo)
	_, err := runInit(t, env, "--agent", "bogus", "--model", "m", "--repo", repo)
	if err == nil {
		t.Fatal("init error = nil, want unknown-agent error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "bogus") {
		t.Errorf("error %q does not mention bogus", msg)
	}
	for _, name := range taboo.AgentNames() {
		if !strings.Contains(msg, name) {
			t.Errorf("error %q does not list valid agent %q", msg, name)
		}
	}
}

// TestInit_MakesZeroCommanderCalls asserts init never launches a workshop: a
// successful run records no Commander invocations.
func TestInit_MakesZeroCommanderCalls(t *testing.T) {
	t.Parallel()
	repo := gitRepo(t)
	fake := &fakeCommander{}
	env := initEnv(t, fake, repo)
	if _, err := runInit(t, env,
		"--agent", "opencode", "--model", "m", "--repo", repo); err != nil {
		t.Fatalf("init error = %v", err)
	}
	if len(fake.calls) != 0 {
		t.Errorf("init made %d Commander calls, want 0: %v", len(fake.calls), invocations(fake))
	}
}

// TestInit_NextStepsAndDoctorOffer asserts a successful run prints next steps and
// suggests running doctor.
func TestInit_NextStepsAndDoctorOffer(t *testing.T) {
	t.Parallel()
	repo := gitRepo(t)
	env := initEnv(t, &fakeCommander{}, repo)
	out, err := runInit(t, env, "--agent", "opencode", "--model", "m", "--repo", repo)
	if err != nil {
		t.Fatalf("init error = %v", err)
	}
	if !strings.Contains(out, "doctor") {
		t.Errorf("output missing doctor suggestion\nfull:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "next steps") {
		t.Errorf("output missing next steps\nfull:\n%s", out)
	}
}

// TestInit_PrintsErrorToStderr asserts a failed run surfaces its message on
// stderr (not just as a returned error): the root silences cobra errors and main
// exits without printing, so a silent failure would hide the named flag from the
// user.
func TestInit_PrintsErrorToStderr(t *testing.T) {
	t.Parallel()
	repo := gitRepo(t)
	env := initEnv(t, &fakeCommander{}, repo)
	cmd := newInitCmd(env)
	cmd.SetArgs([]string{"--model", "x", "--repo", repo}) // missing --agent
	if err := cmd.Execute(); err == nil {
		t.Fatal("init error = nil, want missing-flag error")
	}
	stderr, ok := env.Stderr.(*bytes.Buffer)
	if !ok {
		t.Fatal("env.Stderr must be a *bytes.Buffer")
	}
	if !strings.Contains(stderr.String(), "--agent") {
		t.Errorf("stderr should name the missing flag, got: %q", stderr.String())
	}
}

// TestIsInteractive asserts the non-TTY cases that drive the flag-or-fail path:
// an in-memory reader, an *os.File on /dev/null, and a pipe are all
// non-interactive. /dev/null is the load-bearing case — it is a character device
// yet not a terminal, so a bare ModeCharDevice check would wrongly report it
// interactive.
func TestIsInteractive(t *testing.T) {
	t.Parallel()
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	t.Cleanup(func() { _ = devNull.Close() })
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	t.Cleanup(func() { _ = pr.Close(); _ = pw.Close() })

	tests := []struct {
		name string
		in   io.Reader
		want bool
	}{
		{name: "in-memory reader", in: strings.NewReader(""), want: false},
		{name: "dev null", in: devNull, want: false},
		{name: "pipe", in: pr, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isInteractive(Env{Stdin: tt.in}); got != tt.want {
				t.Errorf("isInteractive(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}
