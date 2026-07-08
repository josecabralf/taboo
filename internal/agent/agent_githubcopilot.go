package agent

// gitHubCopilot is the AgentProfile for the GitHub Copilot CLI.
type gitHubCopilot struct {
	model string
}

// NewGitHubCopilot returns the Copilot AgentProfile configured for model.
func NewGitHubCopilot(model string) AgentProfile {
	return gitHubCopilot{model: model}
}

// GitHubCopilot is taboo's identity for the Copilot agent; the argv it builds
// still invokes the real `copilot` binary.
const GitHubCopilot AgentName = "github-copilot"

func (gitHubCopilot) Name() AgentName { return GitHubCopilot }

// BuildCommand renders the Copilot invocation with the prompt as the value of -p
// (ADR 0001), never on stdin.
//
// -p selects non-interactive mode and carries the prompt; omitting it drops
// copilot into a hanging TUI, so it is always emitted. Copilot rejects an empty
// value, so a resume still needs a non-empty prompt; BuildCommand does not guard
// this.
//
// --allow-all is required in non-interactive mode (no approver), so tool calls
// don't block; the ephemeral LXD workshop is the security boundary.
// --allow-all-paths matters because commits land in the host repo's `.git`,
// mounted outside the agent's workspace.
//
// --deny-tool=shell(git push) is the one hard deny (deny outranks --allow-all,
// verified copilot 1.0.22); it blocks every push form. A linked worktree shares
// the host repo's object store, so a push could mutate host branches; taboo
// commits in place. Mirrors claude-code's `--disallowedTools "Bash(git push *)"`.
//
// --output-format text keeps output literal so the <result> extraction works
// (json emits JSONL and breaks it); it is the default, pinned defensively.
// -s drops run stats.
//
// --resume=<id> uses the =-attached form: a bare --resume opens the interactive
// picker, so attaching the id with `=` targets the session unambiguously
// (verified copilot 1.0.22).
//
// Copilot has no native headless fork (ADR 0003), so Fork is not consulted here.
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

// CredentialEnvKeys returns the three token env vars Copilot reads, in copilot's
// precedence order (COPILOT_GITHUB_TOKEN > GH_TOKEN > GITHUB_TOKEN). `workshop
// exec --env NAME` drops those unset, so no config branching; the token never
// enters argv (ADR 0004). BYOK custom-provider vars are excluded: they select a
// different run mode, not a credential for the default GitHub path.
func (gitHubCopilot) CredentialEnvKeys() []string {
	return []string{"COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"}
}

// Sessions points COPILOT_HOME at the mount (defaults to $HOME/.copilot);
// transcripts land under session-state/. It is the only relocation env var
// copilot exposes and captures the whole home, not sessions alone.
//
// Safe ONLY because auth is env-based (see CredentialEnvKeys): copilot writes no
// stored-credential file onto the host mount. Do not pair with interactive
// `copilot login`, which would persist credentials into the host-bound home.
func (gitHubCopilot) Sessions() (SessionSpec, bool) {
	return SessionSpec{DirEnv: "COPILOT_HOME", Subdir: "session-state"}, true
}

// copilotHint is the no-opinion hint (nil pattern; ADR 0008): copilot proxies
// many providers' models, so there is no single shape to check and validate never
// warns on a copilot model.
var copilotHint = modelHint{}
