package agent

import "regexp"

// pi is the AgentProfile for the Pi CLI (earendil-works/pi), run against a single
// model.
type pi struct {
	model string
}

// NewPi returns the AgentProfile for the Pi CLI configured to run the given model.
func NewPi(model string) AgentProfile {
	return pi{model: model}
}

// Pi is the canonical name of the Pi agent; it equals the SDK dir baked into the
// workshop (internal/workshop/sdk/pi).
const Pi AgentName = "pi"

func (pi) Name() AgentName { return Pi }

// BuildCommand renders the verified Pi invocation: `pi -p --approve --model <m>`,
// with the prompt piped on stdin rather than in argv (ADR 0001).
//
// -p/--print is Pi's non-interactive mode ("print response and exit"). It emits
// the agent's plain final response, which keeps the orchestrator's
// completion-signal scan and <result>{…}</result> extraction working with no
// output parser — the same reason copilot pins `--output-format text`. Pi's
// richer `--mode json` (JSONL of every event) was rejected here: like claude-code's
// stream-json it would need a dedicated parser to reduce the captured stdout back
// to clean text, which is out of scope for this profile.
//
// --approve trusts the project for this one run. Non-interactive modes show no
// trust prompt and otherwise fall back to Pi's `defaultProjectTrust` (default
// `ask`), which leaves the project untrusted and would withhold project resources
// and tool execution from the headless agent — so it could never edit or commit.
// Taboo runs each agent in an isolated, ephemeral LXD workshop, so the workshop is
// the security boundary — the same trust-the-project posture under which OpenCode,
// Claude Code, and Copilot run their tools freely.
//
// Pi has no argv-level tool deny: its allow/deny rules (e.g. blocking `git push`,
// as claude-code's `--disallowedTools` and copilot's `--deny-tool` do) live in a
// permissions *policy file* (`--permissions-file`), which the argv+stdin
// BuildCommand contract cannot ship. So the git-push hard-deny the other profiles
// add is not expressible for Pi here; taboo relies on the workshop isolation
// boundary, and a policy-file deny is a clean follow-up if it is ever wanted.
//
// A resume id maps to `--session <id>` — the deterministic id-targeted flag (Pi's
// `-r/--resume` is an interactive picker, a trap; ADR 0003). A fork instead emits
// `--fork <id>`: Pi's fork flag *takes* the source id and writes the continuation
// to a new session file, leaving the source untouched, so it replaces --session
// rather than adding to it. Fork without a session to fork from is dropped (it
// degrades to taboo's filesystem-only isolation; ADR 0003).
func (a pi) BuildCommand(opts CommandOptions) AgentCommand {
	argv := []string{"pi", "-p", "--approve", "--model", a.model}
	if opts.ResumeSession != "" {
		if opts.Fork {
			argv = append(argv, "--fork", opts.ResumeSession)
		} else {
			argv = append(argv, "--session", opts.ResumeSession)
		}
	}
	// The prompt rides on stdin, never in argv (no positional to omit): an empty
	// prompt simply pipes empty stdin to `pi -p`.
	return AgentCommand{Argv: argv, Stdin: opts.Prompt}
}

// CredentialEnvKeys returns OPENROUTER_API_KEY, the env var Pi reads its OpenRouter
// credentials from (verified). A single key mirrors OpenCode's OpenRouter path and
// pairs with the provider/model hint below; the value travels only via
// `workshop exec --env NAME`, never in argv (ADR 0004). Pi can also authenticate
// from `~/.pi/agent/auth.json`, but supplying the key through --env means no such
// credential file is needed — and Sessions() relies on that (see below).
func (pi) CredentialEnvKeys() []string { return []string{"OPENROUTER_API_KEY"} }

// Sessions redirects Pi's session store by pointing PI_CODING_AGENT_SESSION_DIR at
// the mount (Pi defaults it to ~/.pi/agent/sessions/). Pi writes session files
// directly under that dir, organized into per-working-directory subdirs, so Subdir
// is empty — taboo's capture wiring drives DirEnv at the mount target and Pi owns
// the layout beneath it. Resume/fork read this store; it write-throughs the
// bind-mount and survives the per-run rootfs wipe.
//
// Unlike OpenCode's XDG_DATA_HOME or Copilot's COPILOT_HOME, this env var
// relocates ONLY the sessions dir, not Pi's whole config/auth home — so Pi's
// credential file (~/.pi/agent/auth.json) never lands on the host mount. And
// because auth is supplied through --env (see CredentialEnvKeys), Pi writes no
// auth.json at all. Do not pair this with interactive `pi /login`, which would
// persist credentials into Pi's home.
func (pi) Sessions() (SessionSpec, bool) {
	return SessionSpec{DirEnv: "PI_CODING_AGENT_SESSION_DIR", Subdir: ""}, true
}

// piHint warns when the model is not a provider/model slug (ADR 0008). Pi forwards
// OpenRouter credentials (see CredentialEnvKeys), and OpenRouter models are
// addressed provider-first — so a value with no leading provider segment (a bare
// `sonnet` or `gpt-4`) is almost certainly a mistake or implies a provider Pi is
// not credentialed for here. The pattern mirrors OpenCode, the other OpenRouter
// agent: one non-slash provider segment, a slash, then a non-empty remainder.
var piHint = modelHint{
	pattern:  regexp.MustCompile(`^[^/]+/.+$`),
	expected: "<provider>/<model>, e.g. openrouter/qwen/qwen3-coder-plus",
}
