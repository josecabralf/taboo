package taboo

// copilot is the AgentProfile for the GitHub Copilot CLI, run against a single model.
type copilot struct {
	model string
}

// Copilot returns the AgentProfile for the GitHub Copilot CLI configured to run
// the given model.
func Copilot(model string) AgentProfile {
	return copilot{model: model}
}

func (copilot) Name() string { return "copilot" }

// BuildCommand renders the verified GitHub Copilot CLI invocation:
// `copilot --model <m> --allow-all --deny-tool=shell(git push) --output-format
// text -s -p <prompt>`, with the prompt delivered as the value of -p (ADR 0001),
// never on stdin.
//
// -p/--prompt is what makes the run non-interactive: without it copilot starts an
// interactive TUI and would hang, so the prompt is always emitted as the -p value
// (never on stdin; ADR 0001). Copilot requires a non-empty prompt for a -p run —
// an empty value makes it exit 1 ("No prompt provided"), so a "just continue"
// resume (empty Prompt) is not a valid Copilot run. Like the other profiles
// BuildCommand does not guard this; the caller must supply a prompt even when
// resuming. An empty prompt is still emitted faithfully as `-p ""`, because
// omitting -p would launch the hanging TUI instead of that fast, clear exit 1.
//
// --allow-all (= --allow-all-tools --allow-all-paths --allow-all-urls) is
// required for non-interactive mode: a `-p` run has no interactive approver, so
// without it every tool call would block and the agent could never edit or
// commit. Each agent runs in an isolated, ephemeral LXD workshop, so the
// workshop is the security boundary — the same allow-broadly posture under which
// OpenCode and Claude Code run their tools. --allow-all-paths matters here
// specifically because the worktree's commits land in the repo's main `.git`,
// mounted at its host absolute path *outside* /workspace.
//
// --deny-tool=shell(git push) is the one hard deny. Denial rules always take
// precedence over allow rules, even --allow-all (verified, copilot 1.0.22), and
// copilot approves shell commands on a first-level subcommand basis, so this one
// pattern blocks every `git push` form (bare, `origin main`, `--force`). The deny
// is deliberate — a linked worktree shares the host repo's object store and refs,
// so a push from inside the workshop could mutate host branches, and taboo's
// contract is commit-in-place; the host owns integration, so the agent never pushes
// (mirrors claude-code's `--disallowedTools "Bash(git push *)"`).
//
// --output-format text keeps the agent's output literal so the orchestrator's
// completion-signal scan and the <result>{…}</result> extraction keep working
// (--output-format json emits JSONL and would break extraction); text is the
// default but is pinned defensively. -s/--silent drops copilot's run stats so
// only the agent response reaches the captured stream.
//
// A resume id is bound with the =-attached `--resume=<id>`, the form the CLI
// documents (`copilot --resume=<session-id>`). `--resume[=sessionId]` is a
// commander.js optional-value option: a bare `--resume` opens the interactive
// session picker, and the space form binds an id only when the following token is
// not option-like — so attaching the id with `=` targets the session
// unambiguously and never risks the picker (verified, copilot 1.0.22). Copilot has
// no native headless fork (1.0.22; ADR 0003), so Fork is a documented no-op here —
// it is intentionally not consulted, and session-level fork degrades to taboo's
// filesystem-only isolation (fork = resume onto a fresh branch+worktree). The
// resume flag precedes the -p prompt.
func (a copilot) BuildCommand(opts CommandOptions) AgentCommand {
	argv := []string{
		"copilot", "--model", a.model,
		"--allow-all", "--deny-tool=shell(git push)",
		"--output-format", "text", "-s",
	}
	if opts.ResumeSession != "" {
		argv = append(argv, "--resume="+opts.ResumeSession)
	}
	// The prompt rides as the value of -p, which also selects non-interactive mode;
	// it is always emitted (even empty) because omitting -p would start the TUI.
	argv = append(argv, "-p", opts.Prompt)
	return AgentCommand{Argv: argv}
}

// CredentialEnvKeys returns the three env vars Copilot reads a GitHub auth token
// from, in copilot's own documented precedence order (COPILOT_GITHUB_TOKEN >
// GH_TOKEN > GITHUB_TOKEN). Returning all three needs no configuration branching:
// `workshop exec --env NAME` silently drops whichever are unset on the host, so
// each user forwards exactly the token they hold; when several are set, copilot's
// precedence picks the first present. The token value travels only via --env,
// never in argv (ADR 0004).
//
// BYOK custom-provider auth (COPILOT_PROVIDER_API_KEY / _BEARER_TOKEN, paired
// with COPILOT_PROVIDER_BASE_URL/_TYPE) is deliberately not listed: it selects a
// different run mode (a non-GitHub model endpoint), not a credential for the
// default GitHub Copilot path this profile targets.
func (copilot) CredentialEnvKeys() []string {
	return []string{"COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"}
}

// Sessions redirects Copilot's whole config+state directory by pointing
// COPILOT_HOME at the mount (it defaults to $HOME/.copilot); session transcripts
// land under session-state/. COPILOT_HOME is the only env var copilot exposes to
// relocate this directory — the --config-dir flag does the same but taboo's
// capture wiring drives an env var, not argv — and it captures the entire home
// (config.json, logs, command history, sessions), not sessions alone.
//
// Safe here ONLY because auth is env-based (see CredentialEnvKeys): with the
// GitHub token supplied through --env, copilot writes no stored-credential file
// onto the host mount. Do not pair this redirect with interactive `copilot login`,
// which would persist credentials into the captured (host-bound) home.
func (copilot) Sessions() (SessionSpec, bool) {
	return SessionSpec{DirEnv: "COPILOT_HOME", Subdir: "session-state"}, true
}

// copilotHint is Copilot's model-format hint for the agent registry (ADR 0005):
// registry-table metadata co-located with the profile, kept off the
// deliberately-minimal AgentProfile interface (ADR 0001). It is a deferred-type
// placeholder for this slice — see modelHint (registry.go).
var copilotHint modelHint
