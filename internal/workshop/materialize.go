package workshop

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// sdkFS holds every agent SDK taboo ships. A run seeds only the configured
// agent's tree into the managed project's .workshop/ directory so the rendered
// definition can reference it as an in-project SDK (e.g. "project-opencode").
//
//go:embed sdk
var sdkFS embed.FS

// Materialize regenerates the .taboo artifacts a workshop launch depends on: the
// seeded agent SDK, the derived definition, and the project-SDK symlinks. It runs
// at the start of every Setup so the artifacts self-heal each run (see ADR 0009).
// The definition is written BEFORE reconcile so a malformed source fails without
// touching symlinks. It returns the fingerprint for the caller to compare against
// the persisted record.
func Materialize(cfg Config) (fingerprint string, err error) {
	srcPath, err := sourceDefinitionPath(cfg)
	if err != nil {
		return "", fmt.Errorf("resolve project definition: %w", err)
	}
	// srcPath is the project's own workshop definition, resolved within the repo
	// by resolveSourceDefinition — not arbitrary user-controlled file access.
	source, err := os.ReadFile(srcPath) //nolint:gosec
	if err != nil {
		return "", fmt.Errorf("read project definition %s: %w", srcPath, err)
	}
	if err := seedSDK(cfg); err != nil {
		return "", fmt.Errorf("seed agent SDK: %w", err)
	}
	fingerprint, projectNames, err := writeDefinition(cfg, source)
	if err != nil {
		return "", fmt.Errorf("write definition: %w", err)
	}
	if err := reconcileProjectSDKs(cfg.ProjectDir, cfg.RepoPath, projectNames); err != nil {
		return "", fmt.Errorf("reconcile project SDKs: %w", err)
	}
	return fingerprint, nil
}

// sourceDefinitionPath is the project's own workshop definition that taboo derives
// the agent's workshop from. It lives under the repo root (RepoPath), never under
// ProjectDir. Resolution is delegated to resolveSourceDefinition.
func sourceDefinitionPath(cfg Config) (string, error) {
	return resolveSourceDefinition(cfg.RepoPath, cfg.SourceDefinition)
}

// definitionPath is where taboo writes the derived workshop definition. Taboo
// launches with --project <ProjectDir> and workshop resolves the launch from the
// project dir's root workshop.yaml, so it lives at <ProjectDir>/workshop.yaml.
func definitionPath(cfg Config) string {
	return filepath.Join(cfg.ProjectDir, "workshop.yaml")
}

// writeDefinition derives the agent's workshop definition from source, writes it
// to definitionPath, and returns its fingerprint plus the source's in-project SDK
// names (for the caller to reconcile into symlinks).
func writeDefinition(cfg Config, source []byte) (fingerprint string, projectNames []string, err error) {
	out, projectNames, err := deriveDefinition(cfg, source)
	if err != nil {
		return "", nil, err
	}
	dst := filepath.Clean(definitionPath(cfg))
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return "", nil, err
	}

	// dst is from config, cleaned by filepath.Join and filepath.Clean
	if err := os.WriteFile(dst, []byte(out), 0o600); err != nil { //nolint:gosec
		return "", nil, err
	}
	return fingerprintOf(out), projectNames, nil
}

// fingerprintOf returns a stable hex digest of a derived workshop definition, the
// drift key: equal digests mean the live workshop was last provisioned with this
// exact def and can be reused without a refresh.
func fingerprintOf(def string) string {
	sum := sha256.Sum256([]byte(def))
	return hex.EncodeToString(sum[:])
}

// seedSDK writes the configured agent's embedded SDK into the project's .workshop
// directory, so the rendered definition's "project-<agent>" reference resolves.
func seedSDK(cfg Config) error {
	const sdkRoot = "sdk"
	// Walk only the configured agent's subtree, stripping just the leading "sdk/"
	// so the agent-name segment survives and the destination stays .workshop/<agent>/.
	root := path.Join(sdkRoot, string(cfg.Agent.Name()))
	return fs.WalkDir(sdkFS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(p, sdkRoot+"/")
		dst := filepath.Join(cfg.ProjectDir, ".workshop", rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o750)
		}
		data, err := sdkFS.ReadFile(p)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if strings.Contains(rel, "/hooks/") {
			mode = 0o755 // hook scripts must be executable
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			return err
		}
		return os.WriteFile(dst, data, mode)
	})
}

// reconcileProjectSDKs makes <projectDir>/.workshop/<name> a symlink to
// <repoPath>/.workshop/<name> for each wanted name, and prunes stale symlinks.
// Safety invariant: it only ever creates or os.Removes entries it confirms are
// symlinks via os.Lstat, never a real dir (such as the seeded agent SDK) or a
// link's target.
func reconcileProjectSDKs(projectDir, repoPath string, names []string) error {
	dir := filepath.Join(projectDir, ".workshop")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[name] = true
		if err := ensureSymlink(dir, repoPath, name); err != nil {
			return err
		}
	}
	return pruneStaleSymlinks(dir, wanted)
}

// ensureSymlink creates or updates a symlink for the given SDK name.
func ensureSymlink(dir, repoPath, name string) error {
	link := filepath.Join(dir, name)
	target := filepath.Join(repoPath, ".workshop", name)
	if fi, err := os.Lstat(link); err == nil { //nolint:gosec // link is dir + an internal SDK name, not external path input
		if fi.Mode()&os.ModeSymlink == 0 {
			return nil // a real entry already occupies this name; never clobber it
		}
		cur, err := os.Readlink(link)
		if err != nil {
			return err
		}
		if cur == target {
			return nil // already correct
		}
		if err := os.Remove(link); err != nil { //nolint:gosec // link-safe: removes the symlink only, name is internal
			return err
		}
	}
	return os.Symlink(target, link)
}

// pruneStaleSymlinks removes symlinks that are no longer wanted.
func pruneStaleSymlinks(dir string, wanted map[string]bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if wanted[e.Name()] {
			continue
		}
		p := filepath.Join(dir, e.Name())
		fi, err := os.Lstat(p)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			continue // not a symlink (e.g. the seeded agent SDK dir) — leave it
		}
		if err := os.Remove(p); err != nil { // removes the stale link, never its target
			return err
		}
	}
	return nil
}
