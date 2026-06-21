# Plan the next parallel batch

## Candidates

These are the open `ready-for-agent` issues, already filtered to remove the ones currently in progress and the ones blocked by other open work:

```json
{{CANDIDATES}}
```

## How

From the candidates above, select a subset that is **safe to run in parallel** — issues that do not touch the same files and whose changes will not conflict with one another. Prefer a smaller, clearly-independent batch over a larger speculative one; when in doubt, leave an issue out.

Emit only the number and title of each issue you select — the orchestrator derives the branch name and everything else. Your job is to choose *which* issues run together, not to produce their branches.

When you are done, emit your selection as a single `<result>` block holding a JSON **array** (not an object) with this shape:

```
<result>
[ { "number": 0, "title": "..." } ]
</result>
```

- Select only from the candidates above — do not invent numbers; any number that is not a candidate is dropped.
- Use `[]` if nothing is safe to parallelize.

## Boundaries (do not cross)

- **Do not change code.** Planning only.
- **Do not post to GitHub.** Emit the `<result>` block; the orchestrator handles the rest.
- The candidates are injected above — there is no GitHub access inside this run.
