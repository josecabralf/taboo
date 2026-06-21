package workshop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveSourceDefinition_RootDefault pins the single-root case: a repo with
// only a root workshop.yaml resolves (with no selection) to that exact path.
func TestResolveSourceDefinition_RootDefault(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "workshop.yaml"), []byte("name: x\n"), 0o600); err != nil {
		t.Fatalf("write root def: %v", err)
	}

	got, err := resolveSourceDefinition(repo, "")
	if err != nil {
		t.Fatalf("resolveSourceDefinition: %v", err)
	}
	if want := filepath.Join(repo, "workshop.yaml"); got != want {
		t.Errorf("resolveSourceDefinition(repo, \"\") = %q, want %q", got, want)
	}
}

// TestResolveSourceDefinition_LoneNamed pins the single-named case: a repo with
// no root workshop.yaml but exactly one .workshop/*.yaml resolves (with no
// selection) to that named definition's path.
func TestResolveSourceDefinition_LoneNamed(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".workshop"), 0o700); err != nil {
		t.Fatalf("mkdir .workshop: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".workshop", "foo.yaml"), []byte("name: foo\n"), 0o600); err != nil {
		t.Fatalf("write named def: %v", err)
	}

	got, err := resolveSourceDefinition(repo, "")
	if err != nil {
		t.Fatalf("resolveSourceDefinition: %v", err)
	}
	if want := filepath.Join(repo, ".workshop", "foo.yaml"); got != want {
		t.Errorf("resolveSourceDefinition(repo, \"\") = %q, want %q", got, want)
	}
}

// TestResolveSourceDefinition_MultipleNamedNoSelection pins the ambiguous
// multi-named case: a repo with no root workshop.yaml but several
// .workshop/*.yaml definitions and no selection is a hard error that lists the
// candidate names (sorted) and tells the user how to pick one.
func TestResolveSourceDefinition_MultipleNamedNoSelection(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".workshop"), 0o700); err != nil {
		t.Fatalf("mkdir .workshop: %v", err)
	}
	for _, name := range []string{"web", "api"} {
		if err := os.WriteFile(filepath.Join(repo, ".workshop", name+".yaml"), []byte("name: "+name+"\n"), 0o600); err != nil {
			t.Fatalf("write named def %q: %v", name, err)
		}
	}

	got, err := resolveSourceDefinition(repo, "")
	if err == nil {
		t.Fatalf("resolveSourceDefinition(repo, \"\") = %q, want error", got)
	}
	if got != "" {
		t.Errorf("resolveSourceDefinition path = %q, want \"\"", got)
	}
	msg := err.Error()
	if !strings.Contains(msg, "api, web") {
		t.Errorf("error %q does not list the sorted candidates \"api, web\"", msg)
	}
	if !strings.Contains(msg, "source-definition") || !strings.Contains(msg, "--from") {
		t.Errorf("error %q does not instruct setting source-definition or --from", msg)
	}
}

// TestResolveSourceDefinition_ExplicitSelection pins the explicit-selection case:
// a non-empty selection naming an existing .workshop/<name>.yaml resolves to that
// definition's path — even when a root workshop.yaml is also present (an explicit
// selection wins over the root-vs-named ambiguity check).
func TestResolveSourceDefinition_ExplicitSelection(t *testing.T) {
	t.Run("named definitions only", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.MkdirAll(filepath.Join(repo, ".workshop"), 0o700); err != nil {
			t.Fatalf("mkdir .workshop: %v", err)
		}
		for _, name := range []string{"api", "web"} {
			if err := os.WriteFile(filepath.Join(repo, ".workshop", name+".yaml"), []byte("name: "+name+"\n"), 0o600); err != nil {
				t.Fatalf("write named def %q: %v", name, err)
			}
		}

		got, err := resolveSourceDefinition(repo, "web")
		if err != nil {
			t.Fatalf("resolveSourceDefinition: %v", err)
		}
		if want := filepath.Join(repo, ".workshop", "web.yaml"); got != want {
			t.Errorf("resolveSourceDefinition(repo, \"web\") = %q, want %q", got, want)
		}
	})

	t.Run("wins over a root workshop.yaml", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.WriteFile(filepath.Join(repo, "workshop.yaml"), []byte("name: root\n"), 0o600); err != nil {
			t.Fatalf("write root def: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(repo, ".workshop"), 0o700); err != nil {
			t.Fatalf("mkdir .workshop: %v", err)
		}
		if err := os.WriteFile(filepath.Join(repo, ".workshop", "web.yaml"), []byte("name: web\n"), 0o600); err != nil {
			t.Fatalf("write named def: %v", err)
		}

		got, err := resolveSourceDefinition(repo, "web")
		if err != nil {
			t.Fatalf("resolveSourceDefinition: %v", err)
		}
		if want := filepath.Join(repo, ".workshop", "web.yaml"); got != want {
			t.Errorf("resolveSourceDefinition(repo, \"web\") = %q, want %q", got, want)
		}
	})
}

// TestResolveSourceDefinition_UnknownSelection pins the unknown-selection case: a
// non-empty selection that names no existing definition is a hard error that
// names the unknown selection and lists the available candidates (sorted).
func TestResolveSourceDefinition_UnknownSelection(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".workshop"), 0o700); err != nil {
		t.Fatalf("mkdir .workshop: %v", err)
	}
	for _, name := range []string{"api", "web"} {
		if err := os.WriteFile(filepath.Join(repo, ".workshop", name+".yaml"), []byte("name: "+name+"\n"), 0o600); err != nil {
			t.Fatalf("write named def %q: %v", name, err)
		}
	}

	got, err := resolveSourceDefinition(repo, "nope")
	if err == nil {
		t.Fatalf("resolveSourceDefinition(repo, \"nope\") = %q, want error", got)
	}
	if got != "" {
		t.Errorf("resolveSourceDefinition path = %q, want \"\"", got)
	}
	msg := err.Error()
	if !strings.Contains(msg, "nope") {
		t.Errorf("error %q does not name the unknown selection nope", msg)
	}
	if !strings.Contains(msg, "unknown workshop definition") {
		t.Errorf("error %q does not use the \"workshop definition\" terminology", msg)
	}
	if !strings.Contains(msg, "api, web") {
		t.Errorf("error %q does not list the sorted candidates \"api, web\"", msg)
	}
}

// TestResolveSourceDefinition_SelectionWithNoNamedDefs pins the root-only case
// under selection: a repo with a selection supplied but no named .workshop/*.yaml
// definitions is a hard error telling the caller to drop the selection, rather
// than an unknown-name error with an empty candidate list.
func TestResolveSourceDefinition_SelectionWithNoNamedDefs(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "workshop.yaml"), []byte("name: x\n"), 0o600); err != nil {
		t.Fatalf("write root def: %v", err)
	}

	got, err := resolveSourceDefinition(repo, "web")
	if err == nil {
		t.Fatalf("resolveSourceDefinition(repo, \"web\") = %q, want error", got)
	}
	if got != "" {
		t.Errorf("resolveSourceDefinition path = %q, want \"\"", got)
	}
	msg := err.Error()
	if !strings.Contains(msg, "no named .workshop/*.yaml definitions") {
		t.Errorf("error %q does not flag the project has no named definitions", msg)
	}
	if strings.Contains(msg, "available definitions are") {
		t.Errorf("error %q should not render an empty candidate list", msg)
	}
}

// TestResolveSourceDefinition_NoDefinition pins the empty-repo case: a repo with
// neither a root workshop.yaml nor any .workshop/*.yaml and no selection is a
// hard error saying no workshop definition was found.
func TestResolveSourceDefinition_NoDefinition(t *testing.T) {
	repo := t.TempDir()

	got, err := resolveSourceDefinition(repo, "")
	if err == nil {
		t.Fatalf("resolveSourceDefinition(repo, \"\") = %q, want error", got)
	}
	if got != "" {
		t.Errorf("resolveSourceDefinition path = %q, want \"\"", got)
	}
	if msg := err.Error(); !strings.Contains(msg, "no workshop definition found") {
		t.Errorf("error %q does not say no workshop definition found", msg)
	}
}

// TestResolveSourceDefinition_RootAndNamedAmbiguous pins the ambiguous case: a
// repo carrying BOTH a root workshop.yaml and a named .workshop/*.yaml with no
// selection is a hard error signaling that both are present.
func TestResolveSourceDefinition_RootAndNamedAmbiguous(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "workshop.yaml"), []byte("name: x\n"), 0o600); err != nil {
		t.Fatalf("write root def: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".workshop"), 0o700); err != nil {
		t.Fatalf("mkdir .workshop: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".workshop", "x.yaml"), []byte("name: x\n"), 0o600); err != nil {
		t.Fatalf("write named def: %v", err)
	}

	got, err := resolveSourceDefinition(repo, "")
	if err == nil {
		t.Fatalf("resolveSourceDefinition(repo, \"\") = %q, want error", got)
	}
	if got != "" {
		t.Errorf("resolveSourceDefinition path = %q, want \"\"", got)
	}
	msg := err.Error()
	if !strings.Contains(msg, "workshop.yaml") || !strings.Contains(msg, ".workshop") {
		t.Errorf("error %q does not signal both a root workshop.yaml and named .workshop defs are present", msg)
	}
	if !strings.Contains(msg, "remove one") {
		t.Errorf("error %q does not offer a remedy (remove one)", msg)
	}
}

// TestSourceDefinitions pins the exported scan: it returns the sorted stems of
// the .workshop/*.yaml definitions, and an empty slice (not nil-required, just
// len 0) with no error for a root-only repo.
func TestSourceDefinitions(t *testing.T) {
	t.Run("named definitions sorted", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.MkdirAll(filepath.Join(repo, ".workshop"), 0o700); err != nil {
			t.Fatalf("mkdir .workshop: %v", err)
		}
		for _, name := range []string{"web", "api"} {
			if err := os.WriteFile(filepath.Join(repo, ".workshop", name+".yaml"), []byte("name: "+name+"\n"), 0o600); err != nil {
				t.Fatalf("write named def %q: %v", name, err)
			}
		}

		got, err := SourceDefinitions(repo)
		if err != nil {
			t.Fatalf("SourceDefinitions: %v", err)
		}
		want := []string{"api", "web"}
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Errorf("SourceDefinitions(repo) = %v, want %v", got, want)
		}
	})

	t.Run("root-only repo is empty", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.WriteFile(filepath.Join(repo, "workshop.yaml"), []byte("name: x\n"), 0o600); err != nil {
			t.Fatalf("write root def: %v", err)
		}

		got, err := SourceDefinitions(repo)
		if err != nil {
			t.Fatalf("SourceDefinitions: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("SourceDefinitions(repo) = %v, want empty", got)
		}
	})
}
