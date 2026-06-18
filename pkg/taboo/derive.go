package taboo

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// deriveDefinition derives the agent's workshop definition from the project's
// own workshop.yaml (source). It parses source as an opaque yaml.Node tree and
// touches only the keys taboo models: it overwrites `name:` with cfg.Workshop
// and appends the agent SDK to `sdks:`. Everything else — `base:`, the source
// SDKs and their unmodeled fields, `actions:`, custom plugs — is carried
// through verbatim, never decoded into a typed struct and re-marshaled, so
// taboo cannot silently drop fields it does not understand (issue #68).
func deriveDefinition(cfg Config, source []byte) (string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(source, &doc); err != nil {
		return "", err
	}
	// A non-mapping or empty document (an empty file, a bare scalar, a top-level
	// list) carries no name/sdks: fail fast with a clear taboo error instead of
	// panicking on Content[0] or silently emitting a malformed definition that
	// only breaks later, opaquely, inside `workshop launch`.
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return "", fmt.Errorf("project workshop.yaml is empty or its root is not a mapping")
	}
	root := doc.Content[0] // document node wraps the root mapping

	// Overwrite name; leave base untouched (inherited from source).
	if err := setMappingValue(root, "name", cfg.Workshop); err != nil {
		return "", err
	}

	agent := sdkDef{Name: projectSDKRef(cfg.Agent.Name()), Plugs: agentPlugs(cfg)}
	var agentNode yaml.Node
	if err := agentNode.Encode(agent); err != nil {
		return "", err
	}

	sdks := mappingValue(root, "sdks")
	if sdks == nil {
		sdks = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		var key yaml.Node
		if err := key.Encode("sdks"); err != nil {
			return "", err
		}
		root.Content = append(root.Content, &key, sdks)
	}
	sdks.Content = append(sdks.Content, &agentNode)

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// projectSDKNames returns the bare names of the in-project SDKs (those referenced
// as "project-<x>") declared in the source definition, with the "project-" prefix
// stripped. Store SDKs (e.g. "go") and any non-project entries are excluded. The
// agent SDK taboo appends is NOT in the source, so it is never returned here.
func projectSDKNames(source []byte) []string {
	var doc yaml.Node
	if err := yaml.Unmarshal(source, &doc); err != nil || len(doc.Content) == 0 {
		return nil
	}
	sdks := mappingValue(doc.Content[0], "sdks")
	if sdks == nil {
		return nil
	}
	var names []string
	for _, sdk := range sdks.Content { // each element is a mapping node
		if n := mappingValue(sdk, "name"); n != nil && strings.HasPrefix(n.Value, "project-") {
			names = append(names, strings.TrimPrefix(n.Value, "project-"))
		}
	}
	return names
}

// agentPlugs returns the mount plugs for the injected agent SDK: workspace and
// gitcommon always, plus sessions for a session-capable agent.
func agentPlugs(cfg Config) map[string]plug {
	plugs := map[string]plug{
		"workspace": {Interface: "mount", WorkshopTarget: workspaceTarget},
		"gitcommon": {Interface: "mount", WorkshopTarget: gitCommonTarget(cfg.RepoPath)},
	}
	if _, ok := cfg.Agent.Sessions(); ok {
		plugs["sessions"] = plug{Interface: "mount", WorkshopTarget: sessionsTarget}
	}
	return plugs
}

// mappingValue returns the value node for key in mapping m, or nil if absent.
// A YAML mapping node stores keys and values as alternating Content entries.
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// setMappingValue overwrites key's value in mapping m, or appends the pair if
// key is absent.
func setMappingValue(m *yaml.Node, key string, v any) error {
	if val := mappingValue(m, key); val != nil {
		return val.Encode(v)
	}
	var k, val yaml.Node
	if err := k.Encode(key); err != nil {
		return err
	}
	if err := val.Encode(v); err != nil {
		return err
	}
	m.Content = append(m.Content, &k, &val)
	return nil
}
