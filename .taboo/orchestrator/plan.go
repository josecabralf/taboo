package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"afk/internal/ghio"

	taboo "github.com/josecabralf/taboo/pkg"
)

// readyLabel is the label an issue carries once it is ready for an agent to pick
// up. The plan subcommand lists the open issues bearing it as its candidates.
const readyLabel = "ready-for-agent"

// inProgressLabel is the label an issue carries while an agent is actively
// working it. The plan subcommand excludes such issues so one already in flight
// is never re-selected.
const inProgressLabel = "agent:in-progress"

// planItem is one issue selected for the next parallel batch. The agent emits
// number and title in its <result> JSON array (decoded by
// taboo.RunWorkflowAs[[]planItem]); selectBatch then canonicalizes each item
// against the candidate it names, filling Branch via slugBranch so the branch
// name has a single source of truth shared with the implement flow.
type planItem struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	// Branch is derived by the orchestrator (slugBranch), not emitted by the
	// agent; it is part of the JSON the plan subcommand and loop dry-run print.
	Branch string `json:"branch"`
}

// planGH is the subset of GitHub I/O the plan sequence uses. *ghio.Client
// satisfies it; tests substitute a fake to record calls and args.
type planGH interface {
	ListOpenIssuesByLabel(ctx context.Context, label string) ([]ghio.Issue, error)
	IssueState(ctx context.Context, number int) (string, error)
}

// planRunner runs the plan workflow discovered at or above startDir and returns
// its structured result already decoded as a []planItem. The typed bridge
// taboo.RunWorkflowAs[[]planItem] satisfies it; tests substitute a fake that
// returns a canned slice directly, without provisioning a workshop or
// hand-encoding a <result> block.
type planRunner func(ctx context.Context, startDir, workflow string, vars map[string]string, ov taboo.PlanOverrides, cmd taboo.Commander) ([]planItem, taboo.OrchestratedResult, error)

// selectBatch is the testable core of the plan sequence: list the open
// ready-for-agent issues, drop those already in progress or with an unresolved
// "Blocked by #N" dependency, hand the survivors to the plan workflow agent
// (which selects a parallel-safe subset and emits it as a <result> JSON array
// decoded into []planItem by the bridge), then canonicalize each pick against the
// candidate it names — taking title and the slugBranch-derived branch from the
// candidate, not the agent's echo, so the branch has one source of truth — and
// return that batch. When nothing is eligible it returns a non-nil empty slice
// and skips the agent run, so a caller can marshal it to [] (never null) and skip
// wasted work. The gh and taboo seams are injected so tests drive the full
// sequence with fakes.
func selectBatch(ctx context.Context, startDir string, gh planGH, runPlan planRunner) ([]planItem, error) {
	ready, err := gh.ListOpenIssuesByLabel(ctx, readyLabel)
	if err != nil {
		return nil, fmt.Errorf("list ready issues: %w", err)
	}

	inProgress, err := gh.ListOpenIssuesByLabel(ctx, inProgressLabel)
	if err != nil {
		return nil, fmt.Errorf("list in-progress issues: %w", err)
	}

	candidates, err := unblockedCandidates(ctx, gh, excludeInProgress(ready, inProgress))
	if err != nil {
		return nil, err
	}

	// With nothing eligible, running the plan agent would be wasted work (and it
	// could hallucinate). Return an empty selection and skip the run, mirroring
	// review's graceful short-circuit. The slice is non-nil so json.Marshal
	// yields [] rather than null.
	if len(candidates) == 0 {
		fmt.Fprintln(os.Stderr, "afk: no eligible ready-for-agent issues to plan")
		return []planItem{}, nil
	}

	candidatesJSON, err := json.MarshalIndent(candidates, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal candidates: %w", err)
	}

	vars := map[string]string{
		"CANDIDATES": string(candidatesJSON),
	}

	selected, _, err := runPlan(ctx, startDir, "plan", vars, taboo.PlanOverrides{
		Stdout: os.Stderr,
		Stderr: os.Stderr,
	}, taboo.NewExecCommander())
	if err != nil {
		return nil, fmt.Errorf("run plan agent: %w", err)
	}
	return canonicalBatch(candidates, selected), nil
}

// canonicalBatch rebuilds the agent's selection from the candidate issues the
// orchestrator already holds, keyed by the number the agent picked. Title and
// branch come from the candidate — the branch via slugBranch, the same function
// the implement flow uses — not from the agent's echo, so the branch has a single
// source of truth. A picked number that is not among the candidates (an agent
// invention) is dropped with a stderr notice rather than trusted. The returned
// slice is non-nil so it marshals to [] rather than null.
func canonicalBatch(candidates []ghio.Issue, selected []planItem) []planItem {
	byNum := make(map[int]ghio.Issue, len(candidates))
	for _, c := range candidates {
		byNum[c.Number] = c
	}

	batch := make([]planItem, 0, len(selected))
	for _, it := range selected {
		cand, ok := byNum[it.Number]
		if !ok {
			fmt.Fprintf(os.Stderr, "afk: skipping #%d: not among the planned candidates\n", it.Number)
			continue
		}
		batch = append(batch, planItem{
			Number: cand.Number,
			Title:  cand.Title,
			Branch: slugBranch(cand.Number, cand.Title),
		})
	}
	return batch
}

// plan is the plan subcommand: it delegates the listing, filtering, and
// agent-run logic to selectBatch and writes the resulting selection as a JSON
// array to out (an empty selection prints as []). The gh and taboo seams are
// injected so tests drive the full sequence with fakes.
func plan(ctx context.Context, startDir string, out io.Writer, gh planGH, runPlan planRunner) error {
	items, err := selectBatch(ctx, startDir, gh, runPlan)
	if err != nil {
		return err
	}

	data, err := json.Marshal(items)
	if err != nil {
		return fmt.Errorf("marshal selection: %w", err)
	}
	if _, err := fmt.Fprintln(out, string(data)); err != nil {
		return fmt.Errorf("write selection: %w", err)
	}
	return nil
}

// excludeInProgress returns the ready issues whose numbers are not among the
// in-progress ones, preserving the ready order. An issue already being worked is
// thus never re-offered as a candidate.
func excludeInProgress(ready, inProgress []ghio.Issue) []ghio.Issue {
	busy := make(map[int]bool, len(inProgress))
	for _, i := range inProgress {
		busy[i.Number] = true
	}

	candidates := make([]ghio.Issue, 0, len(ready))
	for _, i := range ready {
		if !busy[i.Number] {
			candidates = append(candidates, i)
		}
	}
	return candidates
}

// blockedByRe matches a "Blocked by #N" declaration and captures the run of
// references that follows it — whether inline ("#N, #M"), colon-introduced, or a
// bulleted markdown list with one "#N" per line. A blank line ends the run, so
// references in a later section are not swept in; issueRefRe then pulls each "#N"
// out of that captured run.
var (
	blockedByRe = regexp.MustCompile(`(?i)blocked by\b[:\s]*((?:[-*]?[ \t]*#\d+[ \t]*,?[ \t]*\n?)+)`)
	issueRefRe  = regexp.MustCompile(`#(\d+)`)
)

// parseBlockedBy extracts the issue numbers a body declares it is blocked by. It
// matches "Blocked by #N" (case-insensitive) and the references that follow,
// whether inline ("#N, #M") or a bulleted markdown list, returning the distinct
// numbers in first-seen order. References not introduced by "blocked by" (e.g.
// "fixes #9") are ignored.
func parseBlockedBy(body string) []int {
	var nums []int
	seen := make(map[int]bool)
	for _, m := range blockedByRe.FindAllStringSubmatch(body, -1) {
		for _, ref := range issueRefRe.FindAllStringSubmatch(m[1], -1) {
			n, err := strconv.Atoi(ref[1])
			if err != nil || n <= 0 || seen[n] {
				continue
			}
			seen[n] = true
			nums = append(nums, n)
		}
	}
	return nums
}

// isBlocked reports whether the issue has any unresolved "Blocked by #N"
// dependency: it is blocked if any referenced issue is not CLOSED. States are
// read through stateCache so a dependency shared by several candidates is fetched
// from GitHub only once per plan run.
func isBlocked(ctx context.Context, gh planGH, iss ghio.Issue, stateCache map[int]string) (bool, error) {
	for _, dep := range parseBlockedBy(iss.Body) {
		state, ok := stateCache[dep]
		if !ok {
			var err error
			state, err = gh.IssueState(ctx, dep)
			if err != nil {
				return false, err
			}
			stateCache[dep] = state
		}
		if !strings.EqualFold(state, "CLOSED") {
			return true, nil
		}
	}
	return false, nil
}

// unblockedCandidates returns the candidates whose "Blocked by #N" dependencies
// are all resolved, dropping (with a stderr notice) any that are still blocked. A
// single state cache spans the batch so a dependency shared by several candidates
// costs one IssueState lookup.
func unblockedCandidates(ctx context.Context, gh planGH, candidates []ghio.Issue) ([]ghio.Issue, error) {
	stateCache := make(map[int]string)
	unblocked := make([]ghio.Issue, 0, len(candidates))
	for _, iss := range candidates {
		blocked, err := isBlocked(ctx, gh, iss, stateCache)
		if err != nil {
			return nil, fmt.Errorf("check dependencies for #%d: %w", iss.Number, err)
		}
		if blocked {
			fmt.Fprintf(os.Stderr, "afk: skipping #%d: blocked by an unresolved dependency\n", iss.Number)
			continue
		}
		unblocked = append(unblocked, iss)
	}
	return unblocked, nil
}
