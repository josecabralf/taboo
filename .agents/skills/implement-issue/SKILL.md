---
name: implement-issue
description: Take an issue from exploration to a committed, test-covered implementation. Composes the tdd skill for the red-green mechanics. Use when implementing an issue end-to-end, AFK or via /implement-issue.
---

# Implement Issue

## Explore

- [ ] Read `CONTEXT.md` to match the project's domain language.
- [ ] Read the code the issue touches and its tests to learn the existing seams.
- [ ] Read relevant `docs/adr/` entries; respect the recorded decisions.

## Plan (MANDATORY)

Write the plan to `PLAN_OUTPUT_PATH` (default `.taboo-plan.md`) before any code. It becomes the PR body, so write it for a human reviewer.

- [ ] State what you'll change and why.
- [ ] Name the existing test seams you'll exercise.
- [ ] Call out risks and unknowns.

## TDD

Drive the implementation through the [tdd](../tdd/SKILL.md) skill's red-green-refactor loop.

- [ ] Test only through seams the code already offers; don't invent seams just to test.
- [ ] AFK: prefer a fresh sub-agent per red-green cycle.

## Validate

Your workshop carries the same toolchain CI runs.

- [ ] `make fmt`.
- [ ] `make lint test build`. Fix everything red before committing. CI reruns these on your branch, so green locally is the bar to clear.

## Commit

- [ ] Conventional-commit message(s) on the current branch.
- [ ] Do NOT push, open or label PRs, or touch the issue. The workflow layer owns all GitHub I/O.
