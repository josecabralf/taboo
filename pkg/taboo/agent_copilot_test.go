package taboo

import (
	"slices"
	"testing"
)

// A GPT model (not a Claude one) keeps the Copilot fixtures visually distinct
// from the claude-code suite.
const copilotModel = "gpt-5.4"

// copilotBaseArgv is the stable prefix every Copilot invocation carries.
// --allow-all is required for headless `-p` runs; the `git push` deny outranks it
// (deny beats allow, even --allow-all) so the worktree's shared host refs cannot
// be pushed; -s (silent) plus text output keep the orchestrator's completion scan
// and <result>{…}</result> extraction seeing clean agent text. Resume flags
// append after this prefix; the prompt rides last as the value of -p. Verified
// against `copilot --help` (v1.0.22).
var copilotBaseArgv = []string{
	"copilot", "--model", copilotModel,
	"--allow-all", "--deny-tool=shell(git push)",
	"--output-format", "text", "-s",
}

func TestCopilot_Name(t *testing.T) {
	if got := Copilot(copilotModel).Name(); got != "copilot" {
		t.Errorf("Name() = %q, want %q", got, "copilot")
	}
}

func TestCopilot_BuildCommand(t *testing.T) {
	ac := Copilot(copilotModel).BuildCommand(CommandOptions{Prompt: "do the thing"})

	// -p both carries the prompt and selects non-interactive mode.
	want := append(slices.Clone(copilotBaseArgv), "-p", "do the thing")
	if !slices.Equal(ac.Argv, want) {
		t.Errorf("Argv =\n  %v\nwant\n  %v", ac.Argv, want)
	}
	// Copilot delivers the prompt in argv, so Stdin stays empty; the stdin path
	// exists in the contract only for the stdin-delivery agents (ADR 0001).
	if ac.Stdin != "" {
		t.Errorf("Stdin = %q, want empty (Copilot delivers the prompt via -p in argv)", ac.Stdin)
	}
}

// Resume binds the session id as the =-attached `--resume=<id>`. The = form is
// required: bare `--resume` opens the interactive picker (commander.js
// optional-value rationale in BuildCommand's doc). ADR 0003 wrote `--resume <id>`
// schematically, but the real CLI documents the = form (verified, copilot 1.0.22).
func TestCopilot_BuildCommand_Resume(t *testing.T) {
	ac := Copilot(copilotModel).BuildCommand(CommandOptions{
		Prompt: "do the thing", ResumeSession: "ses_abc",
	})

	want := append(slices.Clone(copilotBaseArgv), "--resume=ses_abc", "-p", "do the thing")
	if !slices.Equal(ac.Argv, want) {
		t.Errorf("Argv =\n  %v\nwant\n  %v", ac.Argv, want)
	}
}

// TestCopilot_BuildCommand owns the canonical full-argv check; here only the
// model interpolation matters.
func TestCopilot_BuildCommand_UsesModel(t *testing.T) {
	ac := Copilot("claude-sonnet-4.6").BuildCommand(CommandOptions{Prompt: "go"})
	i := slices.Index(ac.Argv, "--model")
	if i < 0 || i+1 >= len(ac.Argv) || ac.Argv[i+1] != "claude-sonnet-4.6" {
		t.Errorf("Argv = %v, want --model followed by %q", ac.Argv, "claude-sonnet-4.6")
	}
}

// Copilot has no native headless fork (ADR 0003), so Fork is a documented no-op:
// with ResumeSession set, Fork=true renders the same argv as a plain resume.
// (taboo still isolates a fork at the orchestration level via a fresh
// branch+worktree; only the session-level fork is unavailable.) Guards against a
// future edit that adds a nonexistent Copilot fork flag.
func TestCopilot_BuildCommand_ForkIsNoOp(t *testing.T) {
	resume := Copilot(copilotModel).BuildCommand(CommandOptions{
		Prompt: "do the thing", ResumeSession: "ses_abc",
	})
	fork := Copilot(copilotModel).BuildCommand(CommandOptions{
		Prompt: "do the thing", ResumeSession: "ses_abc", Fork: true,
	})
	if !slices.Equal(fork.Argv, resume.Argv) {
		t.Errorf("Fork argv =\n  %v\nwant identical to plain resume\n  %v (Copilot has no native fork)", fork.Argv, resume.Argv)
	}
}

// Fork without a session to fork from is dropped; argv matches a plain fresh run.
func TestCopilot_BuildCommand_ForkWithoutResumeIgnored(t *testing.T) {
	ac := Copilot(copilotModel).BuildCommand(CommandOptions{Prompt: "go", Fork: true})

	want := append(slices.Clone(copilotBaseArgv), "-p", "go")
	if !slices.Equal(ac.Argv, want) {
		t.Errorf("Argv =\n  %v\nwant\n  %v (Fork without ResumeSession must be ignored)", ac.Argv, want)
	}
}

// Unlike OpenCode (which omits an empty positional prompt), Copilot always emits
// -p, so an empty prompt becomes a literal `-p ""`: omitting -p would launch the
// interactive TUI.
//
// Gotcha: at runtime `-p ""` is not a working run — Copilot exits 1 ("No prompt
// provided"), so a "just continue" resume must still supply an instruction. Not
// guarded at this layer (same posture as the Claude Code empty-prompt note).
func TestCopilot_BuildCommand_EmptyPrompt(t *testing.T) {
	ac := Copilot(copilotModel).BuildCommand(CommandOptions{ResumeSession: "ses_abc"})

	want := append(slices.Clone(copilotBaseArgv), "--resume=ses_abc", "-p", "")
	if !slices.Equal(ac.Argv, want) {
		t.Errorf("Argv =\n  %v\nwant\n  %v", ac.Argv, want)
	}
}

// Copilot authenticates with a GitHub token read from any of three env vars,
// listed in copilot's own precedence order (COPILOT_GITHUB_TOKEN > GH_TOKEN >
// GITHUB_TOKEN, ADR 0004).
func TestCopilot_CredentialEnvKeys(t *testing.T) {
	got := Copilot(copilotModel).CredentialEnvKeys()
	want := []string{"COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"}
	if !slices.Equal(got, want) {
		t.Errorf("CredentialEnvKeys() = %v, want %v", got, want)
	}
}

// Copilot has a redirectable session store: COPILOT_HOME relocates its whole
// config+state home and transcripts live under session-state/. Taboo points
// DirEnv at the host sessions mount so they survive the per-run rootfs wipe.
func TestCopilot_Sessions(t *testing.T) {
	spec, ok := Copilot(copilotModel).Sessions()
	if !ok {
		t.Fatal("Sessions() ok = false, want true (Copilot has a session store)")
	}
	want := SessionSpec{DirEnv: "COPILOT_HOME", Subdir: "session-state"}
	if spec != want {
		t.Errorf("Sessions() spec = %+v, want %+v", spec, want)
	}
}
