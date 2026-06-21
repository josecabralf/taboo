# Releasing taboo

taboo is a monorepo of three Go modules. The library is the **root** module; the
CLI and the afk demo live in subdirectories. There is **no `go.work`**:

| Module | Directory | Module path | Released? |
|---|---|---|---|
| Library | `.` (repo root) | `github.com/josecabralf/taboo` | yes |
| CLI | `cli/` | `github.com/josecabralf/taboo/cli` | yes |
| afk demo | `.taboo/orchestrator/` | `afk` | no (in-tree demo) |

## The root module takes a plain tag; subdir modules are prefixed

The library is the repository root module, so Go's module proxy serves it through
an **unprefixed** `vX.Y.Z` tag: `go get github.com/josecabralf/taboo@v0.1.0`
resolves the tag named `v0.1.0`.

A module that lives in a subdirectory is served **only** through a tag whose name
is the module's subdirectory path plus the semver, so the CLI is released through
a `cli/`-prefixed tag:

- `v0.1.0`: releases the library (repo root).
- `cli/v0.1.0`: releases the CLI.

A bare `v0.1.0` tag does **not** serve the CLI: `go get
github.com/josecabralf/taboo/cli@v0.1.0` resolves the tag named `cli/v0.1.0`, not
`v0.1.0`. (CI's tag trigger matches `v*` and `cli/v*` for the same reason — see
`.github/workflows/ci.yml`.)

The two modules version **independently**: a library change is `vX.Y.Z`, a
CLI-only change is `cli/vX.Y.Z`. The afk demo is not published and carries no
release tag.

## Release checklist (per module, before the first tag)

1. `workshop run -- make lint test build` is green (fans out across all three
   modules).
2. The module builds, vets and tests standalone from its own directory (the
   library from the repo root: `go build ./... && go vet ./... && go test ./...`;
   likewise from `cli`).
3. The library module pulls in only `gopkg.in/yaml.v3`. Confirm from the repo
   root with `go list -deps ./...` (no cobra/huh/charmbracelet packages).
4. Choose the right tag: an unprefixed `vX.Y.Z` for the library, or `cli/vX.Y.Z`
   for the CLI.
5. Tag and push: `git tag v0.1.0 && git push origin v0.1.0` (library), or
   `git tag cli/v0.1.0 && git push origin cli/v0.1.0` (CLI).
6. The CLI's release build injects the library version into the `taboo init`
   scaffold via `-ldflags "-X github.com/josecabralf/taboo/cli/internal/app.libraryVersion=vX.Y.Z"`
   so generated `go.mod` files pin the matching `github.com/josecabralf/taboo`
   release.
7. For a cross-module release (library + CLI together), tag the library `vX.Y.Z`
   first so the CLI release can require the published library version.
