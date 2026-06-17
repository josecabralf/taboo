package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	taboo "github.com/josecabralf/taboo/pkg/taboo"
)

// scaffoldInputs are the resolved values init renders the .taboo/ scaffold from:
// the config scalars plus the resolved agent profile whose credential keys seed
// .env.example.
type scaffoldInputs struct {
	// Workshop is the workshop name written to taboo.yaml.
	Workshop string
	// Base is the workshop base image written to taboo.yaml.
	Base string
	// Repo is the absolute host repository path written to taboo.yaml.
	Repo string
	// Agent is the chosen agent name written to taboo.yaml and named in .env.example.
	Agent string
	// Model is the chosen model written to taboo.yaml.
	Model string
	// Profile is the resolved agent profile whose CredentialEnvKeys seed .env.example.
	Profile taboo.AgentProfile
	// SeedWorkflows seeds the example fix/refactor workflows and prompt files when true.
	SeedWorkflows bool
	// Template selects the optional Go scaffold: "none" (default), "single", or "fanout".
	Template string
}

// libraryVersion is the pkg/taboo version the scaffolded go.mod pins to. It is
// the taboo release this CLI was built from; release tooling overwrites it via
// -ldflags "-X main.libraryVersion=vX.Y.Z". The v0.0.0 default marks an
// unreleased dev build (no tag yet) and is kept a valid semver so the require
// directive is well-formed.
var libraryVersion = "v0.0.0"

// scaffoldGoVersion is the go directive written into the scaffolded go.mod.
const scaffoldGoVersion = "1.26"

// scaffoldFile is one file in the scaffold plan: Path is relative to the .taboo/
// project dir, Contents is the rendered bytes.
type scaffoldFile struct {
	// Path is the file's location relative to the project dir, e.g. "taboo.yaml".
	Path string
	// Contents is the rendered file body.
	Contents []byte
}

// plan renders the scaffold files in stable order: always the three base files
// (taboo.yaml, then .gitignore, then .env.example), then the seeded prompt files
// when SeedWorkflows is set, then the Go scaffold (main.go, go.mod) when a
// Template is chosen. It returns the first render error (only taboo.yaml, which
// marshals YAML, can fail).
func (in scaffoldInputs) plan() ([]scaffoldFile, error) {
	tabooYAML, err := renderTabooYAML(in)
	if err != nil {
		return nil, err
	}
	files := []scaffoldFile{
		{Path: "taboo.yaml", Contents: tabooYAML},
		{Path: ".gitignore", Contents: renderGitignore()},
		{Path: ".env.example", Contents: renderEnvExample(in)},
	}
	if in.SeedWorkflows {
		files = append(files,
			scaffoldFile{Path: "prompts/fix.md", Contents: []byte(fixPromptBody)},
			scaffoldFile{Path: "prompts/refactor.md", Contents: []byte(refactorPromptBody)},
		)
	}
	if in.Template != "" && in.Template != "none" {
		files = append(files,
			scaffoldFile{Path: "main.go", Contents: renderMainGo(in)},
			scaffoldFile{Path: "go.mod", Contents: renderGoMod(in)},
		)
	}
	return files, nil
}

// renderMainGo returns the Go skeleton scaffolded for --template: the fanout
// variant fans several runs across a taboo.NewPool and extracts a typed
// taboo.JSONResult from each; otherwise the single-run program that loads
// taboo.yaml and runs the configured agent once through the taboo library.
func renderMainGo(in scaffoldInputs) []byte {
	if in.Template == "fanout" {
		return []byte(`// Command main fans several agent runs out across a pool of workshops and
// extracts a typed result from each. ` + "`taboo init --template fanout`" + ` scaffolds it
// as a richer skeleton (see github.com/josecabralf/taboo/pkg/taboo). It reads the
// same taboo.yaml the CLI uses; grow the prompts slice and the Finding schema
// into your real task.
package main

import (
	"context"
	"fmt"
	"os"

	taboo "github.com/josecabralf/taboo/pkg/taboo"
)

// Finding is the typed result each run emits inside a <result>...</result>
// block; taboo.JSONResult decodes it. Make its fields your real schema.
type Finding struct {
	` + "Summary string " + "`" + `json:"summary"` + "`" + `
}

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := taboo.LoadConfig("taboo.yaml")
	if err != nil {
		return err
	}
	if cfg.Profile == nil {
		return fmt.Errorf("taboo.yaml needs an agent: set one and re-run")
	}

	// Edit these into your real tasks; each runs on its own branch and worktree.
	prompts := []string{
		"Summarize this repository's build setup. Emit the summary as JSON inside a <result>...</result> block.",
		"Summarize this repository's test setup. Emit the summary as JSON inside a <result>...</result> block.",
	}

	pool := taboo.NewPool(taboo.Config{
		Workshop:   taboo.WorkshopName(cfg.Workshop, cfg.Profile.Name()),
		Base:       cfg.Base,
		Agent:      cfg.Profile,
		RepoPath:   cfg.Repo,
		ProjectDir: ".",
	}, 2, taboo.NewExecCommander())

	reqs := make([]taboo.RunRequest, len(prompts))
	for i, p := range prompts {
		reqs[i] = taboo.RunRequest{
			Branch: fmt.Sprintf("taboo/fanout-%d", i),
			Prompt: p,
			Stdout: os.Stderr,
			Stderr: os.Stderr,
		}
	}

	results, err := pool.Run(ctx, reqs)
	if err != nil {
		return err
	}

	extract := taboo.JSONResult[Finding]()
	for i, res := range results {
		if res.Err != nil {
			fmt.Printf("run %d: failed: %v\n", i, res.Err)
			continue
		}
		v, err := extract.Extract(res.Output)
		if err != nil {
			fmt.Printf("run %d: %s (no structured result: %v)\n", i, res.Commit, err)
			continue
		}
		fmt.Printf("run %d: %s -> %+v\n", i, res.Commit, v.(Finding))
	}
	return nil
}
`)
	}
	return []byte(`// Command main runs the agent configured in taboo.yaml once, directly through
// the taboo library. ` + "`taboo init --template single`" + ` scaffolds it as a skeleton
// to grow into fan-out or structured output (see
// github.com/josecabralf/taboo/pkg/taboo). It reads the same taboo.yaml the CLI
// uses, so moving from ` + "`taboo run`" + ` to ` + "`go run .`" + ` reuses the same config
// with no extra setup.
package main

import (
	"context"
	"fmt"
	"os"

	taboo "github.com/josecabralf/taboo/pkg/taboo"
)

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := taboo.LoadConfig("taboo.yaml")
	if err != nil {
		return err
	}
	if cfg.Profile == nil {
		return fmt.Errorf("taboo.yaml needs an agent: set one and re-run")
	}

	runner := taboo.New(taboo.Config{
		Workshop:   taboo.WorkshopName(cfg.Workshop, cfg.Profile.Name()),
		Base:       cfg.Base,
		Agent:      cfg.Profile,
		RepoPath:   cfg.Repo,
		ProjectDir: ".",
	}, taboo.NewExecCommander())

	// Edit this prompt, or read one of your workflow prompt files under prompts/.
	res, err := taboo.NewOrchestrator(runner).Run(ctx, taboo.OrchestratedRequest{
		RunRequest: taboo.RunRequest{
			Branch: "taboo/go-run",
			Prompt: "Summarize what this repository does.",
			Stdout: os.Stderr,
			Stderr: os.Stderr,
		},
	})
	if err != nil {
		return err
	}
	fmt.Printf("branch: %s\ncommit: %s\n", res.Branch, res.Commit)
	return nil
}
`)
}

// renderGoMod returns the scaffolded go.mod: it names the module after the
// workshop and pins the taboo library to the exact libraryVersion (no @latest,
// no replace) so the scaffold is reproducible.
func renderGoMod(in scaffoldInputs) []byte {
	var buf bytes.Buffer
	buf.WriteString("// go.mod — generated by `taboo init`. Pins the taboo library to the exact\n")
	buf.WriteString("// version this CLI shipped with so the scaffold is reproducible: a fixed\n")
	buf.WriteString("// require, no overrides. Run `go mod tidy` to fetch it.\n")
	buf.WriteString("module " + in.Workshop + "\n")
	buf.WriteString("\n")
	buf.WriteString("go " + scaffoldGoVersion + "\n")
	buf.WriteString("\n")
	buf.WriteString("require github.com/josecabralf/taboo " + libraryVersion + "\n")
	return buf.Bytes()
}

// fixPromptBody is the prompt seeded at prompts/fix.md for the example fix workflow.
const fixPromptBody = `Investigate why the test suite is failing and fix the underlying bug.

Run the tests to reproduce the failure, make the smallest change that makes them
pass without weakening any assertion, then commit your work with a message that
explains the root cause and the fix.
`

// refactorPromptBody is the prompt seeded at prompts/refactor.md for the example refactor workflow.
const refactorPromptBody = `Refactor this code for clarity and maintainability without changing its
observable behavior.

Keep the public API and all tests passing. Prefer deleting and simplifying over
adding. When you are done, commit your work with a message summarizing what you
changed and why.
`

// renderTabooYAML marshals the config scalars into a strict-loadable taboo.yaml,
// wrapping the document in a header comment. When SeedWorkflows is set it
// marshals a real fix/refactor workflows block (plus default-workflow); when it
// is not, it appends a commented footer showing where the optional defaults: and
// workflows: blocks go. The marshaled struct guarantees the field names
// round-trip through taboo.LoadConfig.
func renderTabooYAML(in scaffoldInputs) ([]byte, error) {
	cfg := taboo.ProjectConfig{
		Workshop: in.Workshop,
		Base:     in.Base,
		Repo:     in.Repo,
		Agent:    in.Agent,
		Model:    in.Model,
		Strategy: "branch",
	}
	if in.SeedWorkflows {
		cfg.Workflows = map[string]taboo.Workflow{
			"fix":      {PromptFile: "prompts/fix.md"},
			"refactor": {PromptFile: "prompts/refactor.md"},
		}
		cfg.DefaultWorkflow = "fix"
	}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("render taboo.yaml: %w", err)
	}
	var buf bytes.Buffer
	buf.WriteString("# taboo.yaml — generated by `taboo init`.\n")
	buf.WriteString("# This is the single source of truth for this taboo project.\n")
	buf.WriteString("# Edit it directly; run `taboo doctor` to validate your changes.\n")
	buf.WriteString("\n")
	buf.Write(body)
	if !in.SeedWorkflows {
		buf.WriteString("\n")
		buf.WriteString("# Optional blocks (uncomment and fill in as you need them):\n")
		buf.WriteString("#\n")
		buf.WriteString("# defaults:\n")
		buf.WriteString("#   branch-prefix: taboo/\n")
		buf.WriteString("#   timeout: 30m\n")
		buf.WriteString("#   max-iterations: 1\n")
		buf.WriteString("#\n")
		buf.WriteString("# workflows:\n")
		buf.WriteString("#   fix:\n")
		buf.WriteString("#     prompt: \"Fix the failing tests.\"\n")
	}
	return buf.Bytes(), nil
}

// renderGitignore returns the .taboo/.gitignore body: a generated-by header
// comment followed by the four entries taboo writes runtime state to.
func renderGitignore() []byte {
	var buf bytes.Buffer
	buf.WriteString("# .gitignore — generated by `taboo init`.\n")
	buf.WriteString("# Runtime state taboo writes that should never be committed.\n")
	buf.WriteString("worktrees/\n")
	buf.WriteString(".workshop/\n")
	buf.WriteString(".env\n")
	buf.WriteString("logs/\n")
	return buf.Bytes()
}

// renderEnvExample returns the .taboo/.env.example body: a header naming the
// chosen agent and explaining taboo forwards only the keys present in the
// environment, then one KEY= line per credential env key the agent reads.
func renderEnvExample(in scaffoldInputs) []byte {
	var buf bytes.Buffer
	buf.WriteString("# .env.example — generated by `taboo init` for agent " + in.Agent + ".\n")
	buf.WriteString("# Copy this to .env, fill in the credential(s) you hold, then load them into\n")
	buf.WriteString("# your shell (e.g. `set -a; source .env; set +a`) before running taboo.\n")
	buf.WriteString("# taboo forwards only the keys present in the environment when it runs,\n")
	buf.WriteString("# so you only need to provide the one credential you use.\n")
	for _, key := range in.Profile.CredentialEnvKeys() {
		buf.WriteString(key + "=\n")
	}
	return buf.Bytes()
}

// writeScaffold creates projectDir and writes every planned file under it,
// creating parent dirs defensively. Dirs are 0750 and files 0600 per the
// repo's gosec posture.
func writeScaffold(projectDir string, files []scaffoldFile) error {
	if err := os.MkdirAll(projectDir, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", projectDir, err)
	}
	for _, f := range files {
		dst := filepath.Join(projectDir, f.Path)
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			return fmt.Errorf("create dir for %s: %w", f.Path, err)
		}
		if err := os.WriteFile(dst, f.Contents, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", f.Path, err)
		}
	}
	return nil
}
