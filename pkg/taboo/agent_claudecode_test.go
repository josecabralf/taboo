package taboo

import (
	"slices"
	"testing"
)

const claudeCodeModel = "claude-sonnet-4-6"

// claudeBaseArgv is the stable prefix every Claude Code invocation carries.
// Auto permission mode lets the headless agent edit/commit without an approver;
// the `git push` deny matters because the worktree shares the host repo's refs.
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
	// Claude Code delivers the prompt on stdin (ADR 0001), not in argv.
	if ac.Stdin != "do the thing" {
		t.Errorf("Stdin = %q, want %q (Claude Code delivers the prompt on stdin)", ac.Stdin, "do the thing")
	}
}

func TestClaudeCode_BuildCommand_UsesModel(t *testing.T) {
	ac := ClaudeCode("claude-opus-4-8").BuildCommand(CommandOptions{Prompt: "go"})
	// TestClaudeCode_BuildCommand owns the canonical full-argv check; here only the
	// model interpolation matters, so assert it without re-pinning the prefix.
	i := slices.Index(ac.Argv, "--model")
	if i < 0 || i+1 >= len(ac.Argv) || ac.Argv[i+1] != "claude-opus-4-8" {
		t.Errorf("Argv = %v, want --model followed by %q", ac.Argv, "claude-opus-4-8")
	}
}

// Resume threads the session id into argv as `--resume <id>` (ADR 0003). The
// prompt stays on stdin.
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
// --fork-session` (ADR 0003), forking the session into a new one and leaving the
// source conversation untouched.
func TestClaudeCode_BuildCommand_Fork(t *testing.T) {
	ac := ClaudeCode(claudeCodeModel).BuildCommand(CommandOptions{
		Prompt: "do the thing", ResumeSession: "ses_abc", Fork: true,
	})

	want := append(slices.Clone(claudeBaseArgv), "--resume", "ses_abc", "--fork-session")
	if !slices.Equal(ac.Argv, want) {
		t.Errorf("Argv =\n  %v\nwant\n  %v", ac.Argv, want)
	}
}

// Fork without a session to fork from is dropped: --fork-session only applies
// when continuing a session, so argv matches a plain fresh run.
func TestClaudeCode_BuildCommand_ForkWithoutResumeIgnored(t *testing.T) {
	ac := ClaudeCode(claudeCodeModel).BuildCommand(CommandOptions{Prompt: "go", Fork: true})

	want := claudeBaseArgv
	if !slices.Equal(ac.Argv, want) {
		t.Errorf("Argv =\n  %v\nwant\n  %v (Fork without ResumeSession must be ignored)", ac.Argv, want)
	}
}

// An empty prompt yields empty Stdin and leaves argv untouched: unlike OpenCode
// there is no positional prompt to omit, since claude reads from stdin.
//
// Gotcha for the creds-gated integration test: a "just continue" resume pipes
// empty stdin to `claude -p --resume`, which Claude may reject as empty input.
// Surfaced here; no guard is built at this layer.
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

// Claude Code authenticates two ways (ADR 0004): an API key or a long-lived OAuth
// token. The API key is listed first because Claude's own precedence prefers it
// when both are set.
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
