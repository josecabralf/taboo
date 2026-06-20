package agent

import "regexp"

// openCode is the AgentProfile for the OpenCode CLI, run against a single model.
type openCode struct {
	model string
}

// OpenCode returns the AgentProfile for the OpenCode CLI configured to run the
// given model.
func OpenCode(model string) AgentProfile {
	return openCode{model: model}
}

func (openCode) Name() string { return "opencode" }

// BuildCommand renders the proven OpenCode invocation: the prompt rides
// positionally in argv (Stdin empty). --log-level ERROR keeps OpenCode's own
// chatter off the captured agent output. A resume id maps to `--session <id>`,
// and a fork adds `--fork` on top of it (see ADR 0003); both precede the prompt.
func (a openCode) BuildCommand(opts CommandOptions) AgentCommand {
	argv := []string{"opencode", "run", "--log-level", "ERROR", "-m", a.model}
	if opts.ResumeSession != "" {
		argv = append(argv, "--session", opts.ResumeSession)
		// OpenCode's --fork only applies when continuing a session; it forks that
		// session into a new one so the source conversation is left untouched.
		if opts.Fork {
			argv = append(argv, "--fork")
		}
	}
	if opts.Prompt != "" {
		argv = append(argv, opts.Prompt)
	}
	return AgentCommand{Argv: argv}
}

func (openCode) CredentialEnvKeys() []string { return []string{"OPENROUTER_API_KEY"} }

// Sessions redirects OpenCode's data store by pointing XDG_DATA_HOME at the
// mount. This captures OpenCode's whole data dir, not sessions alone: the
// installed binary keeps its session transcript in a single SQLite DB
// (opencode.db + WAL sidecars under XDG_DATA_HOME/opencode), plus snapshots,
// logs, and a per-run git "repos" tree. Resume/fork read that DB; it
// write-throughs the bind-mount and survives the per-run swap cleanly — proven
// end-to-end by TestIntegration_OpenCodeResumeFork. Safe here because OpenCode
// authenticates from OPENROUTER_API_KEY in the env, so no credential file lands
// on the host mount; weigh this before adding an agent that keeps file-based
// secrets under its session-dir env var.
func (openCode) Sessions() (SessionSpec, bool) {
	return SessionSpec{DirEnv: "XDG_DATA_HOME", Subdir: "opencode"}, true
}

// openCodeHint warns when the model is not an OpenCode provider/model slug
// (ADR 0008). OpenCode addresses every model as `<provider>/<model>` (it routes
// through OpenRouter and friends), so a value with no leading provider segment —
// e.g. a bare `gpt-4` or `claude-sonnet-4-6` — is almost certainly a mistake.
// The pattern requires one non-slash provider segment, a slash, then a non-empty
// remainder (which may itself contain slashes, as in openrouter/qwen/...).
var openCodeHint = modelHint{
	pattern:  regexp.MustCompile(`^[^/]+/.+$`),
	expected: "<provider>/<model>, e.g. openrouter/qwen/qwen3-coder-plus",
}
