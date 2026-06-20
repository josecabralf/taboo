package prompt

import (
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

func TestSubstitute_MissingVarIsError(t *testing.T) {
	_, err := Substitute("use {{MODEL}} on {{FILE}}", map[string]string{"FILE": "main.go"})
	if err == nil {
		t.Fatal("Substitute: want error for undefined variable, got nil")
	}
	if !strings.Contains(err.Error(), "MODEL") {
		t.Errorf("error %q should name the missing variable MODEL", err)
	}
}
