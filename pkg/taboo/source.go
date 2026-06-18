package taboo

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// resolveSourceDefinition resolves the project's source workshop definition that
// taboo derives the agent's workshop from. A non-empty selection names a
// definition under <repoPath>/.workshop/; empty auto-resolves the project's
// single definition. It returns the absolute path to the resolved definition.
func resolveSourceDefinition(repoPath, selection string) (string, error) {
	named, err := SourceDefinitions(repoPath)
	if err != nil {
		return "", err
	}

	if selection != "" {
		for _, name := range named {
			if name == selection {
				return filepath.Join(repoPath, ".workshop", name+".yaml"), nil
			}
		}
		return "", fmt.Errorf("unknown source definition %q: available definitions are %s", selection, strings.Join(named, ", "))
	}

	root := filepath.Join(repoPath, "workshop.yaml")
	if _, err := os.Stat(root); err == nil {
		if len(named) > 0 {
			return "", fmt.Errorf("ambiguous workshop definition: both a root workshop.yaml and named .workshop/*.yaml (%s) are present", strings.Join(named, ", "))
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

// SourceDefinitions returns the sorted names of the named workshop definitions
// under <repoPath>/.workshop/ (each is a regular *.yaml file; the name is the
// filename stem). It returns an empty slice for a root-only or empty repo.
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
