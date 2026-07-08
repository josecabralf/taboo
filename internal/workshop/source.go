package workshop

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// resolveSourceDefinition resolves the project's source workshop definition. A
// non-empty selection names a definition under <repoPath>/.workshop/; empty
// auto-resolves the project's single definition. Returns an absolute path.
func resolveSourceDefinition(repoPath, selection string) (string, error) {
	named, err := SourceDefinitions(repoPath)
	if err != nil {
		return "", err
	}

	if selection != "" {
		if err := ValidateSourceDefinition(named, selection); err != nil {
			return "", err
		}
		return filepath.Join(repoPath, ".workshop", selection+".yaml"), nil
	}

	root := filepath.Join(repoPath, "workshop.yaml")
	if _, err := os.Stat(root); err == nil {
		if len(named) > 0 {
			return "", fmt.Errorf("ambiguous workshop definition: both a root workshop.yaml and named .workshop/*.yaml (%s) are present; remove one so taboo can derive from a single source", strings.Join(named, ", "))
		}
		return root, nil
	}

	if len(named) == 1 {
		return filepath.Join(repoPath, ".workshop", named[0]+".yaml"), nil
	}
	if len(named) >= 2 {
		return "", fmt.Errorf("multiple workshop definitions (%s): set source-definition in taboo.yaml or pass --from to pick one", strings.Join(named, ", "))
	}

	return "", fmt.Errorf("no workshop definition found in %s", repoPath)
}

// ValidateSourceDefinition checks that selection names one of the project's named
// workshop definitions, so init and run reject a typo'd or nonexistent name the
// same way. When the project has no named .workshop/*.yaml definitions at all it
// returns a distinct error telling the caller to drop the selection rather than an
// unknown-name error with an empty candidate list.
func ValidateSourceDefinition(named []string, selection string) error {
	if len(named) == 0 {
		return fmt.Errorf("this project has no named .workshop/*.yaml definitions; remove --source-definition/--from")
	}
	for _, name := range named {
		if name == selection {
			return nil
		}
	}
	return fmt.Errorf("unknown workshop definition %q: available definitions are %s", selection, strings.Join(named, ", "))
}

// SourceDefinitions returns the sorted names (filename stems) of the named
// workshop definitions under <repoPath>/.workshop/, each a regular *.yaml file. It
// returns an empty slice for a root-only or empty repo.
func SourceDefinitions(repoPath string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(repoPath, ".workshop"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var names []string
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") {
			continue
		}
		names = append(names, strings.TrimSuffix(name, ".yaml"))
	}
	sort.Strings(names)
	return names, nil
}
