package app

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
)

// minWorkshopVersion is the floor workshop version doctor accepts.
const minWorkshopVersion = "0.9.1"

// versionRe matches the first MAJOR.MINOR[.PATCH] triple in a string.
var versionRe = regexp.MustCompile(`(\d+)\.(\d+)(?:\.(\d+))?`)

// parseVersion extracts the first MAJOR.MINOR[.PATCH] triple in s as a [3]int,
// tolerating a leading `vX` prefix and a trailing suffix like `-abc-dev`. A
// missing PATCH defaults to 0.
func parseVersion(s string) ([3]int, error) {
	m := versionRe.FindStringSubmatch(s)
	if m == nil {
		return [3]int{}, fmt.Errorf("no MAJOR.MINOR version found in %q", s)
	}
	var v [3]int
	for i := range 3 {
		if m[i+1] == "" {
			v[i] = 0
			continue
		}
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return [3]int{}, fmt.Errorf("no MAJOR.MINOR version found in %q", s)
		}
		v[i] = n
	}
	return v, nil
}

// versionLess reports whether version a is strictly older than b.
func versionLess(a, b [3]int) bool {
	return slices.Compare(a[:], b[:]) < 0
}

// formatVersion renders a parsed version back to MAJOR.MINOR.PATCH.
func formatVersion(v [3]int) string {
	return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
}
