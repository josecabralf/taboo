package taboo

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// NewProfile resolves a canonical agent name to its profile, constructed with
// the requested model. Name() is the key (model-independent), so the resolved
// profile reports the same name and threads the model through to its built
// invocation — proving NewProfile passed model, not "", to the constructor.
func TestNewProfile_KnownAgents(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		wantName string
	}{
		{name: "opencode", model: openCodeModel, wantName: "opencode"},
		{name: "claude-code", model: claudeCodeModel, wantName: "claude-code"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := NewProfile(tt.name, tt.model)
			if err != nil {
				t.Fatalf("NewProfile(%q, %q) error = %v, want nil", tt.name, tt.model, err)
			}
			if got := p.Name(); got != tt.wantName {
				t.Errorf("Name() = %q, want %q", got, tt.wantName)
			}
			// The model threads through to the built invocation, so the profile was
			// constructed with model (not the "" used only to read the name key).
			argv := p.BuildCommand(CommandOptions{Prompt: "go"}).Argv
			if !slices.Contains(argv, tt.model) {
				t.Errorf("BuildCommand argv = %v, want it to carry model %q", argv, tt.model)
			}
		})
	}
}

// Resolution is exact and case-sensitive: a typo, a wrong-case name, a spaced
// name, and an empty name all miss. Fuzzy matching is the CLI's job (ADR 0005),
// not the registry's, so every miss yields the one ErrUnknownAgent sentinel the
// CLI branches on with errors.Is, and never a profile.
func TestNewProfile_UnknownAgent(t *testing.T) {
	for _, name := range []string{"gemini", "OpenCode", "open code", ""} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			p, err := NewProfile(name, "some-model")
			if p != nil {
				t.Errorf("profile = %v, want nil on unknown agent", p)
			}
			if !errors.Is(err, ErrUnknownAgent) {
				t.Errorf("error = %v, want errors.Is(err, ErrUnknownAgent)", err)
			}
		})
	}
}

// The wrapped error names the offending input so the CLI can quote it back while
// suggesting a correction (story #24).
func TestNewProfile_UnknownAgentNamesInput(t *testing.T) {
	_, err := NewProfile("gemni", "m")
	if !strings.Contains(err.Error(), "gemni") {
		t.Errorf("error %q, want it to name the unknown agent %q", err, "gemni")
	}
}

// AgentNames is the CLI's fuzzy-match candidate set: sorted and complete (every
// registered agent, nothing else). Hard-coded like the other argv assertions so
// adding an agent is a deliberate one-line test update alongside the roster.
func TestAgentNames_SortedAndComplete(t *testing.T) {
	got := AgentNames()

	// Complete: exactly the registered roster, nothing more, nothing missing.
	want := []string{"claude-code", "opencode"}
	if !slices.Equal(got, want) {
		t.Errorf("AgentNames() = %v, want %v (complete roster)", got, want)
	}
	// Sorted: asserted on its own so the sort is pinned independently of the
	// roster's registration order. The roster is registered non-alphabetically
	// (registry.go), so a dropped slices.Sort also trips the completeness check
	// above; this guards the contract even if the roster is later reordered.
	if !slices.IsSorted(got) {
		t.Errorf("AgentNames() = %v, want sorted ascending", got)
	}
}

// Roster invariant (ADR 0005), the safety-critical direction: every registered
// agent must have an embedded SDK whose directory and sdk.yaml `name` both equal
// its Name(), because runner.go uses Agent.Name() directly as the workshop SDK
// qualifier — a registered agent with no matching SDK breaks at provisioning.
//
// TODO(final-profile): also assert the reverse — every embedded sdk/<dir> has a
// registered profile. It fails today (codex/copilot/pi ship SDK dirs but no Go
// profile yet); wire it once the last profile lands, rather than a live skip-list
// that could rot into a false green.
func TestRegistry_EveryAgentHasMatchingSDK(t *testing.T) {
	for _, name := range AgentNames() {
		t.Run(name, func(t *testing.T) {
			path := "sdk/" + name + "/sdk.yaml"
			data, err := sdkFS.ReadFile(path)
			if err != nil {
				t.Fatalf("registered agent %q has no embedded SDK at %s: %v", name, path, err)
			}
			var meta struct {
				Name string `yaml:"name"`
			}
			if err := yaml.Unmarshal(data, &meta); err != nil {
				t.Fatalf("%s is not valid YAML: %v", path, err)
			}
			if meta.Name != name {
				t.Errorf("%s name = %q, want %q (must equal Agent.Name())", path, meta.Name, name)
			}
		})
	}
}

// The roster is well-formed: every registration has a non-nil constructor, the
// agents resolve to unique Name() keys, and each carries its co-located hint.
// ADR 0005 keys the registry on Name(), so two registrations resolving to the
// same name would let the first silently shadow the second in NewProfile and
// emit a duplicate from AgentNames — a plausible copy-paste slip when adding an
// agent, and otherwise untested. The hint is a deferred-type placeholder this
// slice (empty struct), referenced here only to stay load-bearing until the
// validate slice reads it.
func TestRegistry_RosterWellFormed(t *testing.T) {
	seen := make(map[string]bool, len(agents))
	for _, reg := range agents {
		if reg.New == nil {
			t.Errorf("registration has a nil New constructor")
			continue
		}
		name := reg.New("").Name()
		if seen[name] {
			t.Errorf("duplicate agent name %q in roster — keys must be unique", name)
		}
		seen[name] = true
		_ = reg.Hint
	}
}
