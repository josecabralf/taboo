package agent

import "io"

// AgentName is the canonical identity of a registered agent. It is a defined
// string type so the public API can expose named constants and the compiler can
// catch accidental interchange with arbitrary strings.
type AgentName string

// AgentProfile is taboo's per-agent abstraction: it names the agent's SDK
// environment and builds the exact invocation taboo runs inside the workshop.
// Each supported agent (OpenCode, Claude Code, and Copilot now; Codex/Pi/Cursor
// later) is one implementation. The interface is reviewed by hand because
// fan-out and sessions build on it; see
// docs/adr/0001-agentprofile-argv-stdin-command-contract.md.
type AgentProfile interface {
	// Name is the agent identity: it equals the SDK name baked into the
	// workshop and the remount qualifier used for the mount plugs.
	Name() AgentName
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

// OutputParser is an optional capability an AgentProfile may implement to
// post-process its captured stdout before it lands on RunResult.Output. The
// runner asserts for it after each exec: an agent that implements it has
// ParseOutput applied to the captured buffer; an agent that doesn't keeps its
// raw stdout verbatim. ParseOutput must return the agent's clean final text —
// the orchestrator's completion-signal scan and <result>{…}</result> extraction
// run on the result.
//
// It is deliberately NOT part of AgentProfile: the core interface is reviewed by
// hand and stays minimal (see the doc on AgentProfile), so only the agents whose
// stdout differs from their clean final text opt in. Today that is just Claude
// Code, whose --output-format stream-json interleaves tool calls; the asserted
// optional interface mirrors how Sessions() carves out per-agent behavior
// without growing the shared contract.
type OutputParser interface {
	ParseOutput(raw string) string
}

// OutputRenderer is an optional capability an AgentProfile may implement to
// pretty-print its captured stdout on the live display path. When the agent
// provides one, the runner wraps the caller's Stdout in Render(stdout) before
// teeing, so the workflow log receives a human-readable transcript instead of
// the agent's raw stdout. The capture seam is untouched: the buffer that becomes
// RunResult.Output still accumulates the raw stdout (and OutputParser still
// reduces it), so display and scan stay separate concerns.
//
// Like OutputParser it is deliberately NOT part of AgentProfile and is asserted,
// not added to the hand-reviewed core interface. Today only Claude Code opts in:
// its --output-format stream-json stdout is JSONL, which Render turns into a
// transcript of assistant text and tool calls. OpenCode and Copilot already
// stream a readable transcript, so they do not implement it and the runner
// forwards their stdout verbatim.
type OutputRenderer interface {
	Render(w io.Writer) io.Writer
}

// CommandOptions is the input to AgentProfile.BuildCommand: the agent-agnostic
// inputs each profile maps onto its own CLI flags.
type CommandOptions struct {
	// Prompt is the agent's instruction for this run.
	Prompt string
	// ResumeSession, if set, asks the agent to continue this prior session by id
	// rather than starting fresh (e.g. OpenCode's --session <id>). Empty = fresh.
	ResumeSession string
	// Fork, when set together with ResumeSession, asks the agent to fork that
	// session into a new one rather than appending to it (e.g. OpenCode's --fork),
	// leaving the source conversation untouched. It is ignored without
	// ResumeSession, and a no-op for an agent whose CLI has no native fork.
	Fork bool
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
// locating the store from the outside — the integration test today, and a future
// session-id capture that surfaces ids on RunResult. Resume/fork do not read it:
// they thread a caller-supplied id straight to the agent via CommandOptions.
type SessionSpec struct {
	DirEnv string
	Subdir string
}

// Concrete profiles live one-per-file alongside this one: openCode in
// agent_opencode.go, claudeCode in agent_claudecode.go, each mirroring the
// matching pkg/sdk/<name>/ provisioning dir by filename. Add a new agent
// as agent_<name>.go (+ _test.go), not by growing this file.
