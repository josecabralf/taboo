package taboo

import (
	"slices"
	"testing"
)

// The model OpenCode is configured with, reused across the assertions below
// (and by runner_test.go / template_test.go, which share the package test scope).
const openCodeModel = "openrouter/qwen/qwen3-coder-plus"

func TestOpenCode_Name(t *testing.T) {
	if got := OpenCode(openCodeModel).Name(); got != "opencode" {
		t.Errorf("Name() = %q, want %q", got, "opencode")
	}
}

func TestOpenCode_BuildCommand(t *testing.T) {
	ac := OpenCode(openCodeModel).BuildCommand(CommandOptions{Prompt: "do the thing"})

	want := []string{"opencode", "run", "--log-level", "ERROR", "-m", openCodeModel, "do the thing"}
	if !slices.Equal(ac.Argv, want) {
		t.Errorf("Argv =\n  %v\nwant\n  %v", ac.Argv, want)
	}
	// OpenCode delivers the prompt positionally in argv, so Stdin stays empty;
	// the stdin path exists in the contract for the stdin-delivery agents.
	if ac.Stdin != "" {
		t.Errorf("Stdin = %q, want empty (OpenCode delivers the prompt in argv)", ac.Stdin)
	}
}

// The model is interpolated into argv so distinct models produce distinct
// commands; nothing else is parameterized this slice.
func TestOpenCode_BuildCommand_UsesModel(t *testing.T) {
	ac := OpenCode("anthropic/claude").BuildCommand(CommandOptions{Prompt: "go"})
	// Only the model interpolation is under test here; TestOpenCode_BuildCommand
	// owns the one canonical full-argv check. Assert the configured model rides in
	// argv right after -m, so distinct models yield distinct commands without
	// re-pinning the stable prefix.
	i := slices.Index(ac.Argv, "-m")
	if i < 0 || i+1 >= len(ac.Argv) || ac.Argv[i+1] != "anthropic/claude" {
		t.Errorf("Argv = %v, want -m followed by %q", ac.Argv, "anthropic/claude")
	}
}

// Resume threads the session id into argv as `--session <id>`, placed after the
// model flag and before the positional prompt, so OpenCode continues that prior
// session instead of starting fresh.
func TestOpenCode_BuildCommand_Resume(t *testing.T) {
	ac := OpenCode(openCodeModel).BuildCommand(CommandOptions{
		Prompt: "do the thing", ResumeSession: "ses_abc",
	})

	want := []string{"opencode", "run", "--log-level", "ERROR", "-m", openCodeModel, "--session", "ses_abc", "do the thing"}
	if !slices.Equal(ac.Argv, want) {
		t.Errorf("Argv =\n  %v\nwant\n  %v", ac.Argv, want)
	}
}

// Fork rides on top of resume: with both set, argv carries `--session <id>
// --fork` so OpenCode forks that session into a new one (source untouched). The
// prompt stays positional last.
func TestOpenCode_BuildCommand_Fork(t *testing.T) {
	ac := OpenCode(openCodeModel).BuildCommand(CommandOptions{
		Prompt: "do the thing", ResumeSession: "ses_abc", Fork: true,
	})

	want := []string{"opencode", "run", "--log-level", "ERROR", "-m", openCodeModel, "--session", "ses_abc", "--fork", "do the thing"}
	if !slices.Equal(ac.Argv, want) {
		t.Errorf("Argv =\n  %v\nwant\n  %v", ac.Argv, want)
	}
}

// Fork is meaningless without a session to fork from: OpenCode's --fork only
// applies when continuing a session, so Fork without ResumeSession is dropped and
// argv matches a plain fresh run.
func TestOpenCode_BuildCommand_ForkWithoutResumeIgnored(t *testing.T) {
	ac := OpenCode(openCodeModel).BuildCommand(CommandOptions{Prompt: "go", Fork: true})

	want := []string{"opencode", "run", "--log-level", "ERROR", "-m", openCodeModel, "go"}
	if !slices.Equal(ac.Argv, want) {
		t.Errorf("Argv =\n  %v\nwant\n  %v (Fork without ResumeSession must be ignored)", ac.Argv, want)
	}
}

// An empty prompt is omitted from argv rather than passed as a stray empty
// positional: a "just continue, no new instruction" resume renders the resume
// flags with no trailing "" argument.
func TestOpenCode_BuildCommand_EmptyPromptOmitted(t *testing.T) {
	ac := OpenCode(openCodeModel).BuildCommand(CommandOptions{ResumeSession: "ses_abc"})

	want := []string{"opencode", "run", "--log-level", "ERROR", "-m", openCodeModel, "--session", "ses_abc"}
	if !slices.Equal(ac.Argv, want) {
		t.Errorf("Argv =\n  %v\nwant\n  %v (empty prompt must not add a trailing \"\")", ac.Argv, want)
	}
}

func TestOpenCode_CredentialEnvKeys(t *testing.T) {
	got := OpenCode(openCodeModel).CredentialEnvKeys()
	want := []string{"OPENROUTER_API_KEY"}
	if !slices.Equal(got, want) {
		t.Errorf("CredentialEnvKeys() = %v, want %v", got, want)
	}
}

func TestOpenCode_Sessions(t *testing.T) {
	spec, ok := OpenCode(openCodeModel).Sessions()
	if !ok {
		t.Fatal("Sessions() ok = false, want true (OpenCode has a session store)")
	}
	want := SessionSpec{DirEnv: "XDG_DATA_HOME", Subdir: "opencode"}
	if spec != want {
		t.Errorf("Sessions() spec = %+v, want %+v", spec, want)
	}
}
