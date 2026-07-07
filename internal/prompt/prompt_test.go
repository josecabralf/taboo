package prompt

import (
	"slices"
	"strings"
	"testing"
)

func TestSubstitute_ReplacesPlaceholder(t *testing.T) {
	got, err := Substitute("fix the bug in {{FILE}}", map[string]string{"FILE": "main.go"})
	if err != nil {
		t.Fatalf("Substitute: %v", err)
	}
	if want := "fix the bug in main.go"; got != want {
		t.Errorf("Substitute = %q, want %q", got, want)
	}
}

// TestPlaceholders locks the discovery contract: the distinct {{VAR}} names a
// template references, sorted ascending, with text that is not a placeholder
// ignored exactly as Substitute ignores it.
func TestPlaceholders(t *testing.T) {
	cases := []struct {
		name string
		tmpl string
		want []string
	}{
		{"empty template", "", nil},
		{"no placeholders", "just plain text", nil},
		{"single placeholder", "fix {{FILE}}", []string{"FILE"}},
		{"deduped", "{{X}} then {{X}} again", []string{"X"}},
		{"sorted ascending", "{{ZETA}} {{ALPHA}} {{MID}}", []string{"ALPHA", "MID", "ZETA"}},
		{"non-placeholder text ignored", "{{ VAR }} {{1ST}} {{a-b}} {{OK}}", []string{"OK"}},
		{"underscore and digits", "{{_LEAD}} {{V2}}", []string{"V2", "_LEAD"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Placeholders(tc.tmpl); !slices.Equal(got, tc.want) {
				t.Errorf("Placeholders(%q) = %v, want %v", tc.tmpl, got, tc.want)
			}
		})
	}
}

func TestSubstitute_MissingVarIsError(t *testing.T) {
	_, err := Substitute("use {{MODEL}} on {{FILE}}", map[string]string{"FILE": "main.go"})
	if err == nil {
		t.Fatal("Substitute: want error for undefined variable, got nil")
	}
	if !strings.Contains(err.Error(), "MODEL") {
		t.Errorf("error %q should name the missing variable MODEL", err)
	}
}
