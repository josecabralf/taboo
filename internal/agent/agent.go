package agent

import "io"

// AgentName is the canonical identity of a registered agent, a defined string
// type so the API exposes named constants the compiler checks.
type AgentName string

// AgentProfile names an agent's SDK environment and builds the exact invocation
// taboo runs in the workshop, one implementation per supported agent. Keep the
// interface minimal; see docs/adr/0001-agentprofile-argv-stdin-command-contract.md.
type AgentProfile interface {
	// Name is the agent identity, equal to the workshop SDK name and remount qualifier.
	Name() AgentName
	// BuildCommand renders the agent invocation for a single run.
	BuildCommand(CommandOptions) AgentCommand
	// CredentialEnvKeys are host env var names whose values reach the agent via
	// `workshop exec --env NAME` (never argv).
	CredentialEnvKeys() []string
	// Sessions reports where the agent persists session state; ok is false when it
	// has none. taboo points DirEnv at a host mount so session files survive the
	// per-run rootfs wipe.
	Sessions() (SessionSpec, bool)
}

// OutputParser is an optional AgentProfile capability: the runner applies
// ParseOutput to captured stdout so RunResult.Output is the agent's clean final
// text. Kept off AgentProfile and asserted so only agents whose stdout differs
// from that text opt in (today just Claude Code, whose stream-json interleaves
// tool calls).
type OutputParser interface {
	ParseOutput(raw string) string
}

// OutputRenderer is an optional AgentProfile capability: the runner wraps the
// display sink in Render so the workflow log shows a readable transcript, leaving
// the captured buffer behind RunResult.Output untouched. Asserted, not on
// AgentProfile; today only Claude Code opts in (its stream-json JSONL), while
// OpenCode and Copilot already stream readable output.
type OutputRenderer interface {
	Render(w io.Writer) io.Writer
}

// CommandOptions is the agent-agnostic input each profile maps onto its own flags.
type CommandOptions struct {
	// Prompt is the agent's instruction for this run.
	Prompt string
	// ResumeSession, if set, continues that prior session by id; empty starts fresh.
	ResumeSession string
	// Fork, with ResumeSession, forks that session into a new one instead of
	// appending, leaving the source untouched. Ignored without ResumeSession, and a
	// no-op when the agent's CLI has no native fork.
	Fork bool
}

// AgentCommand is the invocation taboo execs: Argv is the command and args; a
// non-empty Stdin is piped to the agent instead of carrying the prompt in argv.
// Named AgentCommand, not Command, to stay distinct from the host-process Cmd in
// commander.go.
type AgentCommand struct {
	Argv  []string
	Stdin string
}

// SessionSpec locates an agent's on-disk session store: Subdir under the
// directory named by the DirEnv environment variable. Capture wiring points
// DirEnv at the sessions mount; the agent writes under DirEnv/Subdir.
type SessionSpec struct {
	DirEnv string
	Subdir string
}

// Concrete profiles live one-per-file (agent_<name>.go); add a new agent as a new
// file rather than growing this one.
