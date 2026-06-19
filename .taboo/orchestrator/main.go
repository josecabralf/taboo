// Command afk is the taboo orchestrator entrypoint. It drives one GitHub issue
// end-to-end on the host's pkg/taboo: it fetches the issue, runs the implement
// workflow (the agent commits in place, push-denied), pushes the branch, opens a
// draft PR carrying the agent's plan, and applies the agent:review label. All
// GitHub/git I/O funnels through internal/ghio; the taboo run through
// internal/taborun. Invoked in CI as `go run ./.taboo/orchestrator implement
// --issue <n>`.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"afk/internal/ghio"
	"afk/internal/taborun"
)

// planFile is the path, relative to the run's worktree, where the implement agent
// writes its plan (mirrored into the draft PR body). It matches PLAN_OUTPUT_PATH.
const planFile = ".taboo-plan.md"

// reviewLabel is applied to the draft PR to cascade it into the review workflow.
const reviewLabel = "agent:review"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "afk:", err)
		os.Exit(1)
	}
}

// run dispatches to a subcommand. Only "implement" exists today.
func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: afk implement --issue <n>")
	}
	switch args[0] {
	case "implement":
		return runImplement(context.Background(), args[1:])
	default:
		return fmt.Errorf("unknown command %q (usage: afk implement --issue <n>)", args[0])
	}
}

// runImplement drives one issue end-to-end on pkg/taboo: fetch the issue, run the
// implement workflow (agent commits in place, push-denied), push the branch, open
// a draft PR whose body is the agent's plan, and apply the agent:review label.
func runImplement(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("implement", flag.ContinueOnError)
	issue := fs.Int("issue", 0, "GitHub issue number to implement")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *issue <= 0 {
		return errors.New("--issue is required")
	}

	repo, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	projectDir := filepath.Join(repo, ".taboo")
	configPath := filepath.Join(projectDir, "taboo.yaml")

	gh := ghio.New(ghio.NewExec())

	iss, err := gh.IssueView(ctx, *issue)
	if err != nil {
		return fmt.Errorf("fetch issue: %w", err)
	}

	branch := slugBranch(iss.Number, iss.Title)

	vars := map[string]string{
		"ISSUE_NUMBER":     strconv.Itoa(iss.Number),
		"ISSUE_TITLE":      iss.Title,
		"ISSUE_BODY":       iss.Body,
		"PLAN_OUTPUT_PATH": planFile,
	}

	res, err := taborun.Run(ctx, taborun.Options{
		ConfigPath: configPath,
		Workflow:   "implement",
		Branch:     branch,
		Vars:       vars,
		RepoPath:   repo,
		ProjectDir: projectDir,
		Stdout:     os.Stderr,
		Stderr:     os.Stderr,
	})
	if err != nil {
		return fmt.Errorf("run implement agent: %w", err)
	}

	if err := gh.PushBranch(ctx, branch); err != nil {
		return fmt.Errorf("push branch: %w", err)
	}

	body := prBody(iss.Number, readPlan(filepath.Join(res.WorktreePath, planFile)))

	url, err := gh.CreateDraftPR(ctx, ghio.PRSpec{
		Branch: branch,
		Title:  prTitle(iss.Title),
		Body:   body,
	})
	if err != nil {
		return fmt.Errorf("open draft PR: %w", err)
	}

	if err := gh.AddLabel(ctx, url, reviewLabel); err != nil {
		return fmt.Errorf("label PR: %w", err)
	}

	fmt.Println(url)
	return nil
}

// slugBranch builds the deterministic per-issue branch name
// "agent/issue-<n>-<slug>", where slug is the lowercased title with every
// non-alphanumeric run collapsed to a single dash, edges trimmed, capped at 50
// characters (trailing dash removed after the cap).
func slugBranch(number int, title string) string {
	var b strings.Builder
	b.Grow(len(title))
	prevDash := false
	for _, r := range strings.ToLower(title) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		// Collapse any run of non-alphanumerics to a single dash.
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > 50 {
		slug = slug[:50]
	}
	slug = strings.TrimRight(slug, "-")
	return fmt.Sprintf("agent/issue-%d-%s", number, slug)
}

// prBody assembles the draft PR body: a "Closes #<n>" line followed by the
// agent's plan, or a fallback note when the agent produced no plan.
func prBody(number int, plan string) string {
	if plan != "" {
		return fmt.Sprintf("Closes #%d\n\n%s", number, plan)
	}
	return fmt.Sprintf("Closes #%d\n\nImplemented by the taboo agent for issue #%d.\n\n_(No plan file was produced; see the commit for details.)_\n", number, number)
}

// prTitle caps an issue title at 256 characters for use as the PR title.
func prTitle(title string) string {
	if len(title) > 256 {
		return title[:256]
	}
	return title
}

// readPlan returns the contents of the plan file, or "" if it is absent/unreadable.
func readPlan(path string) string {
	// The path is derived from the trusted run's worktree plus a fixed filename,
	// not from end-user input.
	data, err := os.ReadFile(path) // #nosec G304
	if err != nil {
		return ""
	}
	return string(data)
}
