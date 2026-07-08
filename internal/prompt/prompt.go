package prompt

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// placeholderRe matches a {{VAR}} placeholder; VAR is a conventional identifier.
var placeholderRe = regexp.MustCompile(`\{\{([A-Za-z_][A-Za-z0-9_]*)\}\}`)

// Placeholders returns the distinct {{VAR}} names tmpl references, sorted. Text
// that is not a placeholder ({{ VAR }}, {{1ST}}) is ignored, as Substitute
// ignores it.
func Placeholders(tmpl string) []string {
	seen := make(map[string]struct{})
	var names []string
	for _, m := range placeholderRe.FindAllStringSubmatch(tmpl, -1) {
		if _, ok := seen[m[1]]; !ok {
			seen[m[1]] = struct{}{}
			names = append(names, m[1])
		}
	}
	slices.Sort(names)
	return names
}

// Substitute replaces every {{VAR}} in tmpl with vars[VAR]. A placeholder with no
// matching key is an error, so an unfilled prompt never reaches the agent silently.
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
