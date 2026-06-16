package taboo

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// writeConfig writes body to a taboo.yaml in a fresh temp dir and returns its
// path, so each test loads from a real file without touching the repo.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "taboo.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	return path
}

// TestLoadConfig_ResolvesTopLevelAgent is the tracer: a minimal valid config
// loads without error and resolves the top-level agent/model to a profile whose
// Name() matches the configured agent.
func TestLoadConfig_ResolvesTopLevelAgent(t *testing.T) {
	path := writeConfig(t, `
workshop: demo
base: ubuntu@24.04
repo: /home/me/repo
agent: claude-code
model: `+claudeCodeModel+`
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v, want nil", err)
	}
	if cfg.Profile == nil {
		t.Fatalf("cfg.Profile = nil, want resolved profile")
	}
	if got := cfg.Profile.Name(); got != "claude-code" {
		t.Errorf("cfg.Profile.Name() = %q, want %q", got, "claude-code")
	}
}

// TestLoadConfig_Strategy defaults Strategy to "branch" when omitted and
// otherwise preserves whatever is written — including a non-default value — so
// the field stays a forward-compatible seam rather than a closed enum.
func TestLoadConfig_Strategy(t *testing.T) {
	tests := []struct {
		name     string
		strategy string // line to inject, empty => omit the key entirely
		want     string
	}{
		{name: "omitted defaults to branch", strategy: "", want: "branch"},
		{name: "explicit branch preserved", strategy: "strategy: branch", want: "branch"},
		{name: "non-default preserved", strategy: "strategy: merge", want: "merge"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfig(t, "workshop: demo\n"+tt.strategy+"\n")
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatalf("LoadConfig() error = %v, want nil", err)
			}
			if cfg.Strategy != tt.want {
				t.Errorf("cfg.Strategy = %q, want %q", cfg.Strategy, tt.want)
			}
		})
	}
}

// TestLoadConfig_DefaultsBlock parses a full defaults block: the kebab-case
// scalar keys populate RunDefaults, the timeout string decodes through Duration,
// and default-workflow lands on the top-level config.
func TestLoadConfig_DefaultsBlock(t *testing.T) {
	path := writeConfig(t, `
workshop: demo
base: ubuntu@24.04
repo: /home/me/repo
agent: claude-code
model: `+claudeCodeModel+`
default-workflow: fix
defaults:
  branch-prefix: taboo/
  prompt: run the default task
  prompt-file: prompts/run.md
  timeout: 30m
  max-iterations: 5
  completion-signal: DONE
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v, want nil", err)
	}
	if cfg.Defaults == nil {
		t.Fatalf("cfg.Defaults = nil, want populated block")
	}
	if got, want := time.Duration(cfg.Defaults.Timeout), 30*time.Minute; got != want {
		t.Errorf("Defaults.Timeout = %v, want %v", got, want)
	}
	if got, want := cfg.Defaults.MaxIterations, 5; got != want {
		t.Errorf("Defaults.MaxIterations = %d, want %d", got, want)
	}
	if got, want := cfg.Defaults.CompletionSignal, "DONE"; got != want {
		t.Errorf("Defaults.CompletionSignal = %q, want %q", got, want)
	}
	if got, want := cfg.Defaults.BranchPrefix, "taboo/"; got != want {
		t.Errorf("Defaults.BranchPrefix = %q, want %q", got, want)
	}
	if got, want := cfg.Defaults.Prompt, "run the default task"; got != want {
		t.Errorf("Defaults.Prompt = %q, want %q", got, want)
	}
	if got, want := cfg.Defaults.PromptFile, "prompts/run.md"; got != want {
		t.Errorf("Defaults.PromptFile = %q, want %q", got, want)
	}
	if got, want := cfg.DefaultWorkflow, "fix"; got != want {
		t.Errorf("cfg.DefaultWorkflow = %q, want %q", got, want)
	}
}

// TestLoadConfig_MinimalOmitsOptionalBlocks leaves Defaults nil and Workflows
// empty when the optional blocks are absent, so callers can distinguish "no
// defaults" from "zeroed defaults".
func TestLoadConfig_MinimalOmitsOptionalBlocks(t *testing.T) {
	path := writeConfig(t, `
workshop: demo
base: ubuntu@24.04
repo: /home/me/repo
agent: claude-code
model: `+claudeCodeModel+`
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v, want nil", err)
	}
	if cfg.Defaults != nil {
		t.Errorf("cfg.Defaults = %+v, want nil when block omitted", cfg.Defaults)
	}
	if len(cfg.Workflows) != 0 {
		t.Errorf("len(cfg.Workflows) = %d, want 0 when block omitted", len(cfg.Workflows))
	}
}

// TestLoadConfig_EmptyFile treats an empty document as an empty config rather
// than an error: Decode returns io.EOF, Strategy still defaults to "branch", and
// with no agent the top-level Profile stays nil.
func TestLoadConfig_EmptyFile(t *testing.T) {
	path := writeConfig(t, "")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v, want nil on empty file", err)
	}
	if got, want := cfg.Strategy, "branch"; got != want {
		t.Errorf("cfg.Strategy = %q, want %q", got, want)
	}
	if cfg.Profile != nil {
		t.Errorf("cfg.Profile = %v, want nil with no agent", cfg.Profile)
	}
}

// TestLoadConfig_WorkflowProfileResolution resolves each workflow's effective
// profile from its own agent/model when set, falling back to the top level
// otherwise. The expected agent is asserted via Profile.Name(), and the expected
// model is proven to thread through the built invocation's argv (the same
// technique registry_test.go uses), so a wrong agent or a dropped model is
// caught.
func TestLoadConfig_WorkflowProfileResolution(t *testing.T) {
	tests := []struct {
		name      string
		workflow  string // YAML body under "workflows:\n  task:\n"
		wantAgent string
		wantModel string
	}{
		{
			name:      "overrides agent and model",
			workflow:  "    agent: copilot\n    model: " + copilotModel + "\n",
			wantAgent: "copilot",
			wantModel: copilotModel,
		},
		{
			name:      "inherits top-level agent and model",
			workflow:  "    prompt: just do it\n",
			wantAgent: "claude-code",
			wantModel: claudeCodeModel,
		},
		{
			name:      "overrides model only, keeps top-level agent",
			workflow:  "    model: claude-opus-4-9\n",
			wantAgent: "claude-code",
			wantModel: "claude-opus-4-9",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfig(t, `
workshop: demo
base: ubuntu@24.04
repo: /home/me/repo
agent: claude-code
model: `+claudeCodeModel+`
workflows:
  task:
`+tt.workflow)
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatalf("LoadConfig() error = %v, want nil", err)
			}
			wf, ok := cfg.Workflows["task"]
			if !ok {
				t.Fatalf("cfg.Workflows[%q] missing", "task")
			}
			if wf.Profile == nil {
				t.Fatalf("workflow Profile = nil, want resolved profile")
			}
			if got := wf.Profile.Name(); got != tt.wantAgent {
				t.Errorf("workflow Profile.Name() = %q, want %q", got, tt.wantAgent)
			}
			argv := wf.Profile.BuildCommand(CommandOptions{Prompt: "x"}).Argv
			if !slices.Contains(argv, tt.wantModel) {
				t.Errorf("workflow argv = %v, want it to carry model %q", argv, tt.wantModel)
			}
		})
	}
}

// TestLoadConfig_WorkflowScalars pins each scalar key of a workflow block to its
// struct field: prompt, prompt-file, max-iterations, and the Duration timeout all
// decode through their kebab-case YAML tags. The max-iterations key is shared
// with the defaults block, so a workflow overrides the default cap under the same
// name.
func TestLoadConfig_WorkflowScalars(t *testing.T) {
	path := writeConfig(t, `
workshop: demo
base: ubuntu@24.04
repo: /home/me/repo
agent: claude-code
model: `+claudeCodeModel+`
workflows:
  fix:
    prompt: fix the bug
    prompt-file: prompts/fix.md
    max-iterations: 7
    timeout: 45m
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v, want nil", err)
	}
	wf, ok := cfg.Workflows["fix"]
	if !ok {
		t.Fatalf("cfg.Workflows[%q] missing", "fix")
	}
	if got, want := wf.Prompt, "fix the bug"; got != want {
		t.Errorf("workflow Prompt = %q, want %q", got, want)
	}
	if got, want := wf.PromptFile, "prompts/fix.md"; got != want {
		t.Errorf("workflow PromptFile = %q, want %q", got, want)
	}
	if got, want := wf.MaxIterations, 7; got != want {
		t.Errorf("workflow MaxIterations = %d, want %d", got, want)
	}
	if got, want := time.Duration(wf.Timeout), 45*time.Minute; got != want {
		t.Errorf("workflow Timeout = %v, want %v", got, want)
	}
}

// TestLoadConfig_UnknownTopLevelAgent surfaces an unresolvable top-level agent
// as a wrapped ErrUnknownAgent, with a message that names both the offending
// agent and the config path so the CLI can quote them back.
func TestLoadConfig_UnknownTopLevelAgent(t *testing.T) {
	path := writeConfig(t, `
workshop: demo
agent: gemini
model: g-1
`)
	_, err := LoadConfig(path)
	if !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("error = %v, want errors.Is(err, ErrUnknownAgent)", err)
	}
	if !strings.Contains(err.Error(), "gemini") {
		t.Errorf("error %q, want it to name the unknown agent %q", err, "gemini")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q, want it to name the config path %q", err, path)
	}
}

// TestLoadConfig_UnknownWorkflowAgent surfaces an unresolvable workflow agent as
// a wrapped ErrUnknownAgent whose message names the workflow key, so the failure
// points at the offending block.
func TestLoadConfig_UnknownWorkflowAgent(t *testing.T) {
	path := writeConfig(t, `
workshop: demo
agent: claude-code
model: `+claudeCodeModel+`
workflows:
  broken:
    agent: gemini
`)
	_, err := LoadConfig(path)
	if !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("error = %v, want errors.Is(err, ErrUnknownAgent)", err)
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Errorf("error %q, want it to name the workflow key %q", err, "broken")
	}
}

// TestLoadConfig_NoAgentAnywhere leaves every Profile nil without erroring when
// no agent is configured at the top level and a workflow declares none either:
// enforcing the required field is a later validate command's job, not the
// loader's.
func TestLoadConfig_NoAgentAnywhere(t *testing.T) {
	path := writeConfig(t, `
workshop: demo
workflows:
  task:
    prompt: do the thing
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v, want nil with no agent anywhere", err)
	}
	if cfg.Profile != nil {
		t.Errorf("cfg.Profile = %v, want nil with no top-level agent", cfg.Profile)
	}
	wf, ok := cfg.Workflows["task"]
	if !ok {
		t.Fatalf("cfg.Workflows[%q] missing", "task")
	}
	if wf.Profile != nil {
		t.Errorf("workflow Profile = %v, want nil with no agent", wf.Profile)
	}
}

// TestLoadConfig_BadDuration rejects a malformed timeout as a wrapped
// ErrConfigParse whose message echoes the offending value, surfacing the
// Duration unmarshaler's parse failure through the decoder.
func TestLoadConfig_BadDuration(t *testing.T) {
	path := writeConfig(t, `
workshop: demo
defaults:
  timeout: not-a-duration
`)
	_, err := LoadConfig(path)
	if !errors.Is(err, ErrConfigParse) {
		t.Fatalf("error = %v, want errors.Is(err, ErrConfigParse)", err)
	}
	if !strings.Contains(err.Error(), "not-a-duration") {
		t.Errorf("error %q, want it to name the bad value %q", err, "not-a-duration")
	}
}

// TestLoadConfig_NonScalarTimeout rejects a timeout given a mapping as a wrapped
// ErrConfigParse. This hits the Duration unmarshaler's value.Decode error exit
// (the other branch from the time.ParseDuration failure TestLoadConfig_BadDuration
// covers), proving a structurally invalid timeout surfaces through the strict
// decoder rather than being silently admitted.
func TestLoadConfig_NonScalarTimeout(t *testing.T) {
	path := writeConfig(t, `
workshop: demo
defaults:
  timeout:
    nested: oops
`)
	_, err := LoadConfig(path)
	if !errors.Is(err, ErrConfigParse) {
		t.Fatalf("error = %v, want errors.Is(err, ErrConfigParse)", err)
	}
}

// TestLoadConfig_UnknownFieldRejected proves the schema admits only the known
// scalar/file-path keys: an unknown key, whether at the top level or nested
// inside a workflow, is a wrapped ErrConfigParse rather than a silent drop.
func TestLoadConfig_UnknownFieldRejected(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "unknown top-level scalar",
			body: "workshop: demo\nfan-out: 3\n",
		},
		{
			name: "unknown key inside workflow",
			body: "workshop: demo\nworkflows:\n  task:\n    bogus: nope\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadConfig(writeConfig(t, tt.body))
			if !errors.Is(err, ErrConfigParse) {
				t.Errorf("error = %v, want errors.Is(err, ErrConfigParse)", err)
			}
		})
	}
}

// TestLoadConfig_MissingFile maps an unreadable path to the read sentinel, kept
// distinct from a parse failure so the CLI can tell "no such file" apart from
// "bad contents".
func TestLoadConfig_MissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	_, err := LoadConfig(path)
	if !errors.Is(err, ErrConfigRead) {
		t.Errorf("error = %v, want errors.Is(err, ErrConfigRead)", err)
	}
}

// TestLoadConfig_MalformedYAML maps a syntactically broken document to the parse
// sentinel, with a message that names the config path so the CLI can quote it
// back when reporting where the bad document lives.
func TestLoadConfig_MalformedYAML(t *testing.T) {
	path := writeConfig(t, `workshop: "[unterminated`)
	_, err := LoadConfig(path)
	if !errors.Is(err, ErrConfigParse) {
		t.Errorf("error = %v, want errors.Is(err, ErrConfigParse)", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q, want it to name the config path %q", err, path)
	}
}

// TestLoadConfig_MultipleDocuments rejects a config packing more than one YAML
// document (separated by "---") as a wrapped ErrConfigParse, rather than silently
// loading only the first and dropping the rest — matching the loader's strict,
// no-silent-surprises posture, and naming the path so the CLI can quote it.
func TestLoadConfig_MultipleDocuments(t *testing.T) {
	path := writeConfig(t, `
workshop: demo
agent: claude-code
model: `+claudeCodeModel+`
---
workshop: second
`)
	_, err := LoadConfig(path)
	if !errors.Is(err, ErrConfigParse) {
		t.Fatalf("error = %v, want errors.Is(err, ErrConfigParse)", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q, want it to name the config path %q", err, path)
	}
}

// TestDuration_UnmarshalYAML decodes Go duration strings, and treats the empty
// string as zero, so config timeouts read naturally as "30m" / "1h30m".
func TestDuration_UnmarshalYAML(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want time.Duration
	}{
		{name: "minutes", yaml: "30m", want: 30 * time.Minute},
		{name: "compound", yaml: "1h30m", want: 90 * time.Minute},
		{name: "empty is zero", yaml: `""`, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d Duration
			if err := yaml.Unmarshal([]byte("d: "+tt.yaml+"\n"), &struct {
				D *Duration `yaml:"d"`
			}{D: &d}); err != nil {
				t.Fatalf("yaml.Unmarshal(%q) error = %v", tt.yaml, err)
			}
			if got := time.Duration(d); got != tt.want {
				t.Errorf("Duration = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestDuration_MarshalYAML pins taboo's marshaler output directly: a Duration
// renders as the canonical Go duration string, so a written config reads back as
// the human-friendly form rather than a raw nanosecond count.
func TestDuration_MarshalYAML(t *testing.T) {
	got, err := Duration(90 * time.Minute).MarshalYAML()
	if err != nil {
		t.Fatalf("MarshalYAML() error = %v", err)
	}
	s, ok := got.(string)
	if !ok {
		t.Fatalf("MarshalYAML() returned %T, want string", got)
	}
	if want := "1h30m0s"; s != want {
		t.Errorf("MarshalYAML() = %q, want %q", s, want)
	}
}
