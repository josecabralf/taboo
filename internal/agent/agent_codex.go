package agent

import "regexp"

// codex is the AgentProfile for the Codex CLI.
type codex struct {
	model string
}

// NewCodex returns the Codex AgentProfile configured for model.
func NewCodex(model string) AgentProfile {
	return codex{model: model}
}

// Codex is the canonical name of the Codex agent; it matches the baked SDK dir
// internal/workshop/sdk/codex.
const Codex AgentName = "codex"

func (codex) Name() AgentName { return Codex }

// BuildCommand renders the Codex non-interactive `exec` run with the prompt on
// stdin (ADR 0001).
//
// The prompt rides on stdin: Codex's `exec` prompt is a positional that reads
// stdin when it is the `-` sentinel, so argv ends in `-` and Stdin carries the
// prompt. `-` is kept even for an empty prompt so a "just continue" resume never
// drops into the interactive TUI. `codex exec` streams progress to stderr and
// prints only the final agent message to stdout as plain text, so the <result>
// block reaches stdout literally — no --json (which would emit JSONL and break
// the extraction) and no OutputParser, mirroring copilot's --output-format text.
//
// --dangerously-bypass-approvals-and-sandbox runs fully autonomous: there is no
// human approver headless, and the workspace-write sandbox would block git commit
// (the shared object store lives in the parent repo mount, outside cwd — ADR
// 0011). The ephemeral LXD workshop is the security boundary, as with claude's
// --permission-mode auto and copilot's --allow-all. Unlike those, Codex exposes
// no per-command deny flag, so their git-push hard-deny degrades to the workshop
// boundary here; taboo commits in place and never pushes.
//
// A resume maps to the `exec resume <id>` subcommand (ADR 0003); --model and the
// bypass flag are global args, so they remain valid after the subcommand. Codex
// has no native headless fork (its fork is TUI-only), so Fork is not consulted:
// isolation degrades to the fresh branch/worktree taboo already allocates.
func (a codex) BuildCommand(opts CommandOptions) AgentCommand {
	argv := []string{"codex", "exec"}
	if opts.ResumeSession != "" {
		argv = append(argv, "resume", opts.ResumeSession)
	}
	argv = append(argv, "--dangerously-bypass-approvals-and-sandbox", "--model", a.model, "-")
	return AgentCommand{Argv: argv, Stdin: opts.Prompt}
}

// CredentialEnvKeys returns OPENAI_API_KEY, Codex's env-based credential.
// ChatGPT-subscription auth is the interactive `codex login` flow (writes
// auth.json under CODEX_HOME) and is unusable headless, so it is excluded; the
// key never enters argv (ADR 0004).
func (codex) CredentialEnvKeys() []string { return []string{"OPENAI_API_KEY"} }

// Sessions points CODEX_HOME at the mount (defaults to ~/.codex); session JSONL
// lands under sessions/. It captures the whole CODEX_HOME, not sessions alone.
//
// Safe ONLY because auth is env-based (see CredentialEnvKeys): with OPENAI_API_KEY
// set, Codex writes no auth.json onto the host mount. Do not pair with interactive
// `codex login`, which would persist credentials into the host-bound CODEX_HOME.
func (codex) Sessions() (SessionSpec, bool) {
	return SessionSpec{DirEnv: "CODEX_HOME", Subdir: "sessions"}, true
}

// codexHint warns when the model is not an OpenAI-family id (ADR 0008): Codex
// addresses OpenAI models by bare id, so a Claude id or an OpenCode provider slug
// is almost certainly a mistake. The pattern accepts gpt-* and codex-* ids and the
// o-series (o1/o3/o4-…), case-insensitively.
var codexHint = modelHint{
	pattern:  regexp.MustCompile(`(?i)gpt|codex|^o[0-9]`),
	expected: "an OpenAI model id, e.g. gpt-5-codex or o4-mini",
}
