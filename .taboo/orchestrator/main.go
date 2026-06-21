// Command afk is the taboo orchestrator entrypoint. It drives the AFK agent loop
// on the host's pkg. The "implement" subcommand fetches an issue, runs the
// implement workflow (the agent commits in place, push-denied), pushes the
// branch, opens a draft PR carrying the agent's plan, and applies the
// agent:review label. The "review" subcommand fetches a PR's diff, runs the
// review workflow for a structured <result>, drops any comment outside the diff,
// and posts exactly one PR review. The "plan" subcommand lists the open
// ready-for-agent issues and runs the plan workflow to print a parallel-safe
// batch of them as JSON. The "write-pr" subcommand computes a branch's diff
// against main, runs the write-pr workflow for a structured {title, body}
// <result>, and opens a PR for the branch — updating the branch's existing open
// PR instead of opening a duplicate when one is already present. The
// "update-branch" subcommand brings a PR's branch up to date with main: it
// resolves the PR's head branch, fetches origin, no-ops if main is already
// contained, else runs the update-branch workflow (its worktree started on the PR
// branch) to merge origin/main and validate in-workshop, then pushes the branch
// fast-forward — blocking the PR instead of pushing if validation fails. The
// "loop" subcommand is the master orchestrator: it drains the ready-for-agent backlog in
// bounded-parallel waves via taboo.Pool, planning a parallel-safe batch and
// fanning the implement workflow out across it each wave, and driving every issue
// through the agent:in-progress / agent:blocked label state machine. The
// "to-issues" subcommand fetches a PRD-style issue, runs the to-issues workflow
// for a structured []childIssue <result>, and creates each vertical-slice child
// with the ready-for-agent label, a back-link to the parent, and a "Blocked by
// #N" line resolving the child's declared dependency order. All
// GitHub/git I/O funnels through internal/ghio; the taboo runs go through the
// taboo bridge one-liners taboo.RunWorkflow / taboo.RunWorkflowAs (config
// discovery + resolution + run).
// In CI it is built inside its own module and run from the repo root (it cannot
// be `go run` from the parent module, which excludes nested modules); see
// .github/workflows/agent-implement.yml and agent-review.yml.
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

	"github.com/josecabralf/taboo"
)

// planFile is the path, relative to the run's worktree, where the implement agent
// writes its plan (mirrored into the draft PR body). It matches PLAN_OUTPUT_PATH.
const planFile = ".taboo-plan.md"

// reviewLabel is applied to the draft PR to cascade it into the review workflow.
const reviewLabel = "agent:review"

// ghClient is the subset of GitHub/git operations the implement sequence uses.
// *ghio.Client satisfies it; tests substitute a fake to record call order and
// args without shelling out.
type ghClient interface {
	IssueView(ctx context.Context, number int) (ghio.Issue, error)
	PushBranch(ctx context.Context, branch string) error
	CreateDraftPR(ctx context.Context, branch, title, body string) (string, error)
	AddLabel(ctx context.Context, prRef, label string) error
}

// workflowRunner runs a named taboo workflow discovered at or above startDir and
// returns the run's result. The taboo.RunWorkflow bridge satisfies it; tests
// substitute a fake that returns a canned OrchestratedResult (pointing
// WorktreePath at a temp dir) without provisioning a workshop.
type workflowRunner func(ctx context.Context, startDir, workflow string, vars map[string]string, ov taboo.PlanOverrides, cmd taboo.Commander) (taboo.OrchestratedResult, error)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "afk:", err)
		os.Exit(1)
	}
}

// usage summarizes the orchestrator's subcommands.
const usage = "usage: afk implement --issue <n> | afk review --pr <n> | afk plan | afk write-pr [--branch <branch>] [--ready] | afk update-branch --pr <n> | afk loop [--max-iterations <n>] [--parallelism <n>] [--dry-run] | afk to-issues --issue <n>"

// run dispatches to a subcommand: "implement", "review", "plan", "write-pr",
// "to-issues", "update-branch" or "loop".
func run(args []string) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "implement":
		return runImplement(context.Background(), args[1:])
	case "review":
		return runReview(context.Background(), args[1:])
	case "plan":
		return runPlan(context.Background(), args[1:])
	case "write-pr":
		return runWritePR(context.Background(), args[1:])
	case "to-issues":
		return runToIssues(context.Background(), args[1:])
	case "update-branch":
		return runUpdateBranch(context.Background(), args[1:])
	case "loop":
		return runLoop(context.Background(), args[1:])
	default:
		return fmt.Errorf("unknown command %q (%s)", args[0], usage)
	}
}

// runImplement drives one issue end-to-end on pkg: fetch the issue, run the
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

	startDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}

	return implement(ctx, startDir, *issue, ghio.New(ghio.NewExec()), taboo.RunWorkflow)
}

// runReview parses the review subcommand's flags, enforces --pr before any I/O,
// and wires the production gh and taboo seams into review. The typed bridge
// taboo.RunWorkflowAs[reviewResult] decodes the agent's <result> block in-loop.
func runReview(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	pr := fs.Int("pr", 0, "GitHub pull-request number to review")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *pr <= 0 {
		return errors.New("--pr is required")
	}

	startDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}

	return review(ctx, startDir, *pr, ghio.New(ghio.NewExec()), taboo.RunWorkflowAs[reviewResult])
}

// runPlan parses the plan subcommand (it takes no flags) and wires the production
// gh and taboo seams into plan. The typed bridge taboo.RunWorkflowAs[[]planItem]
// decodes the agent's <result> JSON array into []planItem in-loop.
func runPlan(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	startDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	return plan(ctx, startDir, os.Stdout, ghio.New(ghio.NewExec()), taboo.RunWorkflowAs[[]planItem])
}

// runWritePR parses the write-pr subcommand's flags (--branch defaults to the
// current branch; --ready marks the PR ready for review after refreshing it —
// the finalize behavior) and wires the production gh and taboo seams into
// writePR. The typed bridge taboo.RunWorkflowAs[prContent] decodes the agent's
// <result> block into a prContent in-loop.
func runWritePR(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("write-pr", flag.ContinueOnError)
	branch := fs.String("branch", "", "branch to open a PR for (default: the current branch)")
	ready := fs.Bool("ready", false, "after refreshing the PR, mark it ready for review (un-draft); already-ready is a no-op")
	if err := fs.Parse(args); err != nil {
		return err
	}
	startDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	return writePR(ctx, startDir, *branch, *ready, os.Stdout, ghio.New(ghio.NewExec()), taboo.RunWorkflowAs[prContent])
}

// runToIssues parses the to-issues subcommand's flags, enforces --issue before
// any I/O, and wires the production gh and taboo seams into toIssues. The typed
// bridge taboo.RunWorkflowAs[[]childIssue] decodes the agent's <result> JSON
// array into a []childIssue in-loop.
func runToIssues(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("to-issues", flag.ContinueOnError)
	issue := fs.Int("issue", 0, "GitHub PRD issue number to decompose into child issues")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *issue <= 0 {
		return errors.New("--issue is required")
	}
	startDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	return toIssues(ctx, startDir, *issue, os.Stdout, ghio.New(ghio.NewExec()), taboo.RunWorkflowAs[[]childIssue])
}

// runUpdateBranch parses the update-branch subcommand's flags, enforces --pr
// before any I/O, and wires the production gh and taboo seams into updateBranch.
// The typed bridge taboo.RunWorkflowAs[updateBranchResult] decodes the agent's
// <result> block into an updateBranchResult in-loop.
func runUpdateBranch(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("update-branch", flag.ContinueOnError)
	pr := fs.Int("pr", 0, "GitHub pull-request number whose branch to update with main")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *pr <= 0 {
		return errors.New("--pr is required")
	}

	startDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}

	return updateBranch(ctx, startDir, *pr, os.Stdout, ghio.New(ghio.NewExec()), taboo.RunWorkflowAs[updateBranchResult])
}

// runLoop parses the loop subcommand's flags and wires the production gh, plan,
// resolve, and pool seams into loop. It is the master orchestrator: it drains the
// ready-for-agent backlog wave by wave, fanning the implement workflow out across
// each parallel-safe batch through taboo.Pool.
func runLoop(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("loop", flag.ContinueOnError)
	maxIterations := fs.Int("max-iterations", defaultLoopMaxIterations, "maximum plan→fan-out waves before stopping")
	parallelism := fs.Int("parallelism", defaultLoopParallelism, "maximum concurrent implement runs per wave")
	dryRun := fs.Bool("dry-run", false, "print the selected plan without launching any agent")
	if err := fs.Parse(args); err != nil {
		return err
	}

	startDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}

	gh := ghio.New(ghio.NewExec())
	planBatch := func(ctx context.Context, dir string) ([]planItem, error) {
		return selectBatch(ctx, dir, gh, taboo.RunWorkflowAs[[]planItem])
	}
	resolve := func(dir, workflow string, vars map[string]string, ov taboo.PlanOverrides) (*taboo.Plan, error) {
		path, found := taboo.FindConfig(dir)
		if !found {
			return nil, fmt.Errorf("no taboo.yaml found from %s", dir)
		}
		cfg, err := taboo.LoadConfig(path)
		if err != nil {
			return nil, err
		}
		return cfg.Plan(filepath.Dir(path), workflow, vars, ov)
	}
	runPool := func(ctx context.Context, cfg taboo.Config, limit int, cmd taboo.Commander, reqs []taboo.RunRequest) ([]taboo.RunResult, error) {
		return taboo.NewPool(cfg, limit, cmd).Run(ctx, reqs)
	}

	return loop(ctx, startDir, loopOptions{
		maxIterations: *maxIterations,
		parallelism:   *parallelism,
		dryRun:        *dryRun,
	}, os.Stdout, gh, planBatch, resolve, runPool)
}

// implement is the testable core of the implement subcommand: it fetches the
// issue, runs the implement workflow, pushes the branch, opens a draft PR
// carrying the agent's plan, and applies the review label, in that order. The gh
// and taboo seams are injected so tests drive the full sequence with fakes; each
// step's failure is wrapped and short-circuits the rest.
func implement(ctx context.Context, startDir string, issue int, gh ghClient, runWorkflow workflowRunner) error {
	iss, err := gh.IssueView(ctx, issue)
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

	res, err := runWorkflow(ctx, startDir, "implement", vars, taboo.PlanOverrides{
		Branch: branch,
		Stdout: os.Stderr,
		Stderr: os.Stderr,
	}, taboo.NewExecCommander())
	if err != nil {
		return fmt.Errorf("run implement agent: %w", err)
	}

	if err := gh.PushBranch(ctx, branch); err != nil {
		return fmt.Errorf("push branch: %w", err)
	}

	// A missing or unreadable plan file is non-fatal: on any Artifact error we
	// fall back to "" and prBody renders its no-plan body — the outcome readPlan
	// gave for an absent file.
	plan, err := res.Artifact(planFile)
	if err != nil {
		plan = ""
	}
	body := prBody(iss.Number, plan)

	url, err := gh.CreateDraftPR(ctx, branch, prTitle(iss.Title), body)
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

// prTitle caps an issue title at 256 runes (characters) for use as the PR title.
// It truncates by rune, not byte, so a multibyte UTF-8 character is never split
// at the boundary.
func prTitle(title string) string {
	rs := []rune(title)
	if len(rs) > 256 {
		return string(rs[:256])
	}
	return title
}
