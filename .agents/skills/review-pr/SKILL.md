---
name: review-pr
description: Review a pull-request diff and emit inline + top-level comments as JSON for the workflow to post — never posts to GitHub itself. Use when reviewing a PR AFK or via /review-pr.
---

# Review PR

## Read

**Ground the review in the diff and the issue it claims to close.**

- [ ] Read the injected diff end to end — it is the only code you may comment on.
- [ ] Read the injected linked-issue context (its body, title, and number) and confirm the diff actually satisfies it.
- [ ] Check **correctness**: does the change do what the issue asks, with no logic bugs or unhandled edge cases?
- [ ] Check **clarity**: are names, structure, and comments understandable to the next reader?
- [ ] Check **consistency**: does it match the repo's conventions, layout, and existing patterns (see `CLAUDE.md`)?
- [ ] Check **tests**: is the new behavior covered, and do the tests verify behavior through public interfaces (delegate to the [tdd](../tdd/SKILL.md) skill for what good tests look like)?

## Comment

**Produce inline comments anchored to the diff, plus one top-level summary.**

- [ ] Write each inline comment against a line that appears in the diff — capture its `path` (repo-relative) and `line` (the line number on the NEW / right side of the diff).
- [ ] Keep each `body` a focused, actionable markdown note: what is wrong and what to do about it.
- [ ] Write one `topLevelComment`: an overall summary and verdict (does it satisfy the issue, what must change before merge).
- [ ] If the diff is clean, still write the `topLevelComment` and use an empty `inlineComments` array.

## Output

**Write the review JSON to the path given in the prompt** (`REVIEW_OUTPUT_PATH`, default `.taboo-review.json`) in the `/workspace` root, EXACTLY this shape:

```json
{
  "topLevelComment": "…",
  "inlineComments": [
    { "path": "rel/path.go", "line": 123, "body": "…" }
  ]
}
```

- [ ] `topLevelComment` is a string; `inlineComments` is an array (use `[]` when there are none).
- [ ] Each inline entry has `path` (repo-relative string), `line` (number on the NEW / right side of the diff), and `body` (markdown string).
- [ ] No trailing prose outside the JSON file — the workflow parses it verbatim.

## Boundaries

**Review only — the workflow does all GitHub I/O.**

- [ ] Do NOT change code, fix bugs, or touch any file other than the review JSON output.
- [ ] Do NOT post GitHub comments or reviews yourself, and do NOT push.
- [ ] Anchor inline comments carefully: the workflow DROPS any inline comment whose `path`:`line` is not present in the diff, then posts ONE review from what survives.
