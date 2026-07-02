package prompt

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// placeholderRe matches a {{VAR}} placeholder; VAR is a conventional
// identifier (letters, digits, underscore; not leading with a digit).
var placeholderRe = regexp.MustCompile(`\{\{([A-Za-z_][A-Za-z0-9_]*)\}\}`)

// Placeholders returns the distinct {{VAR}} placeholder names tmpl references,
// sorted ascending. It is pure and reuses placeholderRe, so text that is not a
// placeholder ({{ VAR }}, {{1ST}}, {{a-b}}) is ignored, exactly as Substitute
// ignores it. An empty or placeholder-free template yields an empty result.
func Placeholders(tmpl string) []string {
	var names []string
	for _, m := range placeholderRe.FindAllStringSubmatch(tmpl, -1) {
		if !slices.Contains(names, m[1]) {
			names = append(names, m[1])
		}
	}
	slices.Sort(names)
	return names
}

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
