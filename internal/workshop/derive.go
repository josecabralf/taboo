package workshop

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

// deriveDefinition derives the agent's workshop definition from the project's own
// workshop.yaml (source). It parses source as an opaque yaml.Node tree and touches
// only the keys taboo models: it overwrites `name:` with cfg.Workshop and appends
// the agent SDK to `sdks:`. Everything else is carried through, never decoded into
// a typed struct, so taboo cannot silently drop fields it does not understand
// (issue #68). The yaml.Marshal round-trip does not preserve comments or
// whitespace byte-for-byte; only field values survive.
//
// It also returns projectNames: the bare names of the source's in-project SDKs
// (referenced as "project-<x>", prefix stripped). Store SDKs and non-project
// entries are excluded, as is the injected agent SDK (names are read before it is
// appended). The caller reconciles these into .workshop/ symlinks.
func deriveDefinition(cfg Config, source []byte) (out string, projectNames []string, err error) {
	// A multi-document source (`---` separators) decodes into a single Node only
	// for the FIRST document, silently dropping the rest, which would derive from a
	// partial definition. Reject it before the single-Node parse below.
	if err := requireSingleDocument(source); err != nil {
		return "", nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(source, &doc); err != nil {
		return "", nil, err
	}
	// A non-mapping or empty document carries no name/sdks: fail fast rather than
	// panicking on Content[0] or emitting a malformed definition that only breaks
	// later inside `workshop launch`.
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return "", nil, fmt.Errorf("project workshop.yaml is empty or its root is not a mapping")
	}
	root := doc.Content[0] // document node wraps the root mapping

	// The key walk touches only the first match for a key and does not resolve
	// merge keys, so two shapes would mis-derive silently: a duplicate top-level key
	// makes the overwrite/append ambiguous, and a merge key (`<<:`) hides the real
	// name/sdks behind an unresolved alias. Reject both up front.
	if err := rejectDuplicateKeys(root); err != nil {
		return "", nil, err
	}
	if err := rejectMergeKeys(root); err != nil {
		return "", nil, err
	}

	// Overwrite name; leave base inherited from source.
	if err := setMappingValue(root, "name", cfg.Workshop); err != nil {
		return "", nil, err
	}

	agent := sdkDef{Name: projectSDKRef(string(cfg.Agent.Name())), Plugs: agentPlugs(cfg)}
	var agentNode yaml.Node
	if err := agentNode.Encode(agent); err != nil {
		return "", nil, err
	}

	sdks, err := ensureSDKSequence(root)
	if err != nil {
		return "", nil, err
	}
	// Read the in-project SDK names before appending the agent so it is excluded.
	projectNames = projectSDKNamesFromSeq(sdks)
	sdks.Content = append(sdks.Content, &agentNode)

	marshaled, err := yaml.Marshal(&doc)
	if err != nil {
		return "", nil, err
	}
	return string(marshaled), projectNames, nil
}

// ensureSDKSequence returns root's `sdks` node as a sequence, normalizing the two
// acceptable non-sequence shapes in place: an absent key (a fresh empty sequence
// is appended) and a bare `sdks:` line that decodes to a null scalar. Any other
// shape is rejected as an authoring mistake.
func ensureSDKSequence(root *yaml.Node) (*yaml.Node, error) {
	sdks := mappingValue(root, "sdks")
	switch {
	case sdks == nil:
		sdks = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		var key yaml.Node
		if err := key.Encode("sdks"); err != nil {
			return nil, err
		}
		root.Content = append(root.Content, &key, sdks)
	case sdks.Kind != yaml.SequenceNode:
		if sdks.Kind == yaml.ScalarNode && (sdks.Tag == "!!null" || sdks.Value == "") {
			sdks.Kind = yaml.SequenceNode
			sdks.Tag = "!!seq"
			sdks.Value = ""
		} else {
			return nil, fmt.Errorf("project workshop.yaml `sdks:` must be a list, got %s", nodeDescription(sdks.Kind))
		}
	}
	return sdks, nil
}

// DryRunDerive validates that taboo could derive the agent's workshop from source
// without launching anything or writing to the host filesystem. It runs the full
// derivation and discards the rendered definition, returning the in-project SDK
// names and any error so a caller can fail fast on a malformed source.
func DryRunDerive(cfg Config, source []byte) (projectNames []string, err error) {
	_, projectNames, err = deriveDefinition(cfg, source)
	if err != nil {
		return projectNames, err
	}
	// The agent's SDK must be embedded for seedSDK to seed it. A registered agent
	// missing its sdk/<name>/ tree would otherwise fail only at seed time, burning a
	// workshop; catch it here without writing.
	if _, err := fs.Stat(sdkFS, path.Join("sdk", string(cfg.Agent.Name()))); err != nil {
		return projectNames, fmt.Errorf("agent %q has no embedded SDK to seed; "+
			"this is a taboo build defect (registry/embed drift), please report this", cfg.Agent.Name())
	}
	return projectNames, nil
}

// requireSingleDocument rejects a multi-document source (`---` separators),
// because the single-Node parse in deriveDefinition keeps only the first document
// and would derive from a partial definition. It decodes documents off the stream
// until EOF; more than one is an error.
func requireSingleDocument(source []byte) error {
	dec := yaml.NewDecoder(bytes.NewReader(source))
	count := 0
	for {
		var n yaml.Node
		err := dec.Decode(&n)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// A genuine parse error surfaces from the single-Node parse with a fuller
			// message; here just stop probing and let that path report it.
			return nil
		}
		count++
		if count > 1 {
			return fmt.Errorf("project workshop.yaml must contain a single document")
		}
	}
	return nil
}

// rejectDuplicateKeys rejects a root mapping that repeats a top-level key. The key
// walk only touches the first match, so a duplicate makes derivation ambiguous.
func rejectDuplicateKeys(m *yaml.Node) error {
	seen := make(map[string]struct{}, len(m.Content)/2)
	for i := 0; i+1 < len(m.Content); i += 2 {
		key := m.Content[i].Value
		if _, dup := seen[key]; dup {
			return fmt.Errorf("project workshop.yaml has a duplicate top-level key: %s", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// rejectMergeKeys rejects a YAML merge key (`<<:`) in the root mapping or in any
// sdks sequence element's mapping. The plain key walk does not resolve merge keys,
// so they would hide the real name/sdks behind an unresolved alias. Detecting at
// these levels is sufficient; the walk does not recurse deeper.
func rejectMergeKeys(root *yaml.Node) error {
	if mappingHasMergeKey(root) {
		return errMergeKeyUnsupported()
	}
	if sdks := mappingValue(root, "sdks"); sdks != nil && sdks.Kind == yaml.SequenceNode {
		for _, sdk := range sdks.Content {
			if sdk.Kind == yaml.MappingNode && mappingHasMergeKey(sdk) {
				return errMergeKeyUnsupported()
			}
		}
	}
	return nil
}

// mappingHasMergeKey reports whether mapping m uses a YAML merge key — a key
// node tagged !!merge or with the literal value "<<".
func mappingHasMergeKey(m *yaml.Node) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := m.Content[i]
		if k.Tag == "!!merge" || k.Value == "<<" {
			return true
		}
	}
	return false
}

// errMergeKeyUnsupported is the shared merge-key rejection.
func errMergeKeyUnsupported() error {
	return fmt.Errorf("project workshop.yaml uses YAML merge keys (<<), which taboo does not support")
}

// nodeDescription returns a short description of a yaml node kind, for the `sdks:`
// must-be-a-list error.
func nodeDescription(kind yaml.Kind) string {
	switch kind {
	case yaml.MappingNode:
		return "a mapping"
	case yaml.ScalarNode:
		return "a scalar"
	case yaml.AliasNode:
		return "an alias"
	default:
		return "a non-list value"
	}
}

// projectSDKNamesFromSeq returns the bare names of the in-project SDKs (referenced
// as "project-<x>") in an sdks sequence node, with the "project-" prefix stripped.
// Store SDKs and non-project entries are excluded.
func projectSDKNamesFromSeq(sdks *yaml.Node) []string {
	var names []string
	for _, sdk := range sdks.Content { // each element is a mapping node
		if n := mappingValue(sdk, "name"); n != nil && strings.HasPrefix(n.Value, "project-") {
			names = append(names, strings.TrimPrefix(n.Value, "project-"))
		}
	}
	return names
}

// agentPlugs returns the mount plugs for the injected agent SDK. The workspace
// plug is always present, plus sessions for a session-capable agent, plus the
// worktree strategy's gitcommon and worktrees plugs. See CONTEXT.md.
func agentPlugs(cfg Config) map[string]plug {
	plugs := map[string]plug{
		"workspace": {Interface: "mount", WorkshopTarget: WorkspaceTarget},
	}
	// Each plug's workshop-target IS the mount's identical-path target. The branch
	// strategy returns none.
	for _, m := range StrategyGitMounts(cfg) {
		plugs[m.Plug] = plug{Interface: "mount", WorkshopTarget: m.Target}
	}
	if _, ok := cfg.Agent.Sessions(); ok {
		plugs["sessions"] = plug{Interface: "mount", WorkshopTarget: SessionsTarget}
	}
	return plugs
}

// mappingValue returns the value node for key in mapping m, or nil if absent. A
// YAML mapping node stores keys and values as alternating Content entries.
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
