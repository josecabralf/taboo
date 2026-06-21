package agent

// gitHubCopilot is the AgentProfile for the GitHub Copilot CLI, run against a single model.
type gitHubCopilot struct {
	model string
}

// NewGitHubCopilot returns the AgentProfile for the GitHub Copilot CLI configured
// to run the given model.
func NewGitHubCopilot(model string) AgentProfile {
	return gitHubCopilot{model: model}
}

// GitHubCopilot is the canonical name of the GitHub Copilot agent. The argv it
// builds still invokes the real `copilot` binary; only taboo's identity for the
// agent is github-copilot.
const GitHubCopilot AgentName = "github-copilot"

func (gitHubCopilot) Name() AgentName { return GitHubCopilot }

// BuildCommand renders the verified GitHub Copilot CLI invocation:
// `copilot --model <m> --allow-all --deny-tool=shell(git push) --output-format
// text -s -p <prompt>`, with the prompt delivered as the value of -p (ADR 0001),
// never on stdin.
//
// -p/--prompt both selects non-interactive mode and carries the prompt (as its
// value, never on stdin; ADR 0001): omitting it would drop copilot into an
// interactive TUI that hangs, so -p is always emitted, even empty. Copilot then
// requires a non-empty value — an empty one exits 1 ("No prompt provided") — so a
// "just continue" resume (empty Prompt) is not a valid Copilot run. Like the other
// profiles BuildCommand does not guard this; the caller must supply a prompt even
// when resuming, and an empty prompt is still emitted faithfully as `-p ""`.
//
// --allow-all (= --allow-all-tools --allow-all-paths --allow-all-urls) is
// required for non-interactive mode: a `-p` run has no interactive approver, so
// without it every tool call would block and the agent could never edit or
// commit. Each agent runs in an isolated, ephemeral LXD workshop, so the
// workshop is the security boundary — the same allow-broadly posture under which
// OpenCode and Claude Code run their tools. --allow-all-paths matters here
// specifically because the worktree's commits land in the repo's main `.git`,
// mounted at its host absolute path, outside the agent's workspace mount.
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
// unambiguously and never risks the picker (verified, copilot 1.0.22). The resume
// flag precedes the -p prompt.
//
// Copilot has no native headless fork (1.0.22; ADR 0003), so Fork is intentionally
// not consulted here — a documented no-op; see TestCopilot_BuildCommand_ForkIsNoOp.
func (a gitHubCopilot) BuildCommand(opts CommandOptions) AgentCommand {
	argv := []string{
		"copilot", "--model", a.model,
		"--allow-all", "--deny-tool=shell(git push)",
		"--output-format", "text", "-s",
	}
	if opts.ResumeSession != "" {
		argv = append(argv, "--resume="+opts.ResumeSession)
	}
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
func (gitHubCopilot) CredentialEnvKeys() []string {
	return []string{"COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"}
}

// Sessions redirects Copilot's whole config+state directory by pointing
// COPILOT_HOME at the mount (it defaults to $HOME/.copilot); session transcripts
// land under session-state/. COPILOT_HOME is the only env var copilot exposes to
// relocate this directory, and it captures the entire home (config.json, logs,
// command history, sessions), not sessions alone. (The --config-dir flag does the
// same, but taboo's capture wiring drives an env var, not argv.)
//
// Safe here ONLY because auth is env-based (see CredentialEnvKeys): with the
// GitHub token supplied through --env, copilot writes no stored-credential file
// onto the host mount. Do not pair this redirect with interactive `copilot login`,
// which would persist credentials into the captured (host-bound) home.
func (gitHubCopilot) Sessions() (SessionSpec, bool) {
	return SessionSpec{DirEnv: "COPILOT_HOME", Subdir: "session-state"}, true
}

// copilotHint is deliberately the no-opinion hint (nil pattern; ADR 0008).
// Copilot is a proxy that runs models from many providers — gpt-*, claude-*,
// gemini-*, the o-series — so there is no single well-formed shape to check, and
// any format heuristic would warn on valid configs. The validate command
// therefore never warns on a copilot model; the empty modelHint{} makes that
// intent explicit rather than leaving a bare zero value to read as an oversight.
var copilotHint = modelHint{}
