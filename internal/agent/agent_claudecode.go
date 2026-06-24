package agent

import (
	"io"
	"regexp"

	"github.com/josecabralf/taboo/internal/agent/claudestream"
)

// claudeCode is the AgentProfile for the Claude Code CLI, run against a single model.
type claudeCode struct {
	model string
}

// NewClaudeCode returns the AgentProfile for the Claude Code CLI configured to run
// the given model.
func NewClaudeCode(model string) AgentProfile {
	return claudeCode{model: model}
}

// ClaudeCode is the canonical name of the Claude Code agent.
const ClaudeCode AgentName = "claude-code"

func (claudeCode) Name() AgentName { return ClaudeCode }

// BuildCommand renders the verified Claude Code invocation: `claude -p
// --output-format stream-json --verbose --model <m> --permission-mode auto
// --disallowedTools "Bash(git push *)"`, with the prompt piped on stdin rather
// than in argv (ADR 0001).
//
// --output-format stream-json --verbose is what surfaces the agent's full
// activity in the workflow log: it emits JSONL — one event per line for
// assistant text and every tool call — instead of `text`'s final-message-only
// output, matching the visibility OpenCode's run already has. (--verbose is
// mandatory with stream-json in -p mode.) The raw JSONL streams live to the
// caller's Stdout; what the orchestrator scans is decoupled from it. The catch
// `text` avoided — that raw JSONL would break the completion-signal scan and the
// <result>{…}</result> extraction — is handled at the capture seam, not here:
// ParseOutput (the runner's optional OutputParser) reduces the captured JSONL
// back to the clean final text via claudestream.ResultText, so RunResult.Output
// is exactly what `text` used to give. That is the invariant: res.Output is
// always the agent's clean final text; display is a separate concern. Plain
// --output-format json was rejected for the same reason text was kept — it
// JSON-escapes the <result> block — and it never streamed intermediate activity
// anyway.
//
// --permission-mode auto lets the headless agent edit files and commit
// autonomously (there is no interactive approver in `claude -p`; the default
// mode would gate Write/Edit/Bash and the agent could never commit). Taboo runs
// each agent in an isolated, ephemeral LXD container, so the container is the
// security boundary — the same posture under which OpenCode runs its tools
// freely.
//
// --disallowedTools "Bash(git push *)" is a hard deny (deny outranks auto's
// classifier) on every push form: the single `*` spans all arguments, so bare
// `git push`, `git push origin main`, and `--force`/`-f` in any position are all
// blocked. The deny is deliberate — a linked worktree shares the host repo's
// object store and refs, so a push from inside the workshop could mutate host
// branches. Taboo's contract is commit-in-place; the host owns integration, so
// the agent never needs to push. Automations that must publish add their own
// host-side push stage (see CONTEXT.md).
func (a claudeCode) BuildCommand(opts CommandOptions) AgentCommand {
	argv := []string{"claude", "-p", "--output-format", "stream-json", "--verbose", "--model", a.model,
		"--permission-mode", "auto", "--disallowedTools", "Bash(git push *)"}
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

// ParseOutput reduces Claude Code's raw stream-json stdout to the agent's clean
// final text, implementing the runner's optional OutputParser interface. The
// runner applies it at the capture seam so RunResult.Output is the unescaped
// result text — what the completion-signal scan and <result>{…}</result>
// extraction expect — while the raw JSONL still streams live to the workflow
// log. Agents that don't implement OutputParser (OpenCode, Copilot) keep their
// stdout verbatim; only Claude Code, whose stdout interleaves tool calls, opts
// in. See claudestream.ResultText for the no-result-line fallback.
func (claudeCode) ParseOutput(raw string) string {
	return claudestream.ResultText(raw)
}

// Render wraps the live display sink in claudestream's transcript renderer,
// implementing the runner's optional OutputRenderer interface. Claude Code's raw
// stdout is stream-json JSONL; Render turns it into a readable transcript of
// assistant text and tool calls for the workflow log, matching the visibility
// OpenCode's plain transcript already has. It touches only the display path — the
// captured buffer behind RunResult.Output keeps the raw JSONL that ParseOutput
// reduces — so display and scan remain separate concerns. See
// claudestream.NewRenderer for the event schema and partial-line buffering.
func (claudeCode) Render(w io.Writer) io.Writer {
	return claudestream.NewRenderer(w)
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
// the mount; transcripts land under projects/<project>/<session>.jsonl.
// CLAUDE_CONFIG_DIR is the only relocation env var Claude exposes, and it
// captures the whole config dir
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

// claudeCodeHint warns when the model is not a Claude model id or family alias
// (ADR 0008). It accepts any value containing "claude" (case-insensitively) —
// covering ids like claude-sonnet-4-6 and Bedrock/Vertex forms like
// anthropic.claude-3-5-sonnet — or one starting with a bare family alias
// (sonnet/opus/haiku). A foreign id (gpt-*, an OpenCode provider slug) matches
// neither and warns. The match is anchored only for the aliases; "claude" may
// appear anywhere so vendor-prefixed ids still pass.
var claudeCodeHint = modelHint{
	pattern:  regexp.MustCompile(`(?i)claude|^(sonnet|opus|haiku)`),
	expected: "a Claude model id or family alias, e.g. claude-sonnet-4-6 or sonnet",
}
