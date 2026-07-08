package agent

import "regexp"

// openCode is the AgentProfile for the OpenCode CLI.
type openCode struct {
	model string
}

// NewOpenCode returns the OpenCode AgentProfile configured for model.
func NewOpenCode(model string) AgentProfile {
	return openCode{model: model}
}

// OpenCode is the canonical name of the OpenCode agent.
const OpenCode AgentName = "opencode"

func (openCode) Name() AgentName { return OpenCode }

// BuildCommand renders the OpenCode invocation with the prompt positional in
// argv. --log-level ERROR keeps OpenCode's chatter off the captured output. A
// resume id maps to `--session <id>`, with `--fork` on top (ADR 0003).
func (a openCode) BuildCommand(opts CommandOptions) AgentCommand {
	argv := []string{"opencode", "run", "--log-level", "ERROR", "-m", a.model}
	if opts.ResumeSession != "" {
		argv = append(argv, "--session", opts.ResumeSession)
		// --fork applies only when continuing a session, forking it into a new one
		// so the source is left untouched.
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

// Sessions points XDG_DATA_HOME at the mount, capturing OpenCode's whole data dir
// (opencode.db + WAL sidecars, snapshots, logs), not sessions alone. Resume/fork
// read that DB; it write-throughs the bind mount and survives the per-run swap.
// Safe because OpenCode authenticates from OPENROUTER_API_KEY in the env, so no
// credential file lands on the host mount.
func (openCode) Sessions() (SessionSpec, bool) {
	return SessionSpec{DirEnv: "XDG_DATA_HOME", Subdir: "opencode"}, true
}

// openCodeHint warns when the model is not a <provider>/<model> slug (ADR 0008):
// OpenCode addresses every model that way, so a bare id is almost certainly a
// mistake. The pattern requires one provider segment, a slash, then a non-empty
// remainder.
var openCodeHint = modelHint{
	pattern:  regexp.MustCompile(`^[^/]+/.+$`),
	expected: "<provider>/<model>, e.g. openrouter/qwen/qwen3-coder-plus",
}
