package taboo

import (
	"fmt"
	"time"
)

// WorkshopName derives the per-agent workshop name from a base name and an
// agent name, so taboo provisions one workshop per distinct agent (reused
// across runs) rather than one per run.
func WorkshopName(base, agent string) string {
	return base + "-" + agent
}

// projectArgs returns the leading global flags every workshop invocation shares.
func projectArgs(project string) []string {
	return []string{"--project", project}
}

// verbArgs builds a plain `workshop --project <p> <verb> <ws>` invocation — the
// shape shared by launch, stop, start, and info.
func verbArgs(project, verb, ws string) []string {
	return append(projectArgs(project), verb, ws)
}

// remountArgs points the <ws>/<sdk>:<plug> mount plug at a new host source:
//
//	workshop --project <p> remount <ws>/<sdk>:<plug> <source>
func remountArgs(project, ws, sdk, plug, source string) []string {
	target := fmt.Sprintf("%s/%s:%s", ws, sdk, plug)
	return append(projectArgs(project), "remount", target, source)
}

// execOptions are the per-exec knobs taboo sets on `workshop exec`.
type execOptions struct {
	cwd     string
	timeout time.Duration
	// envKeys are env var names whose host values are inherited via `--env NAME`
	// (the value never appears in argv). Used for credentials.
	envKeys []string
	// env are explicit `--env NAME=VALUE` assignments whose value is set in argv
	// rather than inherited. Used to point an agent's session-dir env var at the
	// mounted sessions path (the value is a workshop path, not a secret).
	env []envAssignment
}

// envAssignment is an explicit environment variable value passed to the workshop
// as `--env NAME=VALUE`. Unlike execOptions.envKeys, the value is set here, not
// inherited from the host.
type envAssignment struct {
	Name  string
	Value string
}

// execArgs builds:
//
//	workshop --project <p> exec --cwd <cwd> [--timeout <d>] [--env NAME...] <ws> -- <command...>
func execArgs(project, ws string, opts execOptions, command []string) []string {
	args := append(projectArgs(project), "exec")
	if opts.cwd != "" {
		args = append(args, "--cwd", opts.cwd)
	}
	if opts.timeout > 0 {
		args = append(args, "--timeout", opts.timeout.String())
	}
	for _, k := range opts.envKeys {
		args = append(args, "--env", k)
	}
	for _, e := range opts.env {
		args = append(args, "--env", e.Name+"="+e.Value)
	}
	args = append(args, ws, "--")
	args = append(args, command...)
	return args
}
