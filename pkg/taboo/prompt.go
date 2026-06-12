package taboo

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// placeholderRe matches a {{VAR}} placeholder; VAR is a conventional
// identifier (letters, digits, underscore; not leading with a digit).
var placeholderRe = regexp.MustCompile(`\{\{([A-Za-z_][A-Za-z0-9_]*)\}\}`)

// Substitute replaces every {{VAR}} placeholder in tmpl with vars[VAR]. It is
// pure. A placeholder with no matching key is an error (rather than left in
// place), so an unfilled prompt never reaches the agent silently.
func Substitute(tmpl string, vars map[string]string) (string, error) {
	var missing []string
	out := placeholderRe.ReplaceAllStringFunc(tmpl, func(match string) string {
		name := match[2 : len(match)-2] // the regex guarantees the {{ }} wrapper
		val, ok := vars[name]
		if !ok {
			if !slices.Contains(missing, name) {
				missing = append(missing, name)
			}
			return match
		}
		return val
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("prompt template: undefined variable(s): %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// PromptTemplate resolves a prompt before it reaches the agent: pure {{VAR}}
// substitution (see Substitute) followed by shell-expression expansion executed
// inside the workshop via `workshop exec` through the Commander seam.
type PromptTemplate struct {
	cmd     Commander
	project string
	ws      string
}

// NewPromptTemplate binds a template resolver to a workshop, driving the
// in-workshop shell through cmd.
func NewPromptTemplate(cmd Commander, project, ws string) *PromptTemplate {
	return &PromptTemplate{cmd: cmd, project: project, ws: ws}
}

// Resolve produces the final prompt the agent receives: it substitutes {{VAR}}
// placeholders (pure) and then expands shell expressions inside the workshop. A
// substitution error short-circuits before any workshop call.
//
// Substitution happens before expansion, so a substituted value is itself
// subject to the shell: a value containing $, a backtick, or a quote is
// interpreted, not inserted literally (e.g. {{P}}="$100" expands $1, not the
// literal "$100"). Pass vars that are trusted, literal text.
func (p *PromptTemplate) Resolve(ctx context.Context, tmpl string, vars map[string]string) (string, error) {
	substituted, err := Substitute(tmpl, vars)
	if err != nil {
		return "", err
	}
	return p.Expand(ctx, substituted)
}

// Expand runs prompt through a shell inside the workshop so that shell
// expressions ($(...), $VAR, backticks) resolve against the real environment,
// returning exactly what the shell printed. The prompt is embedded in a
// `printf '%s'` line so no trailing newline is added and printf does not
// reinterpret backslash escapes in the literal text.
func (p *PromptTemplate) Expand(ctx context.Context, prompt string) (string, error) {
	command := []string{"sh", "-c", shellExpandLine(prompt)}
	opts := execOptions{cwd: workspaceTarget}
	var captured strings.Builder
	cmd := Cmd{
		Name:   "workshop",
		Args:   execArgs(p.project, p.ws, opts, command),
		Stdout: &captured,
	}
	if err := p.cmd.Run(ctx, cmd); err != nil {
		return "", fmt.Errorf("expand prompt in workshop: %w", err)
	}
	return captured.String(), nil
}

// shellExpandLine wraps prompt so a POSIX shell expands its expressions and
// prints the result verbatim. The prompt is placed inside double quotes (which
// permit $(...), $VAR, and backtick expansion); backslash and double-quote are
// escaped so the literal text cannot break out of the quoting, while `$` is
// deliberately left intact so expansion happens.
func shellExpandLine(prompt string) string {
	return `printf '%s' "` + shellQuoteEscaper.Replace(prompt) + `"`
}

// shellQuoteEscaper neutralizes the two characters that could break literal
// text out of the double-quoted printf argument: a backslash (doubled so it
// stays literal) and a double-quote (escaped so it cannot close the string).
// `$` and backtick are deliberately left untouched so expansion still happens.
var shellQuoteEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)
