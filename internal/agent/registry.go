package agent

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// ErrUnknownAgent is the sentinel NewProfile wraps when a name matches no
// registered agent; the CLI matches it with errors.Is for its suggestion path.
var ErrUnknownAgent = errors.New("taboo: unknown agent")

// modelHint is a per-agent model-format heuristic for `taboo validate` (ADR 0008):
// pattern is the regexp a well-formed model matches, expected is the format the
// warning quotes. A nil pattern means no opinion (e.g. copilot), so matches always
// succeeds. Read from the agent name alone, off the AgentProfile interface.
type modelHint struct {
	pattern  *regexp.Regexp
	expected string
}

// matches reports whether model looks well-formed. Advisory (warn, never fail): a
// nil-pattern hint always matches, and surrounding whitespace is trimmed.
func (h modelHint) matches(model string) bool {
	return h.pattern == nil || h.pattern.MatchString(strings.TrimSpace(model))
}

// registration pairs an agent's constructor with its model-format hint.
type registration struct {
	New  func(model string) AgentProfile
	Hint modelHint
}

// name reads the registration's canonical key via New("").Name(); Name() is
// model-independent, so constructing with "" purely to read it is safe.
func (r registration) name() AgentName { return r.New("").Name() }

// agents is taboo's declarative agent roster, one line per agent (ADR 0005). The
// order is intentionally not alphabetical: OpenCode before ClaudeCode keeps
// AgentNames's slices.Sort load-bearing, so a dropped sort would trip
// TestAgentNames_SortedAndComplete.
var agents = []registration{
	{New: NewOpenCode, Hint: openCodeHint},
	{New: NewClaudeCode, Hint: claudeCodeHint},
	{New: NewGitHubCopilot, Hint: copilotHint},
}

// NewProfile resolves a canonical agent name to its AgentProfile for model. It
// validates the name only; its sole error is a wrapped ErrUnknownAgent.
func NewProfile(name AgentName, model string) (AgentProfile, error) {
	for _, a := range agents {
		if a.name() == name {
			return a.New(model), nil
		}
	}
	return nil, fmt.Errorf("%w: %q", ErrUnknownAgent, name)
}

// AgentNames returns every registered agent's canonical name, sorted. It is the
// CLI's candidate set for suggesting a correction on an unknown name.
func AgentNames() []string {
	names := make([]string, len(agents))
	for i, a := range agents {
		names[i] = string(a.name())
	}
	slices.Sort(names)
	return names
}

// MatchModelFormat reports whether model looks well-formed for the named agent
// and returns that agent's expected-format string. Advisory: an unknown agent, a
// no-opinion hint, or a match all yield ok=true; only a recognized agent whose
// pattern rejects the model yields ok=false; expected is "" when the agent is
// unknown or has no opinion.
func MatchModelFormat(agent AgentName, model string) (ok bool, expected string) {
	for _, a := range agents {
		if a.name() == agent {
			return a.Hint.matches(model), a.Hint.expected
		}
	}
	return true, ""
}
