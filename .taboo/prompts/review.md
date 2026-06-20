# Review PR #{{PR_NUMBER}}

## Diff

```diff
{{PR_DIFF}}
```

## How

Use the **review-pr** skill — it carries the full review mechanics; this prompt only injects the diff above.

When you are done, emit your review as a single `<result>` block holding JSON with this shape:

```
<result>
{ "summary": "...", "comments": [ { "path": "...", "line": 0, "body": "..." } ] }
</result>
```

- `summary` is the single top-level review comment (use `""` if you have nothing overall to say).
- `comments` are inline comments, each anchored to a `path` and a new-side `line` **present in the diff above**. The orchestrator drops any comment whose `path:line` is not addressable in the diff, so anchor carefully; use `[]` if you have no inline comments.

Boundaries (do not cross):

- **Do not change code.** Review only.
- **Do not post to GitHub.** Emit the `<result>` block; the orchestrator posts the single review.
- The PR diff is injected above — there is no GitHub access inside this run.
