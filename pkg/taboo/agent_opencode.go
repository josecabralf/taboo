package taboo

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
	// The prompt rides positionally last, after every flag. Omit it when empty so
	// a resume with no new instruction ("just continue") does not pass a stray
	// empty positional argument to the agent.
	if opts.Prompt != "" {
		argv = append(argv, opts.Prompt)
	}
	return AgentCommand{Argv: argv}
}

func (openCode) CredentialEnvKeys() []string { return []string{"OPENROUTER_API_KEY"} }

// Sessions redirects OpenCode's data store by pointing XDG_DATA_HOME at the
// mount. This captures OpenCode's whole data dir (sessions plus its SQLite
// "channel db" and anything else it keeps under XDG_DATA_HOME), not sessions
// alone. Safe here because OpenCode authenticates from OPENROUTER_API_KEY in the
// env, so no credential file lands on the host mount; weigh this before adding
// an agent that keeps file-based secrets under its session-dir env var.
func (openCode) Sessions() (SessionSpec, bool) {
	return SessionSpec{DirEnv: "XDG_DATA_HOME", Subdir: "opencode"}, true
}
