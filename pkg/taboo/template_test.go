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
		SDK:      "opencode",
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
