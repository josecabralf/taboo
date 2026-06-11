package taboo

import (
	"fmt"
	"time"
)

// projectArgs returns the leading global flags every workshop invocation shares.
func projectArgs(project string) []string {
	return []string{"--project", project}
}

func launchArgs(project, ws string) []string {
	return append(projectArgs(project), "launch", ws)
}

func stopArgs(project, ws string) []string {
	return append(projectArgs(project), "stop", ws)
}

func startArgs(project, ws string) []string {
	return append(projectArgs(project), "start", ws)
}

func infoArgs(project, ws string) []string {
	return append(projectArgs(project), "info", ws)
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
	// (the value never appears in argv).
	envKeys []string
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
	args = append(args, ws, "--")
	args = append(args, command...)
	return args
}
