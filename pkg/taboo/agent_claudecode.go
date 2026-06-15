package taboo

// claudeCode is the AgentProfile for the Claude Code CLI, run against a single model.
type claudeCode struct {
	model string
}

// ClaudeCode returns the AgentProfile for the Claude Code CLI configured to run
// the given model.
func ClaudeCode(model string) AgentProfile {
	return claudeCode{model: model}
}

func (claudeCode) Name() string { return "claude-code" }

// BuildCommand renders the verified Claude Code invocation: `claude -p
// --output-format text --model <m>`, with the prompt piped on stdin rather than
// in argv (ADR 0001). --output-format text keeps the agent's output literal so
// the orchestrator's completion-signal scan and the <result>{…}</result>
// extraction keep working; --output-format json would escape that block and
// break extraction.
func (a claudeCode) BuildCommand(opts CommandOptions) AgentCommand {
	argv := []string{"claude", "-p", "--output-format", "text", "--model", a.model}
	if opts.ResumeSession != "" {
		argv = append(argv, "--resume", opts.ResumeSession)
		// --fork-session only applies when continuing a session; it forks that
		// session into a new one so the source conversation is left untouched
		// (ADR 0003). Nested under resume so Fork without a session is dropped.
		if opts.Fork {
			argv = append(argv, "--fork-session")
		}
	}
	// The prompt rides on stdin, never in argv (no positional to omit): an empty
	// prompt simply pipes empty stdin to `claude -p`.
	return AgentCommand{Argv: argv, Stdin: opts.Prompt}
}

// CredentialEnvKeys returns both keys Claude Code accepts: ANTHROPIC_API_KEY for
// API users and CLAUDE_CODE_OAUTH_TOKEN (from `claude setup-token`) for
// subscription users (ADR 0004). Returning both needs no configuration branching:
// `workshop exec --env NAME` silently drops whichever key is unset on the host,
// so each user forwards exactly the one they hold; and when both are set, Claude
// Code's own precedence prefers the API key. The API key is listed first only to
// mirror that precedence — list order is otherwise cosmetic.
func (claudeCode) CredentialEnvKeys() []string {
	return []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"}
}

// Sessions redirects Claude Code's config dir by pointing CLAUDE_CONFIG_DIR at
// the mount; transcripts land under projects/<project>/<session>.jsonl. This is
// the only env var Claude exposes, and it captures the whole config dir
// (settings + history + credentials), not sessions alone — no finer-grained
// override exists.
//
// Safe here ONLY because auth is env-based (see CredentialEnvKeys): with the
// credentials supplied through `--env`, Claude writes no .credentials.json onto
// the host mount. Do not pair this redirect with interactive `claude /login`,
// which would persist a credentials file into the captured (host-bound) dir.
func (claudeCode) Sessions() (SessionSpec, bool) {
	return SessionSpec{DirEnv: "CLAUDE_CONFIG_DIR", Subdir: "projects"}, true
}
