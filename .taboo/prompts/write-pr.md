# Write a PR for branch `{{BRANCH}}`

## Diff

These are the changes on `{{BRANCH}}` relative to `main` — the same set GitHub will show in the pull request:

```diff
{{DIFF}}
```

## How

From the diff above, write a pull-request **title** and **body** that describe the change to a reviewer.

- `title`: a single concise line in the imperative mood, following the repository's conventional-commit style (e.g. `feat: …`, `fix: …`, `refactor: …`, `docs: …`). No trailing period.
- `body`: GitHub-flavored markdown. Explain *what* changed and *why*, and call out anything a reviewer should look at. Keep it grounded in the diff — do not invent changes that are not shown above.

When you are done, emit your PR as a single `<result>` block holding JSON with this shape:

```
<result>
{ "title": "...", "body": "..." }
</result>
```

- `title` must be non-empty — the orchestrator rejects an empty title. `body` should be substantive, but only the title is enforced.

## Boundaries (do not cross)

- **Do not change code.** Describe the existing diff only.
- **Do not post to GitHub.** Emit the `<result>` block; the orchestrator opens the PR (or updates the branch's existing one).
- The diff is injected above — there is no GitHub access inside this run.
