package taboo

import (
	"slices"
	"testing"
	"time"
)

func TestWorkshopName(t *testing.T) {
	tests := []struct {
		name  string
		base  string
		agent string
		want  string
	}{
		{name: "opencode", base: "demo", agent: "opencode", want: "demo-opencode"},
		{name: "claude-code", base: "demo", agent: "claude-code", want: "demo-claude-code"},
		{name: "copilot", base: "myrepo", agent: "copilot", want: "myrepo-copilot"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WorkshopName(tt.base, tt.agent); got != tt.want {
				t.Errorf("WorkshopName(%q, %q) = %q, want %q", tt.base, tt.agent, got, tt.want)
			}
		})
	}
}

func TestWorkshopArgs(t *testing.T) {
	const project = "/var/taboo/proj"
	const ws = "taboo-run"

	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{
			name: "launch",
			got:  verbArgs(project, "launch", ws),
			want: []string{"--project", project, "launch", ws},
		},
		{
			name: "stop",
			got:  verbArgs(project, "stop", ws),
			want: []string{"--project", project, "stop", ws},
		},
		{
			name: "start",
			got:  verbArgs(project, "start", ws),
			want: []string{"--project", project, "start", ws},
		},
		{
			name: "info",
			got:  verbArgs(project, "info", ws),
			want: []string{"--project", project, "info", ws},
		},
		{
			name: "remount",
			got:  remountArgs(project, ws, "opencode", "workspace", "/tmp/wt-1"),
			want: []string{"--project", project, "remount", "taboo-run/opencode:workspace", "/tmp/wt-1"},
		},
		{
			name: "exec with timeout and env keys",
			got: execArgs(project, ws, execOptions{
				cwd:     "/workspace",
				timeout: 30 * time.Minute,
				envKeys: []string{"OPENROUTER_API_KEY"},
			}, []string{"opencode", "run", "-m", "openrouter/qwen/qwen3-coder-plus", "do the thing"}),
			want: []string{
				"--project", project, "exec",
				"--cwd", "/workspace",
				"--timeout", "30m0s",
				"--env", "OPENROUTER_API_KEY",
				ws, "--",
				"opencode", "run", "-m", "openrouter/qwen/qwen3-coder-plus", "do the thing",
			},
		},
		{
			name: "exec omits timeout when zero",
			got:  execArgs(project, ws, execOptions{cwd: "/workspace"}, []string{"true"}),
			want: []string{
				"--project", project, "exec",
				"--cwd", "/workspace",
				ws, "--", "true",
			},
		},
		{
			// An explicit assignment is passed as `--env NAME=VALUE` (value set,
			// not inherited) — how taboo points an agent's session-dir env var at
			// the mounted sessions path.
			name: "exec with explicit env assignment",
			got: execArgs(project, ws, execOptions{
				cwd: "/workspace",
				env: []envAssignment{{Name: "XDG_DATA_HOME", Value: "/sessions"}},
			}, []string{"opencode", "run"}),
			want: []string{
				"--project", project, "exec",
				"--cwd", "/workspace",
				"--env", "XDG_DATA_HOME=/sessions",
				ws, "--", "opencode", "run",
			},
		},
		{
			// Inherited keys come first, then explicit assignments, so the argv
			// is deterministic when an agent has both credentials and a session
			// redirect.
			name: "exec orders env keys before explicit assignments",
			got: execArgs(project, ws, execOptions{
				cwd:     "/workspace",
				envKeys: []string{"OPENROUTER_API_KEY"},
				env:     []envAssignment{{Name: "XDG_DATA_HOME", Value: "/sessions"}},
			}, []string{"true"}),
			want: []string{
				"--project", project, "exec",
				"--cwd", "/workspace",
				"--env", "OPENROUTER_API_KEY",
				"--env", "XDG_DATA_HOME=/sessions",
				ws, "--", "true",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !slices.Equal(tt.got, tt.want) {
				t.Errorf("\n got: %v\nwant: %v", tt.got, tt.want)
			}
		})
	}
}
