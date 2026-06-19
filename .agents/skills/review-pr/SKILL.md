---
name: review-pr
description: Review a PR diff and emit inline + top-level comments as JSON for the workflow to post. Use when reviewing a PR AFK or via /review-pr.
---

# Review PR

The deliverable is a **file**, not a chat message. Write the review JSON to the output path (see [Output](#output)) using the Write tool. A review left only in chat is discarded and the run fails.

## Read

- [ ] Read the diff end to end. Its lines are the only anchors for inline comments.
- [ ] The full repo at the PR's state is checked out: open the changed files and trace their callers, callees, and tests for ripple effects beyond the diff.
- [ ] Read the linked issue and confirm the diff actually satisfies it.
- [ ] **Correctness**: does it do what the issue asks, with no logic bugs or unhandled edge cases?
- [ ] **Clarity**: are names, structure, and comments clear to the next reader?
- [ ] **Consistency**: does it match the repo's conventions and patterns (see `AGENTS.md`)?
- [ ] **Tests**: is the new behavior covered, through public interfaces (see [tdd](../tdd/SKILL.md))?

## Comment

- [ ] One inline comment per issue, anchored to a `path` (repo-relative) and `line` (NEW / right side of the diff); `body` says what is wrong and what to do.
- [ ] One `topLevelComment`: overall summary and verdict. Does it satisfy the issue, and what must change before merge.

## Output

Write the JSON to the path given in the prompt (`REVIEW_OUTPUT_PATH`, default `.taboo-review.json` in `/workspace`), EXACTLY this shape:

```json
{
  "topLevelComment": "…",
  "inlineComments": [
    { "path": "rel/path.go", "line": 123, "body": "…" }
  ]
}
```

- [ ] `topLevelComment` is a string; `inlineComments` is an array (use `[]` when there are none).
- [ ] Nothing outside the JSON file — the workflow parses it verbatim.

## Boundaries

- [ ] Do NOT change code or touch any file other than the review JSON.
- [ ] Do NOT post to GitHub or push. The workflow does all GitHub I/O.
- [ ] The workflow DROPS any inline comment whose `path`:`line` is not in the diff, then posts one review from what survives. Anchor carefully.
