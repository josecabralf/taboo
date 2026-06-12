package taboo

import (
	"context"
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

func TestSubstitute_MissingVarIsError(t *testing.T) {
	_, err := Substitute("use {{MODEL}} on {{FILE}}", map[string]string{"FILE": "main.go"})
	if err == nil {
		t.Fatal("Substitute: want error for undefined variable, got nil")
	}
	if !strings.Contains(err.Error(), "MODEL") {
		t.Errorf("error %q should name the missing variable MODEL", err)
	}
}

func TestExpand_RoutesThroughWorkshopExec(t *testing.T) {
	// The fake stands in for the workshop: it returns the already-expanded
	// string the real shell would print, so Expand's job is to route the
	// prompt through `workshop exec` and hand back what the shell produced.
	fc := &fakeCommander{
		stdoutFn: func(c Cmd) string {
			if verbOf(c) == "exec" {
				return "files: main.go util.go"
			}
			return ""
		},
	}
	pt := NewPromptTemplate(fc, "/proj", "taboo-run")

	got, err := pt.Expand(context.Background(), `files: $(ls *.go)`)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if want := "files: main.go util.go"; got != want {
		t.Errorf("Expand = %q, want %q", got, want)
	}

	// The expansion runs as `workshop exec` inside the workshop, in /workspace,
	// invoking a shell so $(...) / $VAR resolve in the real environment.
	exec := fc.findCallN(t, "exec", 0)
	if !slices.Contains(exec.Args, "/workspace") {
		t.Errorf("expand exec missing /workspace cwd: %v", exec.Args)
	}
	if !slices.Contains(exec.Args, "taboo-run") {
		t.Errorf("expand exec missing workshop name: %v", exec.Args)
	}
	if i := slices.Index(exec.Args, "sh"); i < 0 || i+1 >= len(exec.Args) || exec.Args[i+1] != "-c" {
		t.Errorf("expand exec should invoke `sh -c`: %v", exec.Args)
	}
	// The original prompt is embedded in the shell line so the shell expands it.
	if !slices.ContainsFunc(exec.Args, func(a string) bool { return strings.Contains(a, `$(ls *.go)`) }) {
		t.Errorf("expand exec shell line missing the prompt expression: %v", exec.Args)
	}
}

func TestResolve_SubstitutesThenExpands(t *testing.T) {
	fc := &fakeCommander{
		stdoutFn: func(c Cmd) string {
			if verbOf(c) == "exec" {
				return "resolved prompt"
			}
			return ""
		},
	}
	pt := NewPromptTemplate(fc, "/proj", "taboo-run")

	got, err := pt.Resolve(context.Background(), "use {{MODEL}} as of $(date)", map[string]string{"MODEL": "qwen"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if want := "resolved prompt"; got != want {
		t.Errorf("Resolve = %q, want %q", got, want)
	}

	// Substitution happens before expansion: the shell line carries the filled
	// value and the live shell expression, never the raw placeholder.
	exec := fc.findCallN(t, "exec", 0)
	line := exec.Args[len(exec.Args)-1]
	if !strings.Contains(line, "qwen") {
		t.Errorf("shell line %q should contain substituted value qwen", line)
	}
	if !strings.Contains(line, "$(date)") {
		t.Errorf("shell line %q should preserve the shell expression", line)
	}
	if strings.Contains(line, "{{MODEL}}") {
		t.Errorf("shell line %q still contains the unsubstituted placeholder", line)
	}
}

func TestExpand_EscapesQuotesButKeepsExpansion(t *testing.T) {
	fc := &fakeCommander{}
	pt := NewPromptTemplate(fc, "/proj", "taboo-run")

	if _, err := pt.Expand(context.Background(), `say "hi" $(whoami)`); err != nil {
		t.Fatalf("Expand: %v", err)
	}

	line := fc.findCallN(t, "exec", 0).Args
	shellLine := line[len(line)-1]
	// Inner double-quotes are escaped so the literal text stays inside the
	// quoting and cannot terminate the string early.
	if !strings.Contains(shellLine, `\"hi\"`) {
		t.Errorf("shell line %q should escape the embedded double-quotes", shellLine)
	}
	// $ is left intact so the shell still expands the expression.
	if !strings.Contains(shellLine, "$(whoami)") {
		t.Errorf("shell line %q should preserve the shell expansion", shellLine)
	}
}

func TestResolve_SubstitutionErrorSkipsWorkshop(t *testing.T) {
	fc := &fakeCommander{}
	pt := NewPromptTemplate(fc, "/proj", "taboo-run")

	if _, err := pt.Resolve(context.Background(), "use {{MODEL}}", nil); err == nil {
		t.Fatal("Resolve: want error for undefined variable, got nil")
	}
	if len(fc.calls) != 0 {
		t.Errorf("substitution error should short-circuit before any workshop call, got %v", fc.verbs())
	}
}
