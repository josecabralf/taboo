package taboo

import "testing"

// MatchModelFormat is advisory (story #25): it reports whether a model string
// looks well-formed for an agent and returns that agent's human-readable
// expected-format string. OpenCode runs a provider/model slug, so a slash-form
// model is well-formed and a bare model (no provider) warns.
func TestMatchModelFormat_OpenCode(t *testing.T) {
	tests := []struct {
		name   string
		model  string
		wantOK bool
	}{
		{name: "provider slug ok", model: openCodeModel, wantOK: true},
		{name: "multi-segment slug ok", model: "anthropic/claude-sonnet-4-6", wantOK: true},
		{name: "surrounding space tolerated", model: "  openrouter/qwen  ", wantOK: true},
		{name: "bare model warns", model: "gpt-4", wantOK: false},
		{name: "empty warns", model: "", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, expected := MatchModelFormat("opencode", tt.model)
			if ok != tt.wantOK {
				t.Errorf("MatchModelFormat(opencode, %q) ok = %v, want %v", tt.model, ok, tt.wantOK)
			}
			// OpenCode has an opinion, so it always advertises an expected format —
			// the validate warning quotes it.
			if expected == "" {
				t.Errorf("MatchModelFormat(opencode, %q) expected = empty, want a non-empty format hint", tt.model)
			}
		})
	}
}

// Claude Code expects a Claude model id or a bare family alias. The heuristic
// accepts anything containing "claude" (covers claude-*, anthropic.claude-*) and
// a value starting with a family alias (sonnet/opus/haiku, case-insensitively),
// and warns on a foreign id like gpt-* or an OpenCode provider slug.
func TestMatchModelFormat_ClaudeCode(t *testing.T) {
	tests := []struct {
		name   string
		model  string
		wantOK bool
	}{
		{name: "claude id ok", model: claudeCodeModel, wantOK: true},
		{name: "bedrock-style claude ok", model: "anthropic.claude-3-5-sonnet", wantOK: true},
		{name: "family alias ok", model: "sonnet", wantOK: true},
		{name: "opus alias ok", model: "opus", wantOK: true},
		{name: "uppercase alias ok", model: "Haiku", wantOK: true},
		{name: "gpt warns", model: "gpt-5", wantOK: false},
		{name: "opencode slug warns", model: "openrouter/qwen/qwen3-coder-plus", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, expected := MatchModelFormat("claude-code", tt.model)
			if ok != tt.wantOK {
				t.Errorf("MatchModelFormat(claude-code, %q) ok = %v, want %v", tt.model, ok, tt.wantOK)
			}
			if expected == "" {
				t.Errorf("MatchModelFormat(claude-code, %q) expected = empty, want a non-empty format hint", tt.model)
			}
		})
	}
}

// Copilot has no model-format opinion (ADR 0008): it proxies many providers'
// models (gpt-*, claude-*, gemini-*, o-series), so there is no single format to
// check. Every non-empty model is well-formed, and it advertises no expected
// format, so the validate command never warns on a copilot model.
func TestMatchModelFormat_Copilot(t *testing.T) {
	for _, model := range []string{copilotModel, "claude-sonnet-4-6", "gemini-2.5-pro", "o3", "anything-goes"} {
		ok, expected := MatchModelFormat("copilot", model)
		if !ok {
			t.Errorf("MatchModelFormat(copilot, %q) ok = false, want true (copilot has no opinion)", model)
		}
		if expected != "" {
			t.Errorf("MatchModelFormat(copilot, %q) expected = %q, want empty (no opinion)", model, expected)
		}
	}
}

// An unknown agent is not the model heuristic's concern — the agent check fails
// it separately. MatchModelFormat returns ok=true with no expected format so it
// never layers a spurious model warning on top of the unknown-agent failure.
func TestMatchModelFormat_UnknownAgent(t *testing.T) {
	ok, expected := MatchModelFormat("gemini", "anything")
	if !ok {
		t.Errorf("MatchModelFormat(gemini, ...) ok = false, want true (unknown agent never warns)")
	}
	if expected != "" {
		t.Errorf("MatchModelFormat(gemini, ...) expected = %q, want empty", expected)
	}
}
