package taboo

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// parsedDefinition mirrors the shape of a workshop definition just enough to
// assert on what the `workshop` CLI will consume.
type parsedDefinition struct {
	Name string `yaml:"name"`
	Base string `yaml:"base"`
	SDKs []struct {
		Name  string `yaml:"name"`
		Plugs map[string]struct {
			Interface      string `yaml:"interface"`
			WorkshopTarget string `yaml:"workshop-target"`
			ReadOnly       bool   `yaml:"read-only"`
		} `yaml:"plugs"`
	} `yaml:"sdks"`
}

func TestRenderDefinition_ProducesTwoMountPlugsOnAgentSDK(t *testing.T) {
	cfg := Config{
		Workshop: "taboo-run",
		Base:     "ubuntu@24.04",
		Agent:    OpenCode(openCodeModel),
		RepoPath: "/home/dev/repos/myproject",
	}

	out, err := renderDefinition(cfg)
	if err != nil {
		t.Fatalf("renderDefinition: %v", err)
	}

	var def parsedDefinition
	if err := yaml.Unmarshal([]byte(out), &def); err != nil {
		t.Fatalf("rendered definition is not valid YAML: %v\n%s", err, out)
	}

	if def.Name != "taboo-run" {
		t.Errorf("name = %q, want taboo-run", def.Name)
	}
	if def.Base != "ubuntu@24.04" {
		t.Errorf("base = %q, want ubuntu@24.04", def.Base)
	}
	if len(def.SDKs) != 1 {
		t.Fatalf("got %d SDKs, want 1", len(def.SDKs))
	}
	sdk := def.SDKs[0]
	// In-project SDKs (shipped by taboo under .workshop/<name>/) are referenced
	// with a "project-" prefix in the definition; workshop resolves them to the
	// bare name for remount/info.
	if sdk.Name != "project-opencode" {
		t.Errorf("sdk name = %q, want project-opencode", sdk.Name)
	}

	ws, ok := sdk.Plugs["workspace"]
	if !ok {
		t.Fatal("missing workspace plug")
	}
	if ws.Interface != "mount" || ws.WorkshopTarget != "/workspace" {
		t.Errorf("workspace plug = %+v, want mount→/workspace", ws)
	}

	// The two-mount rule: gitcommon target must equal the host repo's .git
	// absolute path so the worktree's .git pointer resolves identically inside.
	gc, ok := sdk.Plugs["gitcommon"]
	if !ok {
		t.Fatal("missing gitcommon plug")
	}
	if gc.Interface != "mount" || gc.WorkshopTarget != "/home/dev/repos/myproject/.git" {
		t.Errorf("gitcommon plug = %+v, want mount→/home/dev/repos/myproject/.git", gc)
	}
}

// A session-capable agent (OpenCode) gets a third mount plug binding a host
// sessions directory into the workshop, so session files written inside survive
// the per-run stop/remount/start swap (which wipes the rootfs).
func TestRenderDefinition_AddsSessionsPlugForSessionCapableAgent(t *testing.T) {
	cfg := Config{
		Workshop: "taboo-run",
		Base:     "ubuntu@24.04",
		Agent:    OpenCode(openCodeModel),
		RepoPath: "/home/dev/repos/myproject",
	}

	out, err := renderDefinition(cfg)
	if err != nil {
		t.Fatalf("renderDefinition: %v", err)
	}

	var def parsedDefinition
	if err := yaml.Unmarshal([]byte(out), &def); err != nil {
		t.Fatalf("rendered definition is not valid YAML: %v\n%s", err, out)
	}
	if len(def.SDKs) != 1 {
		t.Fatalf("got %d SDKs, want 1", len(def.SDKs))
	}

	s, ok := def.SDKs[0].Plugs["sessions"]
	if !ok {
		t.Fatal("missing sessions plug")
	}
	if s.Interface != "mount" || s.WorkshopTarget != "/sessions" {
		t.Errorf("sessions plug = %+v, want mount→/sessions", s)
	}
}

// An agent with no session store gets no sessions plug: there is nothing to
// persist, so taboo does not mount a sessions directory for it.
func TestRenderDefinition_OmitsSessionsPlugForSessionlessAgent(t *testing.T) {
	cfg := Config{
		Workshop: "taboo-run",
		Base:     "ubuntu@24.04",
		Agent:    stdinProfile{}, // Sessions() ok == false
		RepoPath: "/home/dev/repos/myproject",
	}

	out, err := renderDefinition(cfg)
	if err != nil {
		t.Fatalf("renderDefinition: %v", err)
	}

	var def parsedDefinition
	if err := yaml.Unmarshal([]byte(out), &def); err != nil {
		t.Fatalf("rendered definition is not valid YAML: %v\n%s", err, out)
	}
	if len(def.SDKs) != 1 {
		t.Fatalf("got %d SDKs, want 1", len(def.SDKs))
	}
	if _, ok := def.SDKs[0].Plugs["sessions"]; ok {
		t.Error("sessions plug present for a sessionless agent, want none")
	}
}
