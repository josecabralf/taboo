package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	taboo "github.com/josecabralf/taboo/pkg"
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
	}
	checks = append(checks, repoValidateChecks(ctx, env, cfg)...)
	if includePromptFiles {
		checks = append(checks, deriveChecks(cfg, statFile)...)
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
func deriveChecks(cfg taboo.ProjectConfig, statFile func(string) bool) []check {
	if cfg.Repo == "" {
		// Mirror doctor's workshopProjectChecks: without a configured repo there is
		// no <repo>/workshop.yaml to derive from. Guarding here also avoids statting
		// a bare relative "workshop.yaml" against the validate CWD (a false positive
		// if a stray one happens to sit there). repoValidateChecks already flags the
		// unset repo as a hard error, so don't double-report.
		return nil
	}
	src := filepath.Join(cfg.Repo, "workshop.yaml")
	if !statFile(src) {
		return []check{
			fail("source-definition", "no workshop.yaml found in "+cfg.Repo+": taboo derives the "+
				"agent's workshop from the project's workshop.yaml; add a workshop.yaml, then re-run"),
			fail("derive", "skipped: no source workshop.yaml to derive from (see source-definition above)"),
		}
	}
	profile, err := taboo.NewProfile(cfg.Agent, cfg.Model)
	if err != nil {
		return nil // unknown agent: agentChecks already flags it, don't double-report.
	}
	runnerCfg := taboo.Config{
		Workshop: workshopName(cfg.Workshop, profile.Name()),
		Agent:    profile,
		RepoPath: cfg.Repo,
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

// orElse returns override when non-empty, otherwise fallback — the
// workflow-then-top-level precedence taboo applies to agent and model. It mirrors
// the unexported helper in pkg/taboo (config.go), duplicated here because the CLI
// cannot import it.
func orElse(override, fallback string) string {
	if override == "" {
		return fallback
	}
	return override
}

// referencedAgents returns the distinct, non-empty, sorted set of agent names the
// config refers to: the top-level agent plus each workflow's effective agent (its
// own, falling back to the top level).
func referencedAgents(cfg taboo.ProjectConfig) []string {
	seen := map[string]struct{}{}
	add := func(name string) {
		if name != "" {
			seen[name] = struct{}{}
		}
	}
	add(cfg.Agent)
	for _, wf := range cfg.Workflows {
		add(orElse(wf.Agent, cfg.Agent))
	}
	out := make([]string, 0, len(seen))
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
		if slices.Contains(known, name) {
			continue
		}
		allKnown = false
		msg, _ := unknownAgentMessage(name, known)
		checks = append(checks, fail("agent/"+name, msg))
	}
	if allKnown {
		checks = append(checks, ok("agent", "all referenced agents are registered ("+strings.Join(names, ", ")+")"))
	}
	return checks
}

// agentModel is one effective (agent, model) binding the config produces.
type agentModel struct{ agent, model string }

// referencedModels returns the distinct, sorted set of effective (agent, model)
// bindings the config produces: the top-level pair plus each workflow's effective
// pair (its own agent/model, each falling back to the top level). Bindings with
// no agent are dropped — an empty model is only meaningful relative to an agent
// that needs one.
func referencedModels(cfg taboo.ProjectConfig) []agentModel {
	seen := map[agentModel]struct{}{}
	var out []agentModel
	add := func(agent, model string) {
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
		add(orElse(wf.Agent, cfg.Agent), orElse(wf.Model, cfg.Model))
	}
	slices.SortFunc(out, func(a, b agentModel) int {
		if c := strings.Compare(a.agent, b.agent); c != 0 {
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
			checks = append(checks, fail("model/"+am.agent,
				"agent \""+am.agent+"\" has no model configured (model is required)"))
			continue
		}
		if ok, expected := taboo.MatchModelFormat(am.agent, am.model); !ok {
			checks = append(checks, warn("model/"+am.agent+"/"+am.model,
				"model \""+am.model+"\" does not look like a model for "+am.agent+" (expected "+
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
// work tree. An unset repo is a hard failure — validate runs against a real repo.
func repoValidateChecks(ctx context.Context, env Env, cfg taboo.ProjectConfig) []check {
	if cfg.Repo == "" {
		return []check{fail("repo", "no repo configured (set top-level repo:)")}
	}
	return []check{repoLocationCheck(cfg.Repo), repoGitCheck(ctx, env, cfg.Repo)}
}
