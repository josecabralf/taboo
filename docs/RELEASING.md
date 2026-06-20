# Releasing taboo

taboo is a monorepo of three Go modules with **no root `go.mod`** and **no
`go.work`**:

| Module | Directory | Module path | Released? |
|---|---|---|---|
| Library | `pkg/` | `github.com/josecabralf/taboo/pkg` | yes |
| CLI | `cli/` | `github.com/josecabralf/taboo/cli` | yes |
| afk demo | `.taboo/orchestrator/` | `afk` | no (in-tree demo) |

## Tags MUST be subdir-prefixed

A module that lives in a subdirectory of its repository is served by Go's module
proxy **only** through a tag whose name is the module's subdirectory path plus
the semver. So the tags are:

- `pkg/v0.1.0`: releases the library.
- `cli/v0.1.0`: releases the CLI.

An **unprefixed** `v0.1.0` tag does **not** serve a subdir module: `go get
github.com/josecabralf/taboo/pkg@v0.1.0` resolves the tag named `pkg/v0.1.0`, not
`v0.1.0`. Every release tag is subdir-prefixed, including the first. (CI's tag trigger matches
`pkg/v*` and `cli/v*` for the same reason — see `.github/workflows/ci.yml`.)

The two modules version **independently**: a library change is `pkg/vX.Y.Z`, a
CLI-only change is `cli/vX.Y.Z`. The afk demo is not published and carries no
release tag.

## Release checklist (per module, before the first tag)

1. `workshop run -- make lint test build` is green (fans out across all three
   modules).
2. The module builds, vets and tests standalone from its own directory
   (`cd pkg && go build ./... && go vet ./... && go test ./...`, likewise for
   `cli`).
3. The library module pulls in only `gopkg.in/yaml.v3`. Confirm with
   `cd pkg && go list -deps ./...` (no cobra/huh/charmbracelet packages).
4. Choose the subdir-prefixed tag (`pkg/vX.Y.Z` or `cli/vX.Y.Z`) — **never** an
   unprefixed `vX.Y.Z`.
5. Tag and push: `git tag pkg/vX.Y.Z && git push origin pkg/vX.Y.Z`.
6. The CLI's release build injects the library version into the `taboo init`
   scaffold via `-ldflags "-X github.com/josecabralf/taboo/cli/internal/app.libraryVersion=vX.Y.Z"`
   so generated `go.mod` files pin the matching `github.com/josecabralf/taboo/pkg`
   release.
7. For a cross-module release (library + CLI together), tag `pkg/vX.Y.Z` first so
   the CLI release can require the published library version.
