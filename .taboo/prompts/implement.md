# Implement issue #{{ISSUE_NUMBER}}: {{ISSUE_TITLE}}

## Issue

{{ISSUE_BODY}}

## How

Use the **implement-issue** skill — it carries the full mechanics; this prompt only injects the issue above.

Follow its flow: explore the codebase → write a mandatory plan to `{{PLAN_OUTPUT_PATH}}` → run **/tdd** for the tracer-bullet red-green-refactor loop (one test → one impl, vertical slices) → validate → commit in place.

Boundaries (do not cross):

- You are **git-push denied**. Commit to the current branch only; do not push, open a PR, or touch labels. taboo owns the worktree and the workflow layer owns all GitHub I/O.
- You are already inside the run's worktree at `/workspace`. Do **not** create a nested worktree or branch.
- The issue is injected above — there is no GitHub access inside this run.
