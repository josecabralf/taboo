# Update branch `{{BRANCH}}` with `main`

You are on branch `{{BRANCH}}` — PR #{{PR_NUMBER}}'s branch, checked out at its
current tip. It is behind `main` and needs to be brought up to date so the PR
merges cleanly.

## How

1. **Merge main into the branch.** Run `git merge --no-edit origin/main`.
   `origin/main` has already been fetched and is available locally — you do not
   need network access.
2. **Resolve any conflicts.** If the merge stops on conflicts, resolve them so
   *both* sides' intent is preserved (the branch's change and main's change), then
   stage and commit: `git add -A && git commit --no-edit`.
3. **Validate the merged tree in this workshop.** Run `make lint test build`. The
   toolchain is already present here — run the targets **directly**. Do **not**
   run `workshop run`; you are already inside the workshop.

## Result

Emit exactly one `<result>` block holding JSON with this shape:

```
<result>
{ "updated": true, "validated": true, "summary": "merged origin/main, resolved 2 conflicts in runner.go, make lint test build all pass" }
</result>
```

- `updated`: `true` if the merge created a commit; `false` only if git reported
  "Already up to date" (nothing to merge).
- `validated`: `true` only if `make lint`, `make test`, and `make build` all
  succeed on the merged tree. If any fail, report `false` — do not hide it; the
  orchestrator blocks the PR rather than pushing a broken merge.
  If `updated` is `false` (nothing to merge), there is nothing to validate — set
  `validated` to `true`.
- `summary`: one line describing what you merged, any conflicts you resolved, and
  the validation outcome.

## Boundaries (do not cross)

- **Do not push.** You are git-push-denied; the orchestrator pushes the branch
  after it confirms validation passed.
- **Do not open, edit, or comment on the PR.** Emit the `<result>` block only; the
  orchestrator owns all GitHub interaction.
- **Make no change beyond the merge.** Resolve conflicts and commit the merge —
  do not refactor, reformat, or "improve" unrelated code while you are here.
