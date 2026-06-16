package taboo

import (
	"errors"
	"fmt"
	"slices"
)

// ErrUnknownAgent is the sentinel NewProfile wraps when a name matches no
// registered agent. The CLI matches it with errors.Is to branch into its
// fuzzy-suggestion path (story #24); NewProfile itself never suggests.
var ErrUnknownAgent = errors.New("taboo: unknown agent")

// modelHint is a per-agent model-format hint: an empty placeholder type for now.
// ADR 0005 fixes only its placement — registry-table metadata, co-located beside each
// agent as <name>Hint — and defers its concrete shape (regex, predicate, or
// human "expected format" string) to the validate slice that consumes it. An
// empty struct lets that slice add fields without churning registration.Hint's
// type, and keeps the hint off the deliberately-minimal AgentProfile interface
// (ADR 0001).
type modelHint struct{}

// registration pairs an agent's constructor with its model-format hint. The
// roster is keyed by New("").Name() — the profile's own identity, which equals
// its workshop SDK qualifier — so there is no second name literal to drift out
// of sync. Name() is model-independent, so constructing with "" purely to read
// the key is safe.
type registration struct {
	New  func(model string) AgentProfile
	Hint modelHint
}

// name reads the registration's canonical key: the profile's own Name(), which
// is model-independent, so constructing with "" purely to read it is safe (see
// the roster comment below). Centralizes the New("").Name() key extraction
// shared by NewProfile and AgentNames.
func (r registration) name() string { return r.New("").Name() }

// agents is taboo's declarative agent roster: one explicit line per supported
// agent. Enumeration stays greppable here rather than hiding behind init()
// self-registration (ADR 0005); adding an agent is a deliberate one-line edit.
// The order is intentionally not alphabetical: registering OpenCode (the first
// profile) before ClaudeCode keeps AgentNames's slices.Sort load-bearing — a
// dropped sort would reorder its output and trip TestAgentNames_SortedAndComplete.
var agents = []registration{
	{New: OpenCode, Hint: openCodeHint},
	{New: ClaudeCode, Hint: claudeCodeHint},
	{New: Copilot, Hint: copilotHint},
}

// NewProfile resolves a canonical agent name to its AgentProfile, constructed
// for model. It validates the name only — model validation is the validate
// slice's concern (via the hint) — so its sole error is a wrapped
// ErrUnknownAgent the CLI matches with errors.Is.
func NewProfile(name, model string) (AgentProfile, error) {
	for _, a := range agents {
		if a.name() == name {
			return a.New(model), nil
		}
	}
	return nil, fmt.Errorf("%w: %q", ErrUnknownAgent, name)
}

// AgentNames returns every registered agent's canonical name, sorted. It is the
// CLI's candidate set for suggesting a correction on an unknown name (story
// #24); the fuzzy matching itself lives in the CLI, not here.
func AgentNames() []string {
	names := make([]string, len(agents))
	for i, a := range agents {
		names[i] = a.name()
	}
	slices.Sort(names)
	return names
}
