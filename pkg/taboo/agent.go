package taboo

// AgentProfile is taboo's per-agent abstraction: it names the agent's SDK
// environment and builds the exact invocation taboo runs inside the workshop.
// Each supported agent (OpenCode now; Claude/Codex/Pi/Copilot/Cursor later) is
// one implementation. The interface is reviewed by hand because fan-out and
// sessions build on it; see docs/adr/0001-agentprofile-argv-stdin-command-contract.md.
type AgentProfile interface {
	// Name is the agent identity: it equals the SDK name baked into the
	// workshop and the remount qualifier used for the mount plugs.
	Name() string
	// BuildCommand renders the agent invocation for a single run.
	BuildCommand(CommandOptions) AgentCommand
	// CredentialEnvKeys are host environment variable names whose values reach
	// the agent via `workshop exec --env NAME` (the value never enters argv).
	CredentialEnvKeys() []string
	// Sessions reports where the agent persists session state. ok is false for
	// agents with no session store. taboo uses it to render a sessions mount
	// plug, bind a host sessions directory into the workshop, and point DirEnv at
	// the mount target so session files survive the per-run rootfs wipe.
	Sessions() (SessionSpec, bool)
}

// CommandOptions is the input to AgentProfile.BuildCommand. This slice carries
// only Prompt; the sessions slice adds ResumeSession/ForkSession non-breakingly.
type CommandOptions struct {
	// Prompt is the agent's instruction for this run.
	Prompt string
}

// AgentCommand is the agent invocation taboo execs. Argv is the command and its
// arguments; when Stdin is non-empty the runner pipes it to the agent's stdin
// instead of carrying the prompt in argv (the stdin-delivery agents). It is
// named AgentCommand, not Command, to stay distinct from the host-process Cmd in
// commander.go.
type AgentCommand struct {
	Argv  []string
	Stdin string
}

// SessionSpec locates an agent's on-disk session store: Subdir under the
// directory named by the DirEnv environment variable (e.g. XDG_DATA_HOME).
//
// The capture wiring uses only DirEnv: taboo points DirEnv at the sessions
// mount target and the agent itself writes under DirEnv/Subdir. Subdir is the
// agent-relative path to those files (mount source + Subdir), consumed when
// locating the store from the outside — the integration test today, and the
// deferred resume/fork slice — not by the env redirect.
type SessionSpec struct {
	DirEnv string
	Subdir string
}

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
// chatter off the captured agent output.
func (a openCode) BuildCommand(opts CommandOptions) AgentCommand {
	return AgentCommand{
		Argv: []string{"opencode", "run", "--log-level", "ERROR", "-m", a.model, opts.Prompt},
	}
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
