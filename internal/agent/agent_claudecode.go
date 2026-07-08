package agent

import (
	"io"
	"regexp"

	"github.com/josecabralf/taboo/internal/agent/claudestream"
)

// claudeCode is the AgentProfile for the Claude Code CLI.
type claudeCode struct {
	model string
}

// NewClaudeCode returns the Claude Code AgentProfile configured for model.
func NewClaudeCode(model string) AgentProfile {
	return claudeCode{model: model}
}

// ClaudeCode is the canonical name of the Claude Code agent.
const ClaudeCode AgentName = "claude-code"

func (claudeCode) Name() AgentName { return ClaudeCode }

// BuildCommand renders the Claude Code invocation with the prompt on stdin (ADR 0001).
//
// --output-format stream-json --verbose emits JSONL (one event per line for
// assistant text and tool calls) so the workflow log shows full activity;
// --verbose is mandatory with stream-json in -p mode. ParseOutput reduces the
// captured JSONL back to the clean final text, so RunResult.Output matches what
// --output-format text gave. Plain json was rejected because it JSON-escapes the
// <result> block.
//
// --permission-mode auto lets the headless agent edit and commit without an
// interactive approver; the ephemeral LXD container is the security boundary.
//
// --disallowedTools "Bash(git push *)" hard-denies every push form (deny outranks
// auto). The deny is deliberate: a linked worktree shares the host repo's object
// store and refs, so a push could mutate host branches. Taboo commits in place;
// the host owns integration.
func (a claudeCode) BuildCommand(opts CommandOptions) AgentCommand {
	argv := []string{"claude", "-p", "--output-format", "stream-json", "--verbose", "--model", a.model,
		"--permission-mode", "auto", "--disallowedTools", "Bash(git push *)"}
	if opts.ResumeSession != "" {
		argv = append(argv, "--resume", opts.ResumeSession)
		// --fork-session applies only when resuming; nested under resume so Fork
		// without a session is dropped (ADR 0003).
		if opts.Fork {
			argv = append(argv, "--fork-session")
		}
	}
	// The prompt rides on stdin; an empty prompt pipes empty stdin.
	return AgentCommand{Argv: argv, Stdin: opts.Prompt}
}

// ParseOutput reduces Claude Code's stream-json stdout to the clean final text
// (OutputParser). See claudestream.ResultText.
func (claudeCode) ParseOutput(raw string) string {
	return claudestream.ResultText(raw)
}

// Render wraps the display sink in claudestream's transcript renderer
// (OutputRenderer), touching only the display path. See claudestream.NewRenderer.
func (claudeCode) Render(w io.Writer) io.Writer {
	return claudestream.NewRenderer(w)
}

// CredentialEnvKeys returns both keys Claude Code accepts: ANTHROPIC_API_KEY and
// CLAUDE_CODE_OAUTH_TOKEN (ADR 0004). `workshop exec --env NAME` drops whichever
// is unset, so no config branching is needed; the API key is first to mirror
// Claude's own precedence when both are set.
func (claudeCode) CredentialEnvKeys() []string {
	return []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"}
}

// Sessions points CLAUDE_CONFIG_DIR at the mount; transcripts land under
// projects/. It is the only relocation env var Claude exposes and captures the
// whole config dir, not sessions alone.
//
// Safe ONLY because auth is env-based (see CredentialEnvKeys): Claude writes no
// .credentials.json onto the host mount. Do not pair with interactive
// `claude /login`, which would persist credentials into the host-bound dir.
func (claudeCode) Sessions() (SessionSpec, bool) {
	return SessionSpec{DirEnv: "CLAUDE_CONFIG_DIR", Subdir: "projects"}, true
}

// claudeCodeHint warns when the model is not a Claude id or family alias (ADR
// 0008). It accepts any value containing "claude" (so vendor-prefixed ids like
// anthropic.claude-3-5-sonnet pass) or one starting with sonnet/opus/haiku.
var claudeCodeHint = modelHint{
	pattern:  regexp.MustCompile(`(?i)claude|^(sonnet|opus|haiku)`),
	expected: "a Claude model id or family alias, e.g. claude-sonnet-4-6 or sonnet",
}
