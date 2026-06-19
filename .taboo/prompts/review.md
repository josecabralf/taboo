# Review PR #{{PR_NUMBER}}: {{PR_TITLE}}

## Linked issue

#{{ISSUE_NUMBER}} — {{ISSUE_TITLE}}

{{ISSUE_BODY}}

## Diff

```diff
{{PR_DIFF}}
```

## How

Use the **review-pr** skill — it carries the full review mechanics; this prompt only injects the issue and diff above.

Write the review to `{{REVIEW_OUTPUT_PATH}}` as JSON with this shape:

```json
{ "topLevelComment": "...", "inlineComments": [ { "path": "...", "line": 0, "body": "..." } ] }
```

Emit both inline comments (anchored to `path` + `line` in the diff) and a single top-level summary comment.

Boundaries (do not cross):

- **Do not change code.** Review only.
- **Do not post to GitHub.** Write the JSON file; the workflow layer posts the review.
- The PR and issue are injected above — there is no GitHub access inside this run.
