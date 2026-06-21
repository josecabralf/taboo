package main

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSlugBranchLowercasesAndSlugs(t *testing.T) {
	t.Parallel()

	got := slugBranch(42, "Add the Foo!")
	want := "agent/issue-42-add-the-foo"
	if got != want {
		t.Errorf("slugBranch = %q, want %q", got, want)
	}
}

func TestSlugBranchCollapsesAndTrimsPunctuationRuns(t *testing.T) {
	t.Parallel()

	// Leading/trailing punctuation is trimmed; interior runs of non-alphanumerics
	// collapse to a single dash.
	got := slugBranch(7, "  ***Hello,,, World!!!  ")
	want := "agent/issue-7-hello-world"
	if got != want {
		t.Errorf("slugBranch = %q, want %q", got, want)
	}
}

func TestSlugBranchTruncatesAt50WithoutTrailingDash(t *testing.T) {
	t.Parallel()

	// A title whose slug exceeds 50 chars must truncate to 50 and never end on a
	// dash. Here the 51st slug char is the start of "word", and char 50 is a dash
	// (the separator before it), so the cap-then-trim must drop that dash.
	title := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa word"
	got := slugBranch(1, title)

	prefix := "agent/issue-1-"
	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("slugBranch = %q, missing prefix %q", got, prefix)
	}
	slug := strings.TrimPrefix(got, prefix)
	if len(slug) > 50 {
		t.Errorf("slug %q has length %d, want <= 50", slug, len(slug))
	}
	if strings.HasSuffix(slug, "-") {
		t.Errorf("slug %q ends with a dash after truncation", slug)
	}
	if slug != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("slug = %q, want the 50 a's with the trailing dash dropped", slug)
	}
}

func TestPrBodyWithPlanStartsWithClosesThenPlan(t *testing.T) {
	t.Parallel()

	plan := "## Plan\n\n- step one\n"
	got := prBody(7, plan)

	if !strings.HasPrefix(got, "Closes #7\n\n") {
		t.Errorf("prBody = %q, want prefix %q", got, "Closes #7\n\n")
	}
	if !strings.HasSuffix(got, plan) {
		t.Errorf("prBody = %q, want it to end with the plan %q", got, plan)
	}
	want := "Closes #7\n\n" + plan
	if got != want {
		t.Errorf("prBody = %q, want %q", got, want)
	}
}

func TestPrBodyEmptyPlanUsesFallback(t *testing.T) {
	t.Parallel()

	got := prBody(7, "")

	if !strings.Contains(got, "Closes #7") {
		t.Errorf("prBody = %q, want it to contain %q", got, "Closes #7")
	}
	if !strings.Contains(got, "Implemented by the taboo agent for issue #7.") {
		t.Errorf("prBody = %q, want the fallback sentence", got)
	}
	if !strings.Contains(got, "No plan file was produced") {
		t.Errorf("prBody = %q, want the no-plan note", got)
	}
}

func TestPrTitleCapsAt256(t *testing.T) {
	t.Parallel()

	title := strings.Repeat("x", 300)
	got := prTitle(title)
	if n := utf8.RuneCountInString(got); n != 256 {
		t.Errorf("prTitle rune count = %d, want 256", n)
	}
}

func TestPrTitleCapsAt256RunesWithoutCorruptingMultibyte(t *testing.T) {
	t.Parallel()

	// A title of 300 multibyte runes; truncating by byte would split a rune at the
	// 256-byte boundary and produce invalid UTF-8. Rune-aware truncation must keep
	// exactly 256 whole runes and stay valid UTF-8.
	title := strings.Repeat("é", 300)
	got := prTitle(title)

	if !utf8.ValidString(got) {
		t.Errorf("prTitle = %q is not valid UTF-8", got)
	}
	if n := utf8.RuneCountInString(got); n != 256 {
		t.Errorf("prTitle rune count = %d, want 256", n)
	}
}

func TestPrTitleLeavesShortTitleUntouched(t *testing.T) {
	t.Parallel()

	title := "short title"
	if got := prTitle(title); got != title {
		t.Errorf("prTitle = %q, want %q", got, title)
	}
}

func TestRunNoArgsReturnsUsageError(t *testing.T) {
	t.Parallel()

	err := run(nil)
	if err == nil {
		t.Fatal("run(nil) returned nil, want a usage error")
	}
	if !strings.Contains(err.Error(), "usage") {
		t.Errorf("run(nil) error = %q, want it to mention usage", err.Error())
	}
}

func TestRunUnknownCommandReturnsError(t *testing.T) {
	t.Parallel()

	err := run([]string{"bogus"})
	if err == nil {
		t.Fatal(`run(["bogus"]) returned nil, want an unknown-command error`)
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error = %q, want it to name the unknown command", err.Error())
	}
}

func TestRunImplementRequiresIssueBeforeIO(t *testing.T) {
	t.Parallel()

	// With no --issue flag the function must fail validation before touching gh,
	// git, or taboo. We assert on the specific message rather than mocking I/O:
	// the spec requires the check to happen before any I/O.
	err := runImplement(context.Background(), nil)
	if err == nil {
		t.Fatal("runImplement with no --issue returned nil, want a required-flag error")
	}
	if !strings.Contains(err.Error(), "--issue is required") {
		t.Errorf("error = %q, want it to mention --issue is required", err.Error())
	}
}

func TestRunLoopDispatchesAndParsesFlags(t *testing.T) {
	t.Parallel()

	// An unknown flag must surface as a flag-parse error from runLoop's own
	// FlagSet, which proves the loop case is wired into run's switch (not falling
	// through to the unknown-command branch) and that runLoop parses its flags
	// before any working-directory or gh I/O. Keeping the assertion on the parse
	// error keeps the test hermetic — no real wave is ever driven.
	err := run([]string{"loop", "--bogus-flag"})
	if err == nil {
		t.Fatal(`run(["loop", "--bogus-flag"]) returned nil, want a flag-parse error`)
	}
	if strings.Contains(err.Error(), "unknown command") {
		t.Errorf("error = %q, want a flag-parse error, not the unknown-command branch", err.Error())
	}
}

func TestRunReviewRequiresPRBeforeIO(t *testing.T) {
	t.Parallel()

	// With no --pr flag the function must fail validation before touching gh or
	// taboo, mirroring runImplement's pre-I/O guard.
	err := runReview(context.Background(), nil)
	if err == nil {
		t.Fatal("runReview with no --pr returned nil, want a required-flag error")
	}
	if !strings.Contains(err.Error(), "--pr is required") {
		t.Errorf("error = %q, want it to mention --pr is required", err.Error())
	}
}
