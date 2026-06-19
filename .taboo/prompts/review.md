# Review PR #{{PR_NUMBER}}: {{PR_TITLE}}

Use the **review-pr** skill for the full review mechanics. This prompt only injects the issue, diff, and output path.

Write the review JSON to `{{REVIEW_OUTPUT_PATH}}`.

## Linked issue

#{{ISSUE_NUMBER}} — {{ISSUE_TITLE}}

{{ISSUE_BODY}}

## Diff

```diff
{{PR_DIFF}}
```
