package agent

import (
	"slices"
	"testing"
)

// piModel is a verified pi model slug: pi addresses OpenRouter models with the
// provider-prefixed `provider/id` form, matching the OPENROUTER_API_KEY credential
// this profile forwards.
const piModel = "openrouter/qwen/qwen3-coder-plus"

func TestPi_Name(t *testing.T) {
	if got := NewPi(piModel).Name(); got != Pi {
		t.Errorf("Name() = %q, want %q", got, Pi)
	}
}

// The canonical full-argv assertion: print mode (-p), --approve to trust the
// project for the headless run, --model, and the prompt on stdin (never argv).
func TestPi_BuildCommand(t *testing.T) {
	ac := NewPi(piModel).BuildCommand(CommandOptions{Prompt: "do the thing"})

	want := []string{"pi", "-p", "--approve", "--model", piModel}
	if !slices.Equal(ac.Argv, want) {
		t.Errorf("Argv =\n  %v\nwant\n  %v", ac.Argv, want)
	}
	// Pi delivers the prompt on stdin (ADR 0001), never in argv.
	if ac.Stdin != "do the thing" {
		t.Errorf("Stdin = %q, want %q (pi delivers the prompt on stdin)", ac.Stdin, "do the thing")
	}
}

func TestPi_BuildCommand_UsesModel(t *testing.T) {
	ac := NewPi("anthropic/claude").BuildCommand(CommandOptions{Prompt: "go"})
	// TestPi_BuildCommand owns the canonical full-argv check; here only the model
	// interpolation matters.
	i := slices.Index(ac.Argv, "--model")
	if i < 0 || i+1 >= len(ac.Argv) || ac.Argv[i+1] != "anthropic/claude" {
		t.Errorf("Argv = %v, want --model followed by %q", ac.Argv, "anthropic/claude")
	}
}

// Resume threads the session id into argv as `--session <id>`; the prompt still
// rides on stdin.
func TestPi_BuildCommand_Resume(t *testing.T) {
	ac := NewPi(piModel).BuildCommand(CommandOptions{
		Prompt: "do the thing", ResumeSession: "ses_abc",
	})

	want := []string{"pi", "-p", "--approve", "--model", piModel, "--session", "ses_abc"}
	if !slices.Equal(ac.Argv, want) {
		t.Errorf("Argv =\n  %v\nwant\n  %v", ac.Argv, want)
	}
	if ac.Stdin != "do the thing" {
		t.Errorf("Stdin = %q, want %q", ac.Stdin, "do the thing")
	}
}

// Fork takes the source id as its argument and replaces --session: pi's
// `--fork <id>` writes the continuation to a new session file, leaving the source
// untouched (ADR 0003).
func TestPi_BuildCommand_Fork(t *testing.T) {
	ac := NewPi(piModel).BuildCommand(CommandOptions{
		Prompt: "do the thing", ResumeSession: "ses_abc", Fork: true,
	})

	want := []string{"pi", "-p", "--approve", "--model", piModel, "--fork", "ses_abc"}
	if !slices.Equal(ac.Argv, want) {
		t.Errorf("Argv =\n  %v\nwant\n  %v", ac.Argv, want)
	}
}

// Fork without a session to fork from is dropped: --fork needs the source id, so
// argv matches a plain fresh run.
func TestPi_BuildCommand_ForkWithoutResumeIgnored(t *testing.T) {
	ac := NewPi(piModel).BuildCommand(CommandOptions{Prompt: "go", Fork: true})

	want := []string{"pi", "-p", "--approve", "--model", piModel}
	if !slices.Equal(ac.Argv, want) {
		t.Errorf("Argv =\n  %v\nwant\n  %v (Fork without ResumeSession must be ignored)", ac.Argv, want)
	}
}

// An empty prompt simply pipes empty stdin — no stray positional, and resume still
// renders cleanly.
func TestPi_BuildCommand_EmptyPromptStdin(t *testing.T) {
	ac := NewPi(piModel).BuildCommand(CommandOptions{ResumeSession: "ses_abc"})

	want := []string{"pi", "-p", "--approve", "--model", piModel, "--session", "ses_abc"}
	if !slices.Equal(ac.Argv, want) {
		t.Errorf("Argv =\n  %v\nwant\n  %v", ac.Argv, want)
	}
	if ac.Stdin != "" {
		t.Errorf("Stdin = %q, want empty", ac.Stdin)
	}
}

func TestPi_CredentialEnvKeys(t *testing.T) {
	got := NewPi(piModel).CredentialEnvKeys()
	want := []string{"OPENROUTER_API_KEY"}
	if !slices.Equal(got, want) {
		t.Errorf("CredentialEnvKeys() = %v, want %v", got, want)
	}
}

// Pi has a redirectable session store: PI_CODING_AGENT_SESSION_DIR relocates the
// sessions dir, and pi writes session files directly under it (Subdir empty).
func TestPi_Sessions(t *testing.T) {
	spec, ok := NewPi(piModel).Sessions()
	if !ok {
		t.Fatal("Sessions() ok = false, want true (Pi has a session store)")
	}
	want := SessionSpec{DirEnv: "PI_CODING_AGENT_SESSION_DIR", Subdir: ""}
	if spec != want {
		t.Errorf("Sessions() spec = %+v, want %+v", spec, want)
	}
}
