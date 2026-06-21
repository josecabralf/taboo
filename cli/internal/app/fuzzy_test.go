package app

import "testing"

// suggestAgent is the CLI's typo-correction heuristic (ADR 0005, story #24): it
// proposes the closest registered agent name to a misspelled one, or declines.
// It is a pure function over an explicit candidate set so the policy is pinned
// independently of the registry roster.
func TestSuggestAgent(t *testing.T) {
	candidates := []string{"claude-code", "github-copilot", "opencode"}
	tests := []struct {
		name     string
		input    string
		wantSugg string
		wantOK   bool
	}{
		{name: "abbreviation prefers prefix match", input: "claude", wantSugg: "claude-code", wantOK: true},
		{name: "typo within edit budget", input: "claud-code", wantSugg: "claude-code", wantOK: true},
		{name: "transposed opencode", input: "opencpde", wantSugg: "opencode", wantOK: true},
		{name: "transposed github-copilot", input: "github-copilto", wantSugg: "github-copilot", wantOK: true},
		{name: "case normalized", input: "OpenCode", wantSugg: "opencode", wantOK: true},
		{name: "too far yields no suggestion", input: "xyz", wantSugg: "", wantOK: false},
		{name: "empty yields no suggestion", input: "", wantSugg: "", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sugg, ok := suggestAgent(tt.input, candidates)
			if sugg != tt.wantSugg || ok != tt.wantOK {
				t.Errorf("suggestAgent(%q) = (%q, %v), want (%q, %v)", tt.input, sugg, ok, tt.wantSugg, tt.wantOK)
			}
		})
	}
}
