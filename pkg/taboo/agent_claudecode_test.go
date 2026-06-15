package taboo

import (
	"slices"
	"testing"
)

// The model Claude Code is configured with, reused across the assertions below.
const claudeCodeModel = "claude-sonnet-4-6"

// claudeBaseArgv is the stable prefix every Claude Code invocation carries:
// headless print mode, literal text output, the configured model, auto
// permission mode (so the headless agent may edit/commit without an approver),
// and the hard `git push` deny (the worktree shares the host repo's refs).
// Resume/fork flags, when present, append after this prefix.
var claudeBaseArgv = []string{
	"claude", "-p", "--output-format", "text", "--model", claudeCodeModel,
	"--permission-mode", "auto", "--disallowedTools", "Bash(git push *)",
}

func TestClaudeCode_Name(t *testing.T) {
	if got := ClaudeCode(claudeCodeModel).Name(); got != "claude-code" {
		t.Errorf("Name() = %q, want %q", got, "claude-code")
	}
}

func TestClaudeCode_BuildCommand(t *testing.T) {
	ac := ClaudeCode(claudeCodeModel).BuildCommand(CommandOptions{Prompt: "do the thing"})

	want := claudeBaseArgv
	if !slices.Equal(ac.Argv, want) {
		t.Errorf("Argv =\n  %v\nwant\n  %v", ac.Argv, want)
	}
	// Claude Code delivers the prompt on stdin (ADR 0001), so it never enters
	// argv; the runner pipes Stdin to `claude -p`.
	if ac.Stdin != "do the thing" {
		t.Errorf("Stdin = %q, want %q (Claude Code delivers the prompt on stdin)", ac.Stdin, "do the thing")
	}
}

// The model is interpolated into argv so distinct models produce distinct
// commands; nothing else is parameterized this slice.
func TestClaudeCode_BuildCommand_UsesModel(t *testing.T) {
	ac := ClaudeCode("claude-opus-4-8").BuildCommand(CommandOptions{Prompt: "go"})
	// Only the model interpolation is under test here; TestClaudeCode_BuildCommand
	// owns the one canonical full-argv check. Assert the configured model rides in
	// argv right after --model, so distinct models yield distinct commands without
	// re-pinning the stable prefix.
	i := slices.Index(ac.Argv, "--model")
	if i < 0 || i+1 >= len(ac.Argv) || ac.Argv[i+1] != "claude-opus-4-8" {
		t.Errorf("Argv = %v, want --model followed by %q", ac.Argv, "claude-opus-4-8")
	}
}

// Resume threads the session id into argv as `--resume <id>` (ADR 0003), placed
// after the model flag, so Claude Code continues that prior session instead of
// starting fresh. The prompt stays on stdin.
func TestClaudeCode_BuildCommand_Resume(t *testing.T) {
	ac := ClaudeCode(claudeCodeModel).BuildCommand(CommandOptions{
		Prompt: "do the thing", ResumeSession: "ses_abc",
	})

	want := append(slices.Clone(claudeBaseArgv), "--resume", "ses_abc")
	if !slices.Equal(ac.Argv, want) {
		t.Errorf("Argv =\n  %v\nwant\n  %v", ac.Argv, want)
	}
	if ac.Stdin != "do the thing" {
		t.Errorf("Stdin = %q, want %q", ac.Stdin, "do the thing")
	}
}

// Fork rides on top of resume: with both set, argv carries `--resume <id>
// --fork-session` (ADR 0003) so Claude Code forks that session into a new one,
// leaving the source conversation untouched. The prompt stays on stdin.
func TestClaudeCode_BuildCommand_Fork(t *testing.T) {
	ac := ClaudeCode(claudeCodeModel).BuildCommand(CommandOptions{
		Prompt: "do the thing", ResumeSession: "ses_abc", Fork: true,
	})

	want := append(slices.Clone(claudeBaseArgv), "--resume", "ses_abc", "--fork-session")
	if !slices.Equal(ac.Argv, want) {
		t.Errorf("Argv =\n  %v\nwant\n  %v", ac.Argv, want)
	}
}

// Fork is meaningless without a session to fork from: --fork-session only applies
// when continuing a session, so Fork without ResumeSession is dropped and argv
// matches a plain fresh run.
func TestClaudeCode_BuildCommand_ForkWithoutResumeIgnored(t *testing.T) {
	ac := ClaudeCode(claudeCodeModel).BuildCommand(CommandOptions{Prompt: "go", Fork: true})

	want := claudeBaseArgv
	if !slices.Equal(ac.Argv, want) {
		t.Errorf("Argv =\n  %v\nwant\n  %v (Fork without ResumeSession must be ignored)", ac.Argv, want)
	}
}

// An empty prompt yields empty Stdin and leaves argv untouched. Unlike OpenCode
// there is no positional prompt to omit: claude reads the prompt from stdin, so a
// "just continue, no new instruction" resume simply pipes empty stdin.
//
// Edge case for the optional creds-gated integration test (not a unit concern):
// a "just continue" resume pipes empty stdin to `claude -p --resume`, and Claude
// may want non-empty input. Surfaced here; no guard built at this layer.
func TestClaudeCode_BuildCommand_EmptyPrompt(t *testing.T) {
	ac := ClaudeCode(claudeCodeModel).BuildCommand(CommandOptions{ResumeSession: "ses_abc"})

	want := append(slices.Clone(claudeBaseArgv), "--resume", "ses_abc")
	if !slices.Equal(ac.Argv, want) {
		t.Errorf("Argv =\n  %v\nwant\n  %v", ac.Argv, want)
	}
	if ac.Stdin != "" {
		t.Errorf("Stdin = %q, want empty (no prompt to deliver)", ac.Stdin)
	}
}

// Claude Code authenticates two ways (ADR 0004): an API key for API users or a
// long-lived OAuth token for subscription users. The profile returns both keys;
// the workshop drops whichever is unset on the host, and when both are set
// Claude's own precedence prefers the API key — so it is listed first.
func TestClaudeCode_CredentialEnvKeys(t *testing.T) {
	got := ClaudeCode(claudeCodeModel).CredentialEnvKeys()
	want := []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"}
	if !slices.Equal(got, want) {
		t.Errorf("CredentialEnvKeys() = %v, want %v", got, want)
	}
}

func TestClaudeCode_Sessions(t *testing.T) {
	spec, ok := ClaudeCode(claudeCodeModel).Sessions()
	if !ok {
		t.Fatal("Sessions() ok = false, want true (Claude Code has a session store)")
	}
	want := SessionSpec{DirEnv: "CLAUDE_CONFIG_DIR", Subdir: "projects"}
	if spec != want {
		t.Errorf("Sessions() spec = %+v, want %+v", spec, want)
	}
}
