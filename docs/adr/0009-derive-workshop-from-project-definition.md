# Derive the agent's workshop from the project's own definition

## Status

accepted

## Context & decision

taboo's original model had taboo **own** a minimal `workshop.yaml` it rendered
from scratch (`renderDefinition`): base image + exactly one SDK (the agent) +
the mount plugs. That workshop carries the agent CLI but **none of the project's
toolchain** — no compiler, linter, formatter, or test runner. An agent inside it
therefore cannot run a validation flow or tests. For a headless agent (`claude
-p`, `opencode run`) with no interactive approver, that means it commits *blind*:
it cannot run a TDD loop, cannot self-correct against the project's checks, and
its diffs only fail on the next host-side CI pass.

The fix is to stop rendering a from-scratch definition and instead **derive** the
agent's workshop from the project's *own* `workshop.yaml`. The project already
declares its toolchain there (its `go` SDK, its `project-<x>` SDKs, its
`actions:`) for its human developers; taboo reuses that declaration and only adds
what the agent needs. This buys a strong invariant: **the agent's sandbox is the
dev's sandbox** — the agent validates against the exact environment humans and CI
use.

**Decision: derive, don't render.** For a managed project, taboo reads the
project's workshop definition, injects the agent SDK + mount plugs + a minted
name, and writes the result to its own git-ignored `.taboo/workshop.yaml`,
regenerated every run. This **inverts the ownership model** recorded in earlier
CONTEXT.md ("taboo owns this `workshop.yaml` template"): taboo no longer authors
the definition, it *augments* the project's.

This is settled. Scope, mechanics, and the rejected alternatives follow.

### Scope: workshop-using projects only

The feature requires the target project to already ship a `workshop.yaml`. taboo
does **not** infer or synthesize a toolchain for projects that don't use
workshop — that is an open-ended, language-specific problem (detect `go.mod` vs
`package.json` vs `pyproject.toml`, …) and a much larger, fuzzier product. A
project without a workshop definition is a hard, early error (`init`, defensively
`run`), not a fallback path. This keeps taboo language-agnostic: the project owns
its toolchain declaration exactly as it already does for its human devs.

### Derive a copy into `.taboo/`, never mutate the project's file

taboo reads the project's definition and writes the augmented result to
`<repo>/.taboo/workshop.yaml` — git-ignored (entry written by `init`),
regenerated each run, launched under a taboo-minted name. The project's own
`workshop.yaml` is never edited. Rationale:

- The project's file is tracked in the human's repo; mutating it would dirty the
  working tree and surprise the human, whose `workshop launch` would suddenly drag
  in an agent SDK and mounts they never declared.
- The project's named workshop is the human's; taboo's parallelism model stands
  up *N* workshops per wave (ADR 0006), which cannot share the project's single
  named definition.
- It preserves every invariant taboo already relies on: ownership of `.taboo/`,
  the git-ignored project dir, the nested-worktree layout (ADR 0007), one
  workshop per concurrent agent.

So the only thing that changes versus the old flow is the **seed** of the
rendered definition: from "empty" to "the project's definition."

### Opaque-tree injection: preserve everything taboo doesn't model

A real project definition carries far more than taboo's old `definition` struct
modeled (`name`/`base`/`sdks`): `actions:`, slots, `bind`-form plugs
(`PlugOrBind`), SDK channels, and whatever workshop's schema grows next.
Unmarshalling into a typed struct and re-marshalling would **silently drop**
every unmodeled field — quietly diverging the derived env from the dev env and
breaking the invariant this whole change exists to buy.

taboo therefore treats the project definition as an **opaque YAML tree**
(`yaml.Node`/map) and injects surgically, touching only:

- `sdks:` — append one entry: the agent SDK carrying the mount plugs (creating the
  sequence if absent);
- `name:` — overwrite with a taboo-minted, per-concurrent-agent-unique name;
- `base:` — inherit the project's (the toolchain SDKs were authored against it).

Everything else passes through verbatim. This also honors the
"no compile-time coupling to workshop" stance (CONTEXT.md, ADR on CLI-only
integration): taboo does not mirror workshop's evolving definition schema. The
old `renderDefinition`/`definition` struct **retires** for the managed-project
flow.

### Mount targets: namespace the relocatable ones, never fail on collision

Plug *names* are namespaced per SDK (`<ws>/<sdk>:<plug>`) and taboo's plugs hang
off its own agent SDK entry, so plug-name collisions with the project are
impossible. Mount *targets* share one in-workshop filesystem namespace, so they
can collide. taboo reserves three.

- **Worktree and sessions** targets move under a reserved **`/taboo/...`** prefix
  (e.g. `/taboo/workspace`, `/taboo/sessions`). taboo controls the agent's
  `--cwd` and the session-dir env var at exec time, so these are pure internal
  conventions — the agent never cares about the literal path. Namespacing them
  makes collision with the project's mounts **structurally impossible**: no
  detection step, no `init` failure. (Best devex: taboo adapts to the project, it
  does not ask the project author to fix their `workshop.yaml`.)
- **git-common** stays at the host `.git` absolute path. The two-mount rule
  (CONTEXT.md) requires it there so the linked worktree's `.git` pointer resolves
  without rewriting — the path *is* the mechanism, so it cannot be relocated. Its
  target is a host-specific absolute path no real definition would mount inside a
  workshop, so it is left **unchecked** rather than guarded by a branch that could
  only ever fire on a pathological def.

### In-project SDK resolution: quarantine + symlink

workshop resolves in-project SDKs relative to the launch project dir
(`ProjectSdkPath(project, name) = <project>/.workshop/<name>`). Since taboo
launches with `--project <repo>/.taboo`, a `project-<x>` SDK the source def
references would be looked up at `<repo>/.taboo/.workshop/<x>` — which doesn't
exist. Store SDKs (e.g. `go`) are unaffected; they are fetched from the store
regardless of project dir.

**Chosen: quarantine in `.taboo/`, symlink the project's in-project SDKs in.**
For each `project-<x>` in the source def, taboo creates
`.taboo/.workshop/<x>` → symlink to `<repo>/.workshop/<x>`, alongside its own
seeded agent SDK. The link set is **reconciled every run** (add missing, prune
stale) so it tracks the source def exactly. The SDK source is symlinked, not
copied, so there is no staleness. `clean` deletes the **links themselves** and
never recurses through them into the project's real `.workshop/<x>` (a data-loss
hazard worth its own test).

The alternative — launch natively against the repo root and write the derived def
as a second named definition in the project's `.workshop/` — resolves in-project
SDKs for free and shares the project-id + SDK volume cache, but it writes taboo
artifacts into the human-owned `.workshop/` dir (the exact surprise this design
avoids) and mounts the whole repo root into the agent's workshop. Quarantine was
chosen for isolation and to keep the human's `.workshop/` read-only-referenced;
the cache-sharing win is one-time and the workshops are long-lived/amortized, so
it does not pay for the lost quarantine.

### Drift: fingerprint and refresh

The derived def is now a function of an external, mutable input (the project's
`workshop.yaml`), while the execution model launches a workshop once and
amortizes it (`stop → remount → start` per run, which does not pick up SDK/plug
changes — adding an SDK/plug needs a `refresh`). So an edit to the project's
toolchain would leave the long-lived agent workshop stale.

taboo **fingerprints** the derived def (hash of the generated YAML) and records
what the live workshop was provisioned with. Each run: regenerate, compare. Same
→ reuse (the amortization fast path). Changed → `refresh` (or relaunch) before the
run. The fingerprint is the same artifact taboo computes to write the regenerated
`.taboo/workshop.yaml`, so the check is nearly free. This keeps "agent sandbox ==
dev sandbox" true *over time*, not just at first launch.

### Why source-independent provisioning is safe under quarantine

A worry with quarantine is that `.taboo` (nearly empty) is mounted as the project
dir while the real source sits at the worktree mount — could a toolchain SDK's
setup expect the source at build time? No: workshop SDK setup hooks run **as root
before the project is mounted** (verified in taboo's own
`.workshop/taboo/hooks/setup-base` header). They provision the rootfs (install
tools) independent of any source. The agent then runs those tools in its `--cwd`
(the worktree). So the quarantine cannot break provisioning. Workshops are always
Ubuntu-based, so the agent SDKs' `apt-get` provisioning is always valid.

## Considered options

- **Derive from the project's definition (chosen).** Buys "agent sandbox == dev
  sandbox," reuses workshop's native dependency mechanism, stays language-
  agnostic. Costs the ownership inversion and opaque-tree injection complexity.
- **Render from scratch + inject the project's toolchain ourselves (rejected).**
  Requires taboo to detect and model each language's toolchain — open-ended,
  fragile, and duplicates what the project's own definition already states.
- **Let the host run validation after the run, agent commits blind (rejected as
  the primary path).** Consistent with "commit in place; host owns integration"
  (CONTEXT.md), and still available to workflow automation as a host-side gate.
  But it gives the agent no inner correction loop: a headless agent cannot run
  TDD or self-check, so diff quality suffers and the host-CI round-trip is the
  only feedback. The two are complementary, not exclusive — this ADR adds the
  inner loop; the host gate remains for publishing/integration.

## Consequences

- **Ownership inversion.** taboo no longer authors the workshop definition; it
  augments the project's. CONTEXT.md's "taboo owns this `workshop.yaml` template"
  framing is superseded for managed projects and updated to point here.
- **Scoped to workshop-using projects.** Toolchain-less projects are out of scope
  (hard error), not a fallback.
- **`renderDefinition` retires** for the managed-project flow; opaque-tree
  injection replaces it. It survives only if toolchain-less support is ever added.
- **New per-run materialization of `.taboo/`.** `run` regenerates the derived def,
  the agent-SDK seed, and the in-project SDK symlink set every run (single source
  of truth, self-healing). `init` only records config (agent, `source-definition`)
  and writes the gitignore entry.
- **Source-definition selection** is required for multi-definition projects
  (workshop has no implicit default): single def is automatic; multiple needs a
  recorded `source-definition` / `--from`, else a hard error listing candidates.
- **Reversible per concern.** The symlink-vs-second-definition choice and the
  fingerprint-refresh policy are each local; nothing else in the library depends
  on the specific mechanism.
- Implemented across issues #68 (derive + launch, single-def, the tracer bullet),
  #69 (source-definition selection), #70 (drift fingerprint-and-refresh), #71
  (`validate`/`doctor` surface).
