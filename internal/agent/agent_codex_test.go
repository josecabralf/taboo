package agent

import (
	"slices"
	"testing"
)

// Also used by registry_test.go / modelhint_test.go, which share the package
// test scope.
const codexModel = "gpt-5-codex"

func TestCodex_Name(t *testing.T) {
	if got := NewCodex(codexModel).Name(); got != Codex {
		t.Errorf("Name() = %q, want %q", got, Codex)
	}
}

// Codex delivers the prompt on stdin (ADR 0001), so argv carries the `-` stdin
// sentinel as the prompt positional and Stdin holds the prompt itself.
func TestCodex_BuildCommand(t *testing.T) {
	ac := NewCodex(codexModel).BuildCommand(CommandOptions{Prompt: "do the thing"})

	want := []string{"codex", "exec", "--dangerously-bypass-approvals-and-sandbox", "--model", codexModel, "-"}
	if !slices.Equal(ac.Argv, want) {
		t.Errorf("Argv =\n  %v\nwant\n  %v", ac.Argv, want)
	}
	if ac.Stdin != "do the thing" {
		t.Errorf("Stdin = %q, want %q (Codex reads the prompt from stdin)", ac.Stdin, "do the thing")
	}
}

func TestCodex_BuildCommand_UsesModel(t *testing.T) {
	ac := NewCodex("o4-mini").BuildCommand(CommandOptions{Prompt: "go"})
	// TestCodex_BuildCommand owns the canonical full-argv check; here only the
	// model interpolation matters.
	i := slices.Index(ac.Argv, "--model")
	if i < 0 || i+1 >= len(ac.Argv) || ac.Argv[i+1] != "o4-mini" {
		t.Errorf("Argv = %v, want --model followed by %q", ac.Argv, "o4-mini")
	}
}

// Resume maps to the `exec resume <id>` subcommand (ADR 0003); the prompt still
// rides on stdin via the trailing `-`.
func TestCodex_BuildCommand_Resume(t *testing.T) {
	ac := NewCodex(codexModel).BuildCommand(CommandOptions{
		Prompt: "do the thing", ResumeSession: "0199abc",
	})

	want := []string{"codex", "exec", "resume", "0199abc", "--dangerously-bypass-approvals-and-sandbox", "--model", codexModel, "-"}
	if !slices.Equal(ac.Argv, want) {
		t.Errorf("Argv =\n  %v\nwant\n  %v", ac.Argv, want)
	}
	if ac.Stdin != "do the thing" {
		t.Errorf("Stdin = %q, want %q", ac.Stdin, "do the thing")
	}
}

// Codex has no native headless fork (ADR 0003), so Fork is dropped: a resume with
// Fork renders identically to a plain resume, and isolation degrades to the fresh
// branch/worktree taboo already allocates.
func TestCodex_BuildCommand_ForkDegradesToResume(t *testing.T) {
	ac := NewCodex(codexModel).BuildCommand(CommandOptions{
		Prompt: "do the thing", ResumeSession: "0199abc", Fork: true,
	})

	want := []string{"codex", "exec", "resume", "0199abc", "--dangerously-bypass-approvals-and-sandbox", "--model", codexModel, "-"}
	if !slices.Equal(ac.Argv, want) {
		t.Errorf("Argv =\n  %v\nwant\n  %v (Codex has no headless fork; Fork must be a no-op)", ac.Argv, want)
	}
}

// An empty prompt still delivers via stdin (empty Stdin) with the `-` sentinel
// kept: a "just continue" resume must not drop into Codex's interactive TUI.
func TestCodex_BuildCommand_EmptyPromptStdin(t *testing.T) {
	ac := NewCodex(codexModel).BuildCommand(CommandOptions{ResumeSession: "0199abc"})

	want := []string{"codex", "exec", "resume", "0199abc", "--dangerously-bypass-approvals-and-sandbox", "--model", codexModel, "-"}
	if !slices.Equal(ac.Argv, want) {
		t.Errorf("Argv =\n  %v\nwant\n  %v", ac.Argv, want)
	}
	if ac.Stdin != "" {
		t.Errorf("Stdin = %q, want empty", ac.Stdin)
	}
}

func TestCodex_CredentialEnvKeys(t *testing.T) {
	got := NewCodex(codexModel).CredentialEnvKeys()
	want := []string{"OPENAI_API_KEY"}
	if !slices.Equal(got, want) {
		t.Errorf("CredentialEnvKeys() = %v, want %v", got, want)
	}
}

func TestCodex_Sessions(t *testing.T) {
	spec, ok := NewCodex(codexModel).Sessions()
	if !ok {
		t.Fatal("Sessions() ok = false, want true (Codex has a session store)")
	}
	want := SessionSpec{DirEnv: "CODEX_HOME", Subdir: "sessions"}
	if spec != want {
		t.Errorf("Sessions() spec = %+v, want %+v", spec, want)
	}
}
