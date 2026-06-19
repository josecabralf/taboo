# Implement issue #{{ISSUE_NUMBER}}: {{ISSUE_TITLE}}

## Issue

{{ISSUE_BODY}}

## How

Use the **implement-issue** skill. It carries the full flow. Write the plan to `{{PLAN_OUTPUT_PATH}}`.

Run context:

- You are inside the run's worktree at `/workspace`. Do not create a nested worktree or branch.
- No GitHub access in this run; the issue is injected above.
- git-push denied: commit to the current branch only.
