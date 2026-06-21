package agent

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// ErrUnknownAgent is the sentinel NewProfile wraps when a name matches no
// registered agent. The CLI matches it with errors.Is to branch into its
// fuzzy-suggestion path (story #24); NewProfile itself never suggests.
var ErrUnknownAgent = errors.New("taboo: unknown agent")

// modelHint is a per-agent model-format heuristic for `taboo validate` (story
// #25). ADR 0005 fixed its placement — registry-table metadata, co-located beside
// each agent as <name>Hint — and deferred its concrete shape to the validate
// slice that consumes it; ADR 0008 settles that shape here: pattern is the regexp
// a well-formed model matches, and expected is the human-readable format the
// validate warning quotes. A nil pattern means the agent has no opinion (e.g.
// copilot, which proxies many providers' models), so matches always succeeds and
// never warns. The hint stays off the deliberately-minimal AgentProfile interface
// (ADR 0001) — it is read from the agent name alone, before anything is built.
type modelHint struct {
	pattern  *regexp.Regexp
	expected string
}

// matches reports whether model looks well-formed for this hint's agent. The
// heuristic is advisory (warn, never fail): a no-opinion hint (nil pattern)
// always matches, and surrounding whitespace is trimmed so a stray space in YAML
// does not trip a false warning.
func (h modelHint) matches(model string) bool {
	return h.pattern == nil || h.pattern.MatchString(strings.TrimSpace(model))
}

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
func (r registration) name() AgentName { return r.New("").Name() }

// agents is taboo's declarative agent roster: one explicit line per supported
// agent. Enumeration stays greppable here rather than hiding behind init()
// self-registration (ADR 0005); adding an agent is a deliberate one-line edit.
// The order is intentionally not alphabetical: registering OpenCode (the first
// profile) before ClaudeCode keeps AgentNames's slices.Sort load-bearing — a
// dropped sort would reorder its output and trip TestAgentNames_SortedAndComplete.
var agents = []registration{
	{New: NewOpenCode, Hint: openCodeHint},
	{New: NewClaudeCode, Hint: claudeCodeHint},
	{New: NewGitHubCopilot, Hint: copilotHint},
}

// NewProfile resolves a canonical agent name to its AgentProfile, constructed
// for model. It validates the name only — model validation is the validate
// slice's concern (via the hint) — so its sole error is a wrapped
// ErrUnknownAgent the CLI matches with errors.Is.
func NewProfile(name AgentName, model string) (AgentProfile, error) {
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
		names[i] = string(a.name())
	}
	slices.Sort(names)
	return names
}

// MatchModelFormat reports whether model looks well-formed for the named agent
// and returns that agent's human-readable expected-format string. It is purely
// advisory (story #25): an unknown agent, a no-opinion hint, or a pattern match
// all yield ok=true — only a recognized agent whose hint pattern rejects the
// model yields ok=false, which the validate command turns into a WARN, never a
// failure. The expected string is "" exactly when the agent is unknown or has no
// opinion.
func MatchModelFormat(agent AgentName, model string) (ok bool, expected string) {
	for _, a := range agents {
		if a.name() == agent {
			return a.Hint.matches(model), a.Hint.expected
		}
	}
	return true, ""
}
