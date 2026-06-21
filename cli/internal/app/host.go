package app

import (
	"context"

	"github.com/josecabralf/taboo"
)

// probe runs a single host command through the Commander seam and returns its
// captured stdout and the run error. All probe command names and args are static
// literals, so there is no untrusted-input injection concern.
func probe(ctx context.Context, env Env, name string, args ...string) (string, error) {
	return taboo.Output(ctx, env.Cmd, taboo.Cmd{Name: name, Args: args})
}

// hostChecks assembles every always-on host check in display order, threading
// the LXD installed→reachable dependency.
func hostChecks(ctx context.Context, env Env) []check {
	checks := []check{checkWorkshop(ctx, env)}
	checks = append(checks, checkLXD(ctx, env)...)
	checks = append(checks, checkGit(ctx, env))
	checks = append(checks, checkGo(ctx, env))
	return checks
}

// checkWorkshop verifies the workshop snap is runnable and at least the floor
// version. It errors when the probe fails or the reported version is too old.
func checkWorkshop(ctx context.Context, env Env) check {
	const name = "workshop"
	out, err := probe(ctx, env, "workshop", "--version")
	if err != nil {
		return fail(name, "workshop snap not found or not runnable (`workshop --version` failed); install it with `sudo snap install workshop`")
	}
	got, perr := parseVersion(out)
	if perr != nil {
		return fail(name, "could not read workshop version from `workshop --version` output (expected MAJOR.MINOR.PATCH)")
	}
	floor, ferr := parseVersion(minWorkshopVersion)
	if ferr != nil {
		// minWorkshopVersion is a static, well-formed constant; a parse failure
		// here is a programmer error, not a host condition.
		return fail(name, "internal error: built-in minimum workshop version is malformed; please report this")
	}
	if versionLess(got, floor) {
		return fail(name, "workshop "+formatVersion(got)+" is too old; needs >= "+minWorkshopVersion+" — upgrade with `sudo snap refresh workshop`")
	}
	return ok(name, "workshop "+formatVersion(got)+" (>= "+minWorkshopVersion+")")
}

// checkLXD verifies LXD is installed (`lxc version`) and, only when installed,
// reachable/initialized (`lxc info`). It returns the installed check first and
// the reachable check second; when LXD is not installed the reachable check is a
// dependent skip rather than a misleading probe result.
func checkLXD(ctx context.Context, env Env) []check {
	const (
		installedName = "lxd"
		reachableName = "lxd-reachable"
	)
	if _, err := probe(ctx, env, "lxc", "version"); err != nil {
		return []check{
			fail(installedName, "LXD not installed (`lxc` not found); install it with `sudo snap install lxd`"),
			fail(reachableName, "skipped: LXD is not installed (see the lxd check above)"),
		}
	}
	installed := ok(installedName, "LXD installed (`lxc version` ok)")
	if _, err := probe(ctx, env, "lxc", "info"); err != nil {
		return []check{installed, fail(reachableName,
			"LXD not reachable/initialized (is the daemon running? `sudo snap start lxd`)")}
	}
	return []check{installed, ok(reachableName, "LXD reachable (`lxc info` ok)")}
}

// checkGit verifies git is on PATH via `git --version`.
func checkGit(ctx context.Context, env Env) check {
	const name = "git"
	if _, err := probe(ctx, env, "git", "--version"); err != nil {
		return fail(name, "git not found")
	}
	return ok(name, "git present")
}

// checkGo probes the Go toolchain. It is only needed to scaffold/run main.go, so
// a missing toolchain is a warning, never an error.
func checkGo(ctx context.Context, env Env) check {
	const name = "go"
	if _, err := probe(ctx, env, "go", "version"); err != nil {
		return warn(name, "Go toolchain not found (only needed to scaffold/run main.go)")
	}
	return ok(name, "Go toolchain present")
}
