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
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/josecabralf/taboo"
)

// errValidateFailed is the sentinel validate returns when any check is an error.
// The report is fully printed before it is returned; main maps it to a non-zero
// exit. Mirrors doctor's errChecksFailed.
var errValidateFailed = errors.New("validate: one or more checks failed")

// newValidateCmd builds the `validate` subcommand: it discovers the project's
// taboo.yaml, strict-decodes it, and runs config-correctness checks (agents,
// models, prompt files, repo), printing a human or --json report to env.Stdout.
// It returns errValidateFailed when any check is an error so the process exits
// non-zero.
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

// validateChecks discovers the taboo.yaml from env.Getwd(), strict-decodes it,
// and returns the full config-correctness check set the validate command reports.
// A discovery or decode failure is a single terminal config error (no further
// checks run); otherwise the parsed config feeds the agent/model/prompt-file/repo
// check groups. The injected statFile callback makes discovery and prompt-file
// existence testable.
func validateChecks(ctx context.Context, env Env, statFile func(string) bool) []check {
	return configCorrectnessChecks(ctx, env, statFile, true)
}

// runConfigChecks returns the config-correctness checks run's preflight needs:
// the full validate set minus prompt-file existence. Resolving the plan already
// reads and validates the one prompt-file this run actually consumes, so
// re-checking it here is redundant — and statting every other config-referenced
// prompt-file would let an unrelated stale one (e.g. a defaults.prompt-file an
// ad-hoc --prompt run never touches) abort a run that does not need it.
// Whole-config prompt-file linting stays the job of `taboo validate`.
func runConfigChecks(ctx context.Context, env Env, statFile func(string) bool) []check {
	return configCorrectnessChecks(ctx, env, statFile, false)
}

// configCorrectnessChecks is the shared body behind validateChecks and
// runConfigChecks: discover + strict-decode the taboo.yaml, then assemble the
// correctness checks. The includePromptFiles flag toggles the prompt-file
// existence group — validate wants it (whole-config lint), run's preflight does
// not (it is run-scoped). It is distinct from config.go's configChecks, which is
// doctor's host-side config probe.
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
	if includePromptFiles {
		checks = append(checks, promptFileChecks(cfg, path, statFile)...)
		checks = append(checks, varsChecks(cfg, path, statFile)...)
	}
	// Resolve the repo directory once, config-anchored (never the process CWD), so
	// the repo checks and the derive check both speak about the same directory a
	// real run resolves to.
	repoBase := resolveRepoBase(filepath.Dir(path), cfg.Repo)
	checks = append(checks, repoValidateChecks(ctx, env, cfg, repoBase)...)
	if includePromptFiles {
		checks = append(checks, deriveChecks(cfg, repoBase, statFile)...)
	}
	return checks
}

// deriveChecks traces that the agent workshop derives from the project's source
// workshop.yaml. It is gated to validate (whole-config lint), never run's
// preflight. A missing source workshop.yaml is a hard source-definition error
// (the remedy names the file) and skips derive; otherwise it dry-runs the full
// derivation in memory (no launch, no FS writes) and reports derive as a hard
// error carrying the underlying message when the source is malformed, ok
// otherwise.
//
// The repoBase argument is the config-anchored repo directory (see
// resolveRepoBase), resolved against the discovered config's directory rather
// than the process CWD. A relative repo value (including the dot repo of a
// .taboo config, whose project root is the parent of .taboo) therefore lands on
// the same tracked <root>/workshop.yaml whether validate runs from the repo root
// or from .taboo.
func deriveChecks(cfg taboo.ProjectConfig, repoBase string, statFile func(string) bool) []check {
	if cfg.Repo == "" {
		// Mirror doctor's workshopProjectChecks: without a configured repo there is
		// no <repo>/workshop.yaml to derive from. repoValidateChecks already flags
		// the unset repo as a hard error, so don't double-report.
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
		return nil // unknown agent: agentChecks already flags it, don't double-report.
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

// resolveRepoBase resolves the project repo directory the same way a real run
// does, anchored to the config's directory rather than the process CWD. It
// mirrors the runtime resolver internal/config.resolveRepoPath (which the CLI
// cannot import because it is internal). An absolute repo value stands on its
// own; a relative one anchors to configDir; a dot repo in a .taboo config
// resolves to the parent (the repo root). The result is best-effort absolute,
// falling back to the joined path when filepath.Abs fails so the caller still has
// a usable path.
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

// decodeValidate strict-decodes data as a single taboo.yaml document into the
// exported ProjectConfig. It shares the library decodeStrict's strictness
// (KnownFields, single document) but differs deliberately in two ways: it does
// NOT resolve agent profiles — an unknown agent must surface as a per-agent
// check, not abort the whole report — and it treats an empty document as an error
// ("config is empty") rather than the zero config decodeStrict returns, because
// rejecting an empty config is this command's job (the loader leaves it to
// validate). So validate decodes the raw struct itself.
func decodeValidate(data []byte) (taboo.ProjectConfig, error) {
	var cfg taboo.ProjectConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // reject unknown keys, same strictness as LoadConfig
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return cfg, errors.New("config is empty")
		}
		return cfg, err
	}
	// taboo.yaml must be a single document. Without this probe a stray "---" would
	// silently drop everything after the first document; mirror LoadConfig's
	// decodeStrict and reject any trailing document instead.
	if trailing := dec.Decode(&struct{}{}); !errors.Is(trailing, io.EOF) {
		return cfg, errors.New("multiple YAML documents not supported")
	}
	return cfg, nil
}

// referencedAgents returns the distinct, non-empty, sorted set of agent names the
// config refers to: the top-level agent plus each workflow's effective agent (its
// own, falling back to the top level).
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
// unknown agent hard-fails with a precise "did you mean <closest>" drawn from the
// registry's candidate set (omitted when no candidate is close enough); a single
// ok summarizes when at least one agent is referenced and all are known.
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

// referencedModels returns the distinct, sorted set of effective (agent, model)
// bindings the config produces: the top-level pair plus each workflow's effective
// pair (its own agent/model, each falling back to the top level). Bindings with
// no agent are dropped — an empty model is only meaningful relative to an agent
// that needs one.
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
// a hard failure — a model is required wherever an agent is configured. A model
// that does not match the agent's format hint is only an advisory WARN (story
// #25): the heuristic catches likely mistakes without blocking a deliberate but
// unusual choice, so a clean config emits no per-model check at all.
//
// The empty-model failure is keyed model/<agent> (one agent has at most one empty
// binding), but a format warning is keyed model/<agent>/<model>: a single agent
// can be bound to several distinct models across workflows, so the model must be
// in the check name to keep names unique (mirroring agent/<name> and
// prompt-file/<path>).
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

// promptFiles returns the distinct, non-empty, sorted set of prompt-file paths the
// config refers to: the defaults block plus each workflow.
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

// varsChecks reports, per workflow, the {{VAR}} placeholders its effective
// prompt references — an OK-level discoverability surface ("what vars does this
// workflow take?"), never a failure. It is gated behind includePromptFiles
// (whole-config lint: validate only, never run's preflight, which has its own
// stderr warning). The effective prompt mirrors resolvePrompt's config layers
// (no CLI overrides exist here): workflow inline prompt, else workflow
// prompt-file contents, else the defaults prompt/prompt-file. A prompt-file is
// read only when the injected statFile says it exists — a missing one emits no
// vars check (promptFileChecks already hard-fails it; don't double-report) —
// and a placeholder-free workflow emits nothing, mirroring modelChecks'
// clean-config silence.
func varsChecks(cfg taboo.ProjectConfig, configPath string, statFile func(string) bool) []check {
	base := filepath.Dir(configPath)
	names := make([]string, 0, len(cfg.Workflows))
	for name := range cfg.Workflows {
		names = append(names, name)
	}
	slices.Sort(names)
	var checks []check
	for _, name := range names {
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

// effectivePrompt resolves a workflow's prompt text from the config layers
// alone, mirroring the bridge's resolvePrompt precedence minus the CLI
// overrides (validate has none): workflow inline → workflow prompt-file →
// defaults inline → defaults prompt-file. It reports found=false when nothing
// is configured or a configured prompt-file is absent/unreadable.
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

// readExistingPromptFile reads a configured prompt-file's contents, resolving a
// relative path against the config dir, but only when the injected statFile
// says it exists — existence reporting stays promptFileChecks' job.
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

// promptFileChecks confirms every referenced prompt file exists, resolving a
// relative path against the config file's directory.
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

// repoValidateChecks confirms the configured repo is usable: it must be set, then
// (reusing doctor's leaf checks) be on persistent storage (not tmpfs) and be a git
// work tree. The leaf checks run against repoBase, the config-anchored absolute
// repo path deriveChecks and a real run also resolve to, not the raw configured
// value, so a dot repo is judged where it actually lives rather than against the
// process CWD. An unset repo is a hard failure: validate runs against a real repo.
func repoValidateChecks(ctx context.Context, env Env, cfg taboo.ProjectConfig, repoBase string) []check {
	if cfg.Repo == "" {
		return []check{fail("repo", "no repo configured (set top-level repo:)")}
	}
	return []check{repoLocationCheck(repoBase), repoGitCheck(ctx, env, repoBase)}
}
