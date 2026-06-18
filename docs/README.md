# taboo documentation

taboo is a Go library that orchestrates AI coding agents inside Canonical workshop environments. The library (`pkg/taboo`) is the primary contract; the `taboo` CLI wraps the common paths.

New here? Start with one of the two tutorials below: [the library first run](tutorials/library-first-run.md) drives an agent from Go, and [the CLI first run](tutorials/cli-first-run.md) drives one from `taboo run`.

## Tutorials

Learning-oriented, start-to-finish walkthroughs.

- [Library first run](tutorials/library-first-run.md) — go from a small Go program to a commit on a host branch with `Config`, `New`, and `Run`.
- [CLI first run](tutorials/cli-first-run.md) — `go install` the `taboo` binary, scaffold a project with `taboo init`, set one credential, check the host with `taboo doctor`, then `taboo run fix`.

## How-to guides

Goal-oriented recipes for a single task.

- [Iterate until done](guides/iterate-until-done.md) — re-run an agent with `Orchestrator` until it emits a completion signal or hits the iteration cap.
- [Fan out runs](guides/fan-out-runs.md) — run many prompts in parallel with `Pool`.
- [Typed results](guides/typed-results.md) — decode a structured result from agent output with `JSONResult` and validate it.
- [Prepare the workspace with hooks](guides/prepare-the-workspace-with-hooks.md) — run setup commands with `Hooks` before the agent starts.

## Reference

Look-it-up facts about the API and the configuration surface.

- [Library API](reference/library-api.md) — the exported `pkg/taboo` surface: entry points, request and result types, building blocks, errors.
- [Agents](reference/agents.md) — the three supported agents, their credential env keys, prompt delivery, sessions, and model hints.
- [CLI](reference/cli.md) — every `taboo` command, its flags, output, and exit behaviour.
- [taboo.yaml](reference/taboo-yaml.md) — every config key, its type and default, and the precedence chain.

## Explanation

Why taboo is built the way it is.

- [Isolation model](explanation/isolation-model.md) — workshops, the two-mount rule, the `/tmp` trap, and why commits land on the host branch with no extraction step.
- [Design](explanation/design.md) — why the library is the primary contract, the single side-effecting `Commander` seam, and the agent registry.

## Architecture decisions

The load-bearing decisions, one per file.

- [ADR 0001 — AgentProfile argv/stdin command contract](adr/0001-agentprofile-argv-stdin-command-contract.md)
- [ADR 0002 — structured output via generics and encoding/json](adr/0002-structured-output-generics-encoding-json.md)
- [ADR 0003 — session resume and fork command contract](adr/0003-session-resume-fork-command-contract.md)
- [ADR 0004 — multi-key credential env](adr/0004-multi-key-credential-env.md)
- [ADR 0005 — declarative agent registry](adr/0005-agent-registry-declarative-roster.md)
- [ADR 0006 — defer warm fan-out, one workshop per repo](adr/0006-defer-warm-fanout-single-repo-workshops.md)
- [ADR 0007 — nested worktree placement](adr/0007-nested-worktree-placement.md)
- [ADR 0008 — model format hint and fuzzy agent match](adr/0008-model-format-hint-and-fuzzy-agent-match.md)
- [ADR 0009 — derive the workshop from the project's definition](adr/0009-derive-workshop-from-project-definition.md)
- [Spike 0001 — warm workshops and fan-out](spikes/0001-warm-workshops-fanout.md)
