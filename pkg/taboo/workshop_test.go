package taboo

import (
	"slices"
	"testing"
	"time"
)

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
			got:  launchArgs(project, ws),
			want: []string{"--project", project, "launch", ws},
		},
		{
			name: "stop",
			got:  stopArgs(project, ws),
			want: []string{"--project", project, "stop", ws},
		},
		{
			name: "start",
			got:  startArgs(project, ws),
			want: []string{"--project", project, "start", ws},
		},
		{
			name: "info",
			got:  infoArgs(project, ws),
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !slices.Equal(tt.got, tt.want) {
				t.Errorf("\n got: %v\nwant: %v", tt.got, tt.want)
			}
		})
	}
}
