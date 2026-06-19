---
name: implement-issue
description: Orchestrate an issue from explore through a committed, test-covered implementation, composing the tdd skill for the red-green mechanics. Use when implementing a GitHub issue end-to-end, AFK or via /implement-issue.
---

# Implement Issue

## Explore

**Understand the area before you touch it.**

- [ ] Read `CONTEXT.md` so names and vocabulary match the project's domain language.
- [ ] Read the code the issue touches and its existing tests — learn the seams that are already there.
- [ ] Read any `docs/adr/` entries covering the area; respect the recorded decisions.

## Plan (MANDATORY)

**Write the plan before you write any code.** Write it to the path the prompt gives you (`PLAN_OUTPUT_PATH`, default `.taboo-plan.md`). This file becomes the pull-request body, so write it for a human reviewer.

- [ ] State what you'll change and why.
- [ ] Name the test seams you'll exercise (reuse existing ones — see Explore).
- [ ] Call out the risks and anything you're unsure about.

## TDD

**Implement test-first via tracer-bullet + red-green-refactor.** Use the [tdd](../tdd/SKILL.md) skill for the mechanics — do not restate them here.

- [ ] Drive the implementation through the tdd loop.
- [ ] Test only through the seams the code already offers; do not invent new test seams just to have something to test.
- [ ] When running AFK, prefer a fresh sub-agent per red-green cycle.

## Validate

**Get the project's checks green in your workshop before you commit.**

Your workshop is derived from the project's own `workshop.yaml`, so it carries the
full toolchain — Go, `golangci-lint`, and `make` — the same checks CI runs.

- [ ] Format with `make fmt`.
- [ ] Run `make lint test build` and fix what they report; do not commit while any of them is red.
- [ ] This is your inner loop. The PR's CI runs the same `make lint test build` again on the branch you produce, so it stays the authoritative gate. Green locally is the bar to clear, not a substitute for that gate.

## Commit

**Commit on the current branch and stop there.**

- [ ] Use conventional-commit message(s).
- [ ] Do NOT push, do NOT create or label PRs, do NOT touch the issue.

## Boundaries

- The agent is git-push denied; taboo owns the worktree and branch.
- The workflow layer (`.github/`) owns all GitHub I/O — fetching the issue, pushing the branch, opening and labelling the PR. Your job ends at the local commit.
