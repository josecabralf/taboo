package app

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"

	taboo "github.com/josecabralf/taboo/pkg"
)

// findConfig ascends from start looking for a taboo.yaml: either start itself is
// a .taboo dir holding taboo.yaml, or an ancestor holds .taboo/taboo.yaml. It
// returns the config path and true on the first hit, or "" and false at the
// filesystem root. The statFile callback lets tests stub the existence probe.
func findConfig(start string, statFile func(string) bool) (string, bool) {
	dir := filepath.Clean(start)
	for {
		if statFile(filepath.Join(dir, "taboo.yaml")) {
			return filepath.Join(dir, "taboo.yaml"), true
		}
		if statFile(filepath.Join(dir, ".taboo", "taboo.yaml")) {
			return filepath.Join(dir, ".taboo", "taboo.yaml"), true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// configChecks runs the config-aware checks when a taboo.yaml is discoverable
// from env.Getwd(). It returns no checks (not an error) when no config is found,
// which is the normal out-of-project case. The statFile and loadConfig callbacks
// are injected so discovery and load are testable without the real
// filesystem/loader.
func configChecks(
	ctx context.Context,
	env Env,
	statFile func(string) bool,
	loadConfig func(string) (*taboo.ProjectConfig, error),
) []check {
	wd, err := env.Getwd()
	if err != nil {
		return nil
	}
	path, found := findConfig(wd, statFile)
	if !found {
		return nil
	}
	cfg, err := loadConfig(path)
	if err != nil {
		return []check{fail("config", configLoadMessage(err))}
	}
	checks := []check{ok("config", "loaded "+path)}
	checks = append(checks, credentialChecks(env, cfg)...)
	checks = append(checks, repoChecks(ctx, env, cfg)...)
	checks = append(checks, workshopProjectChecks(statFile, cfg)...)
	return checks
}

// workshopProjectChecks reports whether the configured repo is a workshop
// project, naming the single hardcoded <repo>/workshop.yaml source path it builds
// (filepath.Join — there is no selection or disambiguation here). It flags a repo
// with no workshop.yaml as a hard error. The check is presence-only: it does NOT
// derive the workshop.yaml (that is validate's source-definition/derive job).
func workshopProjectChecks(statFile func(string) bool, cfg *taboo.ProjectConfig) []check {
	if cfg.Repo == "" {
		return nil // mirror repoChecks: nothing to check without a configured repo.
	}
	src := filepath.Join(cfg.Repo, "workshop.yaml")
	if !statFile(src) {
		return []check{
			fail("workshop-project", "not a workshop project: no workshop.yaml in "+cfg.Repo+" — add a workshop.yaml, then re-run"),
		}
	}
	return []check{
		ok("workshop-project", "configured repo is a workshop project (workshop.yaml present at "+src+")"),
	}
}

// configLoadMessage turns a LoadConfig error into a user-facing message,
// distinguishing a parse failure from an unreadable file via the library
// sentinels.
func configLoadMessage(err error) string {
	switch {
	case errors.Is(err, taboo.ErrConfigParse):
		return "config invalid (parse error): " + err.Error()
	case errors.Is(err, taboo.ErrConfigRead):
		return "config invalid (read error): " + err.Error()
	default:
		return "config invalid: " + err.Error()
	}
}

// credentialChecks emits one WARN per distinct referenced agent that has none of
// its credential env keys set. Agents are collected from the top-level profile
// and every workflow profile, deduped by Name(), and visited in a stable sorted
// order so output is deterministic.
func credentialChecks(env Env, cfg *taboo.ProjectConfig) []check {
	profiles := distinctProfiles(cfg)
	checks := make([]check, 0, len(profiles))
	for _, p := range profiles {
		keys := p.CredentialEnvKeys()
		if anyEnvSet(env, keys) {
			continue
		}
		checks = append(checks, warn(
			"credentials/"+p.Name(),
			"missing credentials for agent "+p.Name()+" (set one of: "+strings.Join(keys, ", ")+")",
		))
	}
	return checks
}

// distinctProfiles returns the config's referenced agent profiles deduped by
// Name() in sorted name order: the top-level profile plus every workflow
// profile that is non-nil.
func distinctProfiles(cfg *taboo.ProjectConfig) []taboo.AgentProfile {
	seen := map[string]taboo.AgentProfile{}
	if cfg.Profile != nil {
		seen[cfg.Profile.Name()] = cfg.Profile
	}
	for _, wf := range cfg.Workflows {
		if wf.Profile != nil {
			seen[wf.Profile.Name()] = wf.Profile
		}
	}
	out := make([]taboo.AgentProfile, 0, len(seen))
	for _, p := range seen {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b taboo.AgentProfile) int {
		return strings.Compare(a.Name(), b.Name())
	})
	return out
}

// anyEnvSet reports whether at least one of keys resolves to a non-empty value
// through env.LookupEnv.
func anyEnvSet(env Env, keys []string) bool {
	for _, k := range keys {
		if v, ok := env.LookupEnv(k); ok && v != "" {
			return true
		}
	}
	return false
}

// repoChecks validates the configured repo path when one is set: it must not
// live under a tmpfs path (/tmp or /run), and it must be a git work tree.
func repoChecks(ctx context.Context, env Env, cfg *taboo.ProjectConfig) []check {
	if cfg.Repo == "" {
		return nil
	}
	checks := []check{repoLocationCheck(cfg.Repo)}
	checks = append(checks, repoGitCheck(ctx, env, cfg.Repo))
	return checks
}

// repoLocationCheck errors when the repo path sits under /tmp or /run, whose
// tmpfs mounts vanish on reboot and are not safe for a persistent worktree.
func repoLocationCheck(repo string) check {
	const name = "repo-path"
	clean := filepath.Clean(repo)
	for _, bad := range []string{"/tmp", "/run"} {
		if clean == bad || strings.HasPrefix(clean, bad+"/") {
			return fail(name,
				"configured repo "+repo+" is under "+bad+
					", which is cleared on reboot (tmpfs). Move it to persistent storage.")
		}
	}
	return ok(name, "configured repo path is on persistent storage")
}

// repoGitCheck errors when the configured repo is not a git work tree, probed
// via `git -C <repo> rev-parse --is-inside-work-tree`.
func repoGitCheck(ctx context.Context, env Env, repo string) check {
	const name = "repo-git"
	if _, err := probe(ctx, env, "git", "-C", repo, "rev-parse", "--is-inside-work-tree"); err != nil {
		return fail(name,
			"configured repo "+repo+" is not a git repository (or does not exist)")
	}
	return ok(name, "configured repo is a git repository")
}
