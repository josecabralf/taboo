package workshop

import (
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/josecabralf/taboo/internal/agent"
)

// Roster invariant (ADR 0005), the safety-critical direction: every registered
// agent must have an embedded SDK whose directory and sdk.yaml `name` both equal
// its Name(), because the runner uses Agent.Name() directly as the workshop SDK
// qualifier — a registered agent with no matching SDK breaks at provisioning.
//
// This test straddles two now-decomposed packages: the agent roster
// (agent.AgentNames) and the embedded SDK tree (sdkFS, defined in this package's
// materialize.go). It therefore lives here, white-box against sdkFS, rather than
// in internal/agent with the rest of the registry tests.
//
// TODO(final-profile): also assert the reverse — every embedded sdk/<dir> has a
// registered profile. It fails today (codex/pi ship SDK dirs but no Go profile
// yet); wire it once the last profile lands, rather than a live skip-list that
// could rot into a false green.
func TestRegistry_EveryAgentHasMatchingSDK(t *testing.T) {
	for _, name := range agent.AgentNames() {
		name := name
		t.Run(string(name), func(t *testing.T) {
			path := "sdk/" + string(name) + "/sdk.yaml"
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
			if meta.Name != string(name) {
				t.Errorf("%s name = %q, want %q (must equal Agent.Name())", path, meta.Name, name)
			}
		})
	}
}
