package app

import "strings"

// suggestAgent proposes the registered candidate that best corrects name, and
// whether the correction is worth surfacing. It lowercases and trims both sides,
// then ranks candidates so a prefix relationship (an abbreviation like "claude"
// for "claude-code", or vice versa) always outranks a plain edit-distance match;
// within the same class the smaller Levenshtein distance wins. A suggestion is
// surfaced when the best candidate is a prefix match or its distance is within a
// budget that scales with the input length (len/2 + 1) — close enough to be a
// likely typo, not a coincidence. An empty name never suggests.
//
// The fuzzy policy lives in the CLI, not the registry (ADR 0005): the registry
// only supplies the candidate set via taboo.AgentNames().
func suggestAgent(name string, candidates []string) (string, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return "", false
	}
	var (
		best       string
		bestDist   int
		bestPrefix bool
		found      bool
	)
	for _, raw := range candidates {
		c := strings.ToLower(strings.TrimSpace(raw))
		if c == "" {
			continue
		}
		prefix := strings.HasPrefix(c, n) || strings.HasPrefix(n, c)
		d := levenshtein(n, c)
		if !found || isBetterSuggestion(prefix, d, bestPrefix, bestDist) {
			best, bestDist, bestPrefix, found = raw, d, prefix, true
		}
	}
	if !found {
		return "", false
	}
	if bestPrefix || bestDist <= len(n)/2+1 {
		return best, true
	}
	return "", false
}

// isBetterSuggestion reports whether candidate (prefix, dist) outranks the
// current best: a prefix match beats a non-prefix one, and within the same
// prefix class the smaller edit distance wins.
func isBetterSuggestion(prefix bool, dist int, bestPrefix bool, bestDist int) bool {
	if prefix != bestPrefix {
		return prefix
	}
	return dist < bestDist
}

// levenshtein computes the edit distance (insertions, deletions, substitutions)
// between a and b with the standard two-row dynamic-programming table. It is a
// small self-contained helper so the CLI takes on no fuzzy-matching dependency.
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		curr := make([]int, len(rb)+1)
		curr[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev = curr
	}
	return prev[len(rb)]
}
