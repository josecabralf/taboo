# Plan the next parallel batch

## Candidates

These are the open `ready-for-agent` issues, already filtered to remove the ones currently in progress and the ones blocked by other open work:

```json
{{CANDIDATES}}
```

## How

From the candidates above, select a subset that is **safe to run in parallel** — issues that do not touch the same files and whose changes will not conflict with one another. Prefer a smaller, clearly-independent batch over a larger speculative one; when in doubt, leave an issue out.

For each issue you select, emit a branch name of the form `agent/issue-<number>-<slug>`, where the slug is the lowercased title with every run of non-alphanumeric characters collapsed to a single dash, leading and trailing dashes removed, and the result capped at 50 characters (matching the implement workflow's `slugBranch`).

When you are done, emit your selection as a single `<result>` block holding a JSON **array** (not an object) with this shape:

```
<result>
[ { "number": 0, "title": "...", "branch": "agent/issue-0-..." } ]
</result>
```

- Select only from the candidates above — do not invent numbers.
- Use `[]` if nothing is safe to parallelize.

## Boundaries (do not cross)

- **Do not change code.** Planning only.
- **Do not post to GitHub.** Emit the `<result>` block; the orchestrator handles the rest.
- The candidates are injected above — there is no GitHub access inside this run.
