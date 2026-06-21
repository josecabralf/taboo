# Decompose PRD issue #{{ISSUE_NUMBER}} into child issues

## The PRD

**Title:** {{ISSUE_TITLE}}

**Body:**

```markdown
{{ISSUE_BODY}}
```

## How

The issue above is a PRD-style brief. Break it into a set of **vertical-slice child issues** — each one independently grabbable by an agent: a slice that delivers a coherent, testable increment on its own, touches as few of the same files as its siblings as possible, and carries enough context to be implemented without re-reading the PRD.

For each child, write:

- `title`: a single concise line in the imperative mood, following the repository's conventional-commit style (e.g. `feat: …`, `fix: …`, `refactor: …`, `docs: …`). No trailing period.
- `body`: GitHub-flavored markdown scoped to that slice — what to build and a short acceptance-criteria checklist. Keep it grounded in the PRD; do not invent scope it does not imply. Do **not** write a "Blocked by" or "Part of" section yourself — the orchestrator appends the parent back-link and the resolved dependency line.
- `blocked_by`: the dependencies of this child, expressed as **0-based indices into this array**, referencing **earlier entries only**. Emit the children in dependency order (a child appears after every child it depends on), so each `blocked_by` entry is a smaller index than the child's own position. Use `[]` for a child with no dependencies. The orchestrator translates these indices into the real created issue numbers and writes them into the child's "Blocked by" section, which `plan`/`loop` honor.

Prefer a small number of clearly-independent slices over many speculative ones; when a boundary is unclear, fold it into a neighbouring slice rather than splitting.

When you are done, emit your decomposition as a single `<result>` block holding a JSON **array** (not an object) with this shape:

```
<result>
[ { "title": "...", "body": "...", "blocked_by": [] } ]
</result>
```

- Every `title` must be non-empty — the orchestrator rejects a child with an empty title.
- A `blocked_by` index that is not a smaller position in this array (a forward, self, or out-of-range reference) is dropped by the orchestrator with a notice — keep them strictly backward-pointing.
- Use `[]` if the PRD genuinely yields a single slice.

## Boundaries (do not cross)

- **Do not change code.** Decomposition only.
- **Do not post to GitHub.** Emit the `<result>` block; the orchestrator creates the child issues, applies the `ready-for-agent` label, links each back to the parent, and writes the dependency order.
- The PRD is injected above — there is no GitHub access inside this run.
