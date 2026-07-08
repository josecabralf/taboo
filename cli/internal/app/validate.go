package app

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/josecabralf/taboo"
)

// errValidateFailed is the sentinel validate returns when any check is an error.
// Mirrors doctor's errChecksFailed.
var errValidateFailed = errors.New("validate: one or more checks failed")

// newValidateCmd builds the `validate` subcommand.
func newValidateCmd(env Env) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate the project's taboo.yaml for correctness",
		Long: "validate discovers the taboo.yaml for the current project and checks it is " +
			"internally correct: agents resolve to known CLIs, models look well-formed, " +
			"referenced prompt files exist, and the configured repo is a git work tree on " +
			"persistent storage. It does not probe host tooling — that is `taboo doctor`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			checks := validateChecks(cmd.Context(), env, statFileExists)
			if err := renderReport(env, asJSON, "taboo validate — config correctness", checks); err != nil {
				return err
			}
			if anyError(checks) {
				return errValidateFailed
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the report as JSON")
	return cmd
}

// validateChecks returns the full config-correctness check set the validate
// command reports.
func validateChecks(ctx context.Context, env Env, statFile func(string) bool) []check {
	return configCorrectnessChecks(ctx, env, statFile, true)
}

// runConfigChecks returns the config-correctness checks run's preflight needs: the
// full validate set minus prompt-file existence; cfg.Plan already validates the
// one prompt-file the run consumes, and statting every other config-referenced
// prompt-file would let an unrelated stale one abort a run that never touches it.
func runConfigChecks(ctx context.Context, env Env, statFile func(string) bool) []check {
	return configCorrectnessChecks(ctx, env, statFile, false)
}

// configCorrectnessChecks is the shared body behind validateChecks and
// runConfigChecks: discover and strict-decode the taboo.yaml, then assemble the
// checks; includePromptFiles toggles the prompt-file existence group (validate
// wants it; run's preflight is run-scoped). Distinct from config.go's
// configChecks, which is doctor's host-side probe.
func configCorrectnessChecks(ctx context.Context, env Env, statFile func(string) bool, includePromptFiles bool) []check {
	wd, err := env.Getwd()
	if err != nil {
		return []check{fail("config", "cannot determine the working directory: "+err.Error())}
	}
	path, found := findConfig(wd, statFile)
	if !found {
		return []check{fail("config", "no taboo.yaml found from "+wd+" — run `taboo init` first")}
	}
	// path comes from findConfig over a trusted working directory, not untrusted input.
	data, err := os.ReadFile(path) // #nosec G304
	if err != nil {
		return []check{fail("config", "cannot read "+path+": "+err.Error())}
	}
	cfg, err := decodeValidate(data)
	if err != nil {
		return []check{fail("config", "invalid taboo.yaml at "+path+": "+err.Error())}
	}
	checks := []check{ok("config", "parsed "+path)}
	checks = append(checks, agentChecks(cfg)...)
	checks = append(checks, modelChecks(cfg)...)
	checks = append(checks, strategyChecks(cfg)...)
	if includePromptFiles {
		checks = append(checks, promptFileChecks(cfg, path, statFile)...)
		checks = append(checks, varsChecks(cfg, path, statFile)...)
		checks = append(checks, defaultWorkflowCheck(cfg)...)
		checks = append(checks, loopChecks(cfg, path, statFile)...)
	}
	// Resolve the repo directory config-anchored (never the process CWD) so the
	// repo checks and the derive check speak about the same directory a real run
	// resolves to.
	repoBase := resolveRepoBase(filepath.Dir(path), cfg.Repo)
	checks = append(checks, repoValidateChecks(ctx, env, cfg, repoBase)...)
	if includePromptFiles {
		checks = append(checks, deriveChecks(cfg, repoBase, statFile)...)
	}
	return checks
}

// deriveChecks traces that the agent workshop derives from the project's source
// workshop.yaml. Gated to validate, never run's preflight. A missing source
// workshop.yaml is a hard source-definition error and skips derive; otherwise it
// dry-runs the derivation in memory (no launch, no FS writes) and reports derive
// as a hard error when the source is malformed.
//
// The repoBase parameter is the config-anchored repo directory (see resolveRepoBase), so a
// relative repo value lands on the same <root>/workshop.yaml whether validate runs
// from the repo root or from .taboo.
func deriveChecks(cfg taboo.ProjectConfig, repoBase string, statFile func(string) bool) []check {
	if cfg.Repo == "" {
		// repoValidateChecks already flags an unset repo; don't double-report.
		return nil
	}
	src := filepath.Join(repoBase, "workshop.yaml")
	if !statFile(src) {
		return []check{
			fail("source-definition", "no workshop.yaml in "+repoBase+": taboo derives the "+
				"agent's workshop from it; create one there, then re-run"),
			fail("derive", "skipped: no source workshop.yaml to derive from (see source-definition above)"),
		}
	}
	profile, err := taboo.NewProfile(cfg.Agent, cfg.Model)
	if err != nil {
		return nil // agentChecks already flags an unknown agent; don't double-report.
	}
	runnerCfg := taboo.Config{
		Workshop: workshopName(cfg.Workshop, string(profile.Name())),
		Agent:    profile,
		RepoPath: repoBase,
	}
	// src comes from the configured repo path, not untrusted input.
	source, err := os.ReadFile(src) // #nosec G304
	if err != nil {
		return []check{ok("source-definition", "resolves to "+src), fail("derive", err.Error())}
	}
	if _, err := taboo.DryRunDerive(runnerCfg, source); err != nil {
		return []check{ok("source-definition", "resolves to "+src), fail("derive", err.Error())}
	}
	return []check{
		ok("source-definition", "resolves to "+src),
		ok("derive", "agent workshop derives cleanly from "+src),
	}
}

// resolveRepoBase resolves the project repo directory the way a real run does,
// anchored to the config's directory not the process CWD. It mirrors the runtime
// resolver internal/config.resolveRepoPath (which the CLI cannot import). Absolute
// repo stands alone; relative anchors to configDir; a dot repo in a .taboo config
// resolves to the parent. Best-effort absolute, falling back to the joined path.
func resolveRepoBase(configDir, repo string) string {
	base := configDir
	switch {
	case repo != "" && filepath.Clean(repo) != ".":
		// A relative repo anchors to the config dir; filepath.Join would otherwise
		// nest an absolute repo under it.
		base = repo
		if !filepath.IsAbs(repo) {
			base = filepath.Join(configDir, repo)
		}
	case filepath.Base(configDir) == ".taboo":
		base = filepath.Dir(configDir)
	}
	if abs, err := filepath.Abs(base); err == nil {
		return abs
	}
	return base
}

// decodeValidate strict-decodes data as a single taboo.yaml document. It shares
// the library decodeStrict's strictness but differs in two ways: it does NOT
// resolve agent profiles (an unknown agent surfaces as a per-agent check, not a
// whole-report abort), and it treats an empty document as a `config is empty`
// error rather than the zero config decodeStrict returns.
func decodeValidate(data []byte) (taboo.ProjectConfig, error) {
	var cfg taboo.ProjectConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // reject unknown keys, same strictness as LoadConfig.
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return cfg, errors.New("config is empty")
		}
		return cfg, err
	}
	// taboo.yaml must be a single document: without this probe a stray `---` would
	// silently drop everything after the first document.
	if trailing := dec.Decode(&struct{}{}); !errors.Is(trailing, io.EOF) {
		return cfg, errors.New("multiple YAML documents not supported")
	}
	return cfg, nil
}

// referencedAgents returns the distinct, non-empty, sorted agent names the config
// refers to: the top-level agent plus each workflow's effective agent.
func referencedAgents(cfg taboo.ProjectConfig) []taboo.AgentName {
	seen := map[taboo.AgentName]struct{}{}
	add := func(name taboo.AgentName) {
		if name != "" {
			seen[name] = struct{}{}
		}
	}
	add(cfg.Agent)
	for _, wf := range cfg.Workflows {
		add(cmp.Or(wf.Agent, cfg.Agent))
	}
	out := make([]taboo.AgentName, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// agentChecks verifies every referenced agent resolves to a registered CLI. An
// unknown agent hard-fails with a `did you mean` suggestion; a single ok
// summarizes when all referenced agents are known.
func agentChecks(cfg taboo.ProjectConfig) []check {
	names := referencedAgents(cfg)
	if len(names) == 0 {
		return nil
	}
	known := taboo.AgentNames()
	var checks []check
	allKnown := true
	for _, name := range names {
		if slices.Contains(known, string(name)) {
			continue
		}
		allKnown = false
		msg, _ := unknownAgentMessage(string(name), known)
		checks = append(checks, fail("agent/"+string(name), msg))
	}
	if allKnown {
		strs := make([]string, len(names))
		for i, n := range names {
			strs[i] = string(n)
		}
		checks = append(checks, ok("agent", "all referenced agents are registered ("+strings.Join(strs, ", ")+")"))
	}
	return checks
}

// agentModel is one effective (agent, model) binding the config produces.
type agentModel struct {
	agent taboo.AgentName
	model string
}

// referencedModels returns the distinct, sorted effective (agent, model) bindings
// the config produces: the top-level pair plus each workflow's effective pair.
// Bindings with no agent are dropped, since an empty model is only meaningful
// relative to an agent that needs one.
func referencedModels(cfg taboo.ProjectConfig) []agentModel {
	seen := map[agentModel]struct{}{}
	var out []agentModel
	add := func(agent taboo.AgentName, model string) {
		if agent == "" {
			return
		}
		am := agentModel{agent: agent, model: model}
		if _, dup := seen[am]; dup {
			return
		}
		seen[am] = struct{}{}
		out = append(out, am)
	}
	add(cfg.Agent, cfg.Model)
	for _, wf := range cfg.Workflows {
		add(cmp.Or(wf.Agent, cfg.Agent), cmp.Or(wf.Model, cfg.Model))
	}
	slices.SortFunc(out, func(a, b agentModel) int {
		if c := strings.Compare(string(a.agent), string(b.agent)); c != 0 {
			return c
		}
		return strings.Compare(a.model, b.model)
	})
	return out
}

// modelChecks verifies every effective (agent, model) binding. An empty model is
// a hard failure. A model that does not match the agent's format hint is an
// advisory WARN, so a clean config emits no per-model check.
//
// The empty-model failure is keyed model/<agent>, but a format warning is keyed
// model/<agent>/<model>: one agent can bind several models across workflows, so
// the model must be in the key to keep names unique.
func modelChecks(cfg taboo.ProjectConfig) []check {
	var checks []check
	for _, am := range referencedModels(cfg) {
		if strings.TrimSpace(am.model) == "" {
			checks = append(checks, fail("model/"+string(am.agent),
				"agent \""+string(am.agent)+"\" has no model configured (model is required)"))
			continue
		}
		if ok, expected := taboo.MatchModelFormat(am.agent, am.model); !ok {
			checks = append(checks, warn("model/"+string(am.agent)+"/"+am.model,
				"model \""+am.model+"\" does not look like a model for "+string(am.agent)+" (expected "+
					expected+"); set it intentionally to silence this"))
		}
	}
	return checks
}

// promptFiles returns the distinct, non-empty, sorted prompt-file paths the config
// refers to: the defaults block plus each workflow.
func promptFiles(cfg taboo.ProjectConfig) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(p string) {
		if p == "" {
			return
		}
		if _, dup := seen[p]; dup {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	if cfg.Defaults != nil {
		add(cfg.Defaults.PromptFile)
	}
	for _, wf := range cfg.Workflows {
		add(wf.PromptFile)
	}
	slices.Sort(out)
	return out
}

// sortedWorkflowNames returns the config's workflow names in sorted order, the
// deterministic iteration the per-workflow check groups share.
func sortedWorkflowNames(cfg taboo.ProjectConfig) []string {
	names := make([]string, 0, len(cfg.Workflows))
	for name := range cfg.Workflows {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// varsChecks reports, per workflow, the {{VAR}} placeholders its effective prompt
// references: an OK-level discoverability surface, never a failure. Gated behind
// includePromptFiles (validate only). A missing prompt-file emits no vars check
// (promptFileChecks already hard-fails it), and a placeholder-free workflow emits
// nothing.
func varsChecks(cfg taboo.ProjectConfig, configPath string, statFile func(string) bool) []check {
	base := filepath.Dir(configPath)
	var checks []check
	for _, name := range sortedWorkflowNames(cfg) {
		text, found := effectivePrompt(cfg, cfg.Workflows[name], base, statFile)
		if !found {
			continue
		}
		placeholders := taboo.Placeholders(text)
		if len(placeholders) == 0 {
			continue
		}
		checks = append(checks, ok("vars/"+name, "prompt references: "+strings.Join(placeholders, ", ")))
	}
	return checks
}

// loopChecks reports, per workflow in sorted name order, the loop-knob
// misconfigurations validate can see from the config alone. At most one of signal/
// or loop/ fires per workflow: the conditions are mutually exclusive (one needs a
// non-empty effective signal, the other an empty one).
//
//   - signal/<name> (warn): the effective signal is non-empty but is not a
//     substring of the effective prompt, so the agent is never told to print the
//     sentinel and the signal-based early stop can never fire. Advisory only.
//     A workflow whose effective prompt is unresolvable is skipped.
//   - loop/<name> (warn): the effective max-iterations is greater than 1 with no
//     effective signal, so the early stop is disabled and every run pays the full
//     N iterations. Silent at max-iterations <= 1, and when the effective
//     stop-on-no-change is on (that knob IS an early stop). It does NOT silence
//     signal/: stop-on-no-change cannot fix a mistyped sentinel.
//
// Gated behind includePromptFiles: whole-config linting is validate's job.
func loopChecks(cfg taboo.ProjectConfig, configPath string, statFile func(string) bool) []check {
	defaults := cfg.Defaults
	if defaults == nil {
		defaults = &taboo.RunDefaults{}
	}
	base := filepath.Dir(configPath)
	var checks []check
	for _, name := range sortedWorkflowNames(cfg) {
		wf := cfg.Workflows[name]
		signal := cmp.Or(wf.CompletionSignal, defaults.CompletionSignal)
		if signal == "" {
			if wf.StopOnNoChange || defaults.StopOnNoChange {
				continue // stop-on-no-change is an early stop; the loop warn's premise is false.
			}
			if maxIter := cmp.Or(wf.MaxIterations, defaults.MaxIterations); maxIter > 1 {
				checks = append(checks, warn("loop/"+name,
					"max-iterations is "+strconv.Itoa(maxIter)+" but no completion-signal is set "+
						"(workflow or defaults): the loop has no early stop, so every run pays the "+
						"full "+strconv.Itoa(maxIter)+" iterations; set a completion-signal or "+
						"enable stop-on-no-change"))
			}
			continue
		}
		prompt, found := effectivePrompt(cfg, wf, base, statFile)
		if !found {
			continue
		}
		if !strings.Contains(prompt, signal) {
			checks = append(checks, warn("signal/"+name,
				"completion-signal \""+signal+"\" never appears in the effective prompt: the agent "+
					"is never told to print it, so the signal-based early stop can never fire; "+
					"set it intentionally to silence this"))
		}
	}
	return checks
}

// defaultWorkflowCheck verifies a configured default-workflow names a defined
// workflow, hard-failing with the same wording selectRun uses at run time. An
// unset default-workflow is legal and emits nothing (a bare `taboo run` refuses
// via noSelectionError). Gated behind includePromptFiles.
func defaultWorkflowCheck(cfg taboo.ProjectConfig) []check {
	if cfg.DefaultWorkflow == "" {
		return nil
	}
	if _, defined := cfg.Workflows[cfg.DefaultWorkflow]; !defined {
		// strconv.Quote matches selectRun's %q byte-for-byte, exotic names included.
		return []check{fail("default-workflow", "default-workflow "+strconv.Quote(cfg.DefaultWorkflow)+
			" is not defined (configured workflows: "+availableWorkflows(&cfg)+")")}
	}
	return []check{ok("default-workflow", "resolves to workflow "+strconv.Quote(cfg.DefaultWorkflow))}
}

// strategyChecks validates the workspace strategy against the closed set the
// runner accepts. An omitted strategy is valid (defaults to worktree) and reports
// nothing. It catches a set-but-unknown value: decodeValidate accepts any string,
// so without this check a typo would pass validate and only surface later at
// run/doctor. It delegates to BranchingStrategy.Validate, the same guard
// LoadConfig uses.
func strategyChecks(cfg taboo.ProjectConfig) []check {
	if cfg.Strategy == "" {
		return nil
	}
	if err := cfg.Strategy.Validate(); err != nil {
		return []check{fail("strategy", err.Error())}
	}
	return []check{ok("strategy", "workspace strategy "+strconv.Quote(string(cfg.Strategy)))}
}

// effectivePrompt resolves a workflow's prompt text from the config layers alone,
// mirroring the bridge's resolvePrompt precedence minus the CLI overrides:
// workflow inline, workflow prompt-file, defaults inline, defaults prompt-file. It
// reports found=false when nothing is configured or a prompt-file is
// absent/unreadable.
func effectivePrompt(cfg taboo.ProjectConfig, wf taboo.Workflow, base string, statFile func(string) bool) (text string, found bool) {
	defaults := cfg.Defaults
	if defaults == nil {
		defaults = &taboo.RunDefaults{}
	}
	switch {
	case wf.Prompt != "":
		return wf.Prompt, true
	case wf.PromptFile != "":
		return readExistingPromptFile(wf.PromptFile, base, statFile)
	case defaults.Prompt != "":
		return defaults.Prompt, true
	case defaults.PromptFile != "":
		return readExistingPromptFile(defaults.PromptFile, base, statFile)
	default:
		return "", false
	}
}

// readExistingPromptFile reads a configured prompt-file's contents, but only when
// the injected statFile says it exists; existence reporting stays
// promptFileChecks' job.
func readExistingPromptFile(path, base string, statFile func(string) bool) (string, bool) {
	resolved := resolvePromptFilePath(path, base)
	if !statFile(resolved) {
		return "", false
	}
	// resolved comes from the trusted config, like promptFileChecks' probe.
	data, err := os.ReadFile(resolved) // #nosec G304
	if err != nil {
		return "", false
	}
	return string(data), true
}

// promptFileChecks confirms every referenced prompt file exists.
func promptFileChecks(cfg taboo.ProjectConfig, configPath string, statFile func(string) bool) []check {
	base := filepath.Dir(configPath)
	var checks []check
	for _, p := range promptFiles(cfg) {
		resolved := resolvePromptFilePath(p, base)
		if statFile(resolved) {
			checks = append(checks, ok("prompt-file/"+p, "prompt file "+p+" found"))
		} else {
			checks = append(checks, fail("prompt-file/"+p,
				"prompt file \""+p+"\" not found (resolved to "+resolved+")"))
		}
	}
	return checks
}

// repoValidateChecks confirms the configured repo is usable: set, on persistent
// storage (not tmpfs), and a git work tree. The leaf checks run against repoBase,
// the config-anchored path a real run resolves to, so a dot repo is judged where
// it actually lives. An unset repo is a hard failure.
func repoValidateChecks(ctx context.Context, env Env, cfg taboo.ProjectConfig, repoBase string) []check {
	if cfg.Repo == "" {
		return []check{fail("repo", "no repo configured (set top-level repo:)")}
	}
	return []check{repoLocationCheck(repoBase), repoGitCheck(ctx, env, repoBase)}
}
