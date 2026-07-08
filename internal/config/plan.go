package config

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/josecabralf/taboo/internal/agent"
	"github.com/josecabralf/taboo/internal/prompt"
	"github.com/josecabralf/taboo/internal/run"
	"github.com/josecabralf/taboo/internal/workshop"
)

// ErrUnknownWorkflow is the sentinel Plan wraps when the requested workflow name
// matches no entry in the config's Workflows map.
var ErrUnknownWorkflow = errors.New("taboo: unknown workflow")

// ErrNoPrompt is the sentinel Plan returns when no prompt is configured anywhere
// in the override → workflow → defaults precedence chain.
var ErrNoPrompt = errors.New("taboo: no prompt configured")

// ErrNoAgent is the sentinel Plan returns when no agent is configured anywhere
// in the override → workflow → top-level precedence chain.
var ErrNoAgent = errors.New("taboo: no agent configured (set agent: on the workflow or a top-level agent:)")

// Plan resolves the config, workflow, and overrides into a *run.Plan, replicating
// the CLI's resolvePlan precedence. The agent profile is re-resolved here so
// override agent/model take effect.
func (c *ProjectConfig) Plan(configDir, workflow string, vars map[string]string, ov run.PlanOverrides) (*run.Plan, error) {
	var wf Workflow
	if workflow != "" {
		found, ok := c.Workflows[workflow]
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownWorkflow, workflow)
		}
		wf = found
	}

	defaults := c.Defaults
	if defaults == nil {
		defaults = &RunDefaults{}
	}

	model := cmp.Or(ov.Model, wf.Model, c.Model)
	agentName := cmp.Or(ov.Agent, wf.Agent, c.Agent)
	if agentName == "" {
		return nil, ErrNoAgent
	}
	profile, err := agent.NewProfile(agentName, model)
	if err != nil {
		return nil, err
	}

	promptText, err := resolvePrompt(ov, wf, defaults, configDir)
	if err != nil {
		return nil, err
	}
	// Record the template's placeholders before substitution: the substituted
	// prompt no longer carries the {{VAR}} names, and callers need them to detect
	// unused supplied keys and unfilled placeholders.
	placeholders := prompt.Placeholders(promptText)
	if len(vars) > 0 {
		promptText, err = prompt.Substitute(promptText, vars)
		if err != nil {
			return nil, err
		}
	}

	repoPath, err := resolveRepoPath(configDir, c.Repo)
	if err != nil {
		return nil, err
	}
	projectDir := filepath.Join(repoPath, ".taboo")

	sourceDefinition := cmp.Or(ov.From, c.SourceDefinition)

	// Scalar loop knobs: override → workflow → defaults, first non-zero wins.
	// defaults is non-nil here, so cmp.Or covers every layer.
	timeout := cmp.Or(ov.Timeout, time.Duration(wf.Timeout), time.Duration(defaults.Timeout))
	maxIter := cmp.Or(ov.MaxIterations, wf.MaxIterations, defaults.MaxIterations)
	signal := cmp.Or(ov.CompletionSignal, wf.CompletionSignal, defaults.CompletionSignal)
	// stop-on-no-change is a boolean OR across layers, not the first-non-zero
	// chain: opt-in only, any layer enables, none disables.
	stopOnNoChange := ov.StopOnNoChange || wf.StopOnNoChange || defaults.StopOnNoChange

	branch := resolveBranch(ov, defaults, workflow)

	stdout := orDiscard(ov.Stdout)
	stderr := orDiscard(ov.Stderr)

	return &run.Plan{
		Config: workshop.Config{
			Workshop:         workshop.WorkshopName(c.Workshop, string(profile.Name())),
			Base:             c.Base,
			Agent:            profile,
			RepoPath:         repoPath,
			ProjectDir:       projectDir,
			SourceDefinition: sourceDefinition,
			Strategy:         c.Strategy,
		},
		Request: run.OrchestratedRequest{
			RunRequest:       run.RunRequest{Branch: branch, BaseRef: ov.BaseRef, Prompt: promptText, Timeout: timeout, Stdout: stdout, Stderr: stderr},
			MaxIterations:    maxIter,
			CompletionSignal: signal,
			StopOnNoChange:   stopOnNoChange,
		},
		Workflow:     workflow,
		Model:        model,
		Placeholders: placeholders,
	}, nil
}

// resolvePrompt applies the prompt precedence (first non-empty wins): override
// inline/file → workflow inline/file → defaults inline/file → else ErrNoPrompt. A
// *-file value is read from disk; a read error is wrapped and is NOT ErrNoPrompt.
func resolvePrompt(ov run.PlanOverrides, wf Workflow, defaults *RunDefaults, configDir string) (string, error) {
	switch {
	case ov.Prompt != "":
		return ov.Prompt, nil
	case ov.PromptFile != "":
		return readPromptFile(ov.PromptFile, configDir)
	case wf.Prompt != "":
		return wf.Prompt, nil
	case wf.PromptFile != "":
		return readPromptFile(wf.PromptFile, configDir)
	case defaults.Prompt != "":
		return defaults.Prompt, nil
	case defaults.PromptFile != "":
		return readPromptFile(defaults.PromptFile, configDir)
	default:
		return "", ErrNoPrompt
	}
}

// orDiscard returns w, or io.Discard when w is nil, so a nil sink becomes a no-op
// writer the runner can stream to.
func orDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

// resolveRepoPath resolves the run's repo path, always absolute and config-
// anchored: an explicit repo: is used as-is when absolute else relative to
// configDir; a .taboo config dir resolves to its parent; a bare taboo.yaml
// resolves to configDir itself.
func resolveRepoPath(configDir, repo string) (string, error) {
	base := configDir
	switch {
	case repo != "" && filepath.Clean(repo) != ".":
		// An absolute repo: stands alone; a relative one anchors to configDir (else
		// filepath.Join would nest an absolute path under it).
		base = repo
		if !filepath.IsAbs(repo) {
			base = filepath.Join(configDir, repo)
		}
	case filepath.Base(configDir) == ".taboo":
		base = filepath.Dir(configDir)
	}
	return filepath.Abs(base)
}

// resolveBranch returns the override branch, or a fresh per-run branch (prefix +
// label + timestamp + nanosecond) so back-to-back runs never collide. The label
// is the workflow name, or "adhoc" for a prompt-only run.
func resolveBranch(ov run.PlanOverrides, defaults *RunDefaults, workflow string) string {
	if ov.Branch != "" {
		return ov.Branch
	}
	label := workflow
	if label == "" {
		label = "adhoc"
	}
	now := time.Now()
	return fmt.Sprintf("%s%s-%s-%09d", defaults.BranchPrefix, label, now.Format("20060102-150405"), now.Nanosecond())
}

// FindConfig ascends from start looking for a config: at each dir it probes
// taboo.yaml then .taboo/taboo.yaml, returning the first hit. It stops at the
// filesystem root with "", false.
func FindConfig(start string) (string, bool) {
	dir := filepath.Clean(start)
	for {
		if p := filepath.Join(dir, "taboo.yaml"); fileExists(p) {
			return p, true
		}
		if p := filepath.Join(dir, ".taboo", "taboo.yaml"); fileExists(p) {
			return p, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// fileExists reports whether p can be stat'd without error.
func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// readPromptFile reads a configured prompt-file, resolving a relative path
// against configDir, and returns its contents verbatim.
func readPromptFile(path, configDir string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(configDir, path)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- trusted config-derived path
	if err != nil {
		return "", fmt.Errorf("taboo: read prompt-file %s: %w", path, err)
	}
	return string(data), nil
}
