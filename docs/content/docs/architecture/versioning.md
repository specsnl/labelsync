---
title: Versioning
weight: 4
---

The version a binary reports is decided at **build** time, not at run time. There is no version
constant to bump and no file to keep in step with a tag: `internal/cmd.Version` is a variable the
linker overwrites, and every producer derives its value from git.

## The injected variable

```go
// internal/cmd/version.go
var Version = "dev"
```

Both build paths overwrite it by its full path:

```text
-X github.com/specsnl/labelsync/internal/cmd.Version=1.2.3
```

That path is a string in `.goreleaser.yml` and the `Dockerfile` which the compiler never checks.
Rename or move the variable and both keep injecting into nothing — every build would silently ship
as `dev`, with nothing failing. `version_test.go` therefore asserts both files still name it, and
referring to `cmd.Version` from the test makes a rename a compile error rather than a surprise in a
release.

`dev` is the fallback, and it is what a plain `go build` produces. It is not a placeholder anyone
has to remember to change.

## What each build produces

| Build                        | Version string            |
|------------------------------|---------------------------|
| A released binary            | `1.2.3` — the release tag |
| `task build`, on a tag       | `1.2.3`                   |
| `task build`, past a tag     | `1.2.3-31-g69fca8f`       |
| `task build`, uncommitted    | `1.2.3-31-g69fca8f-dev`   |
| `task build`, no tags yet    | `69fca8f` — the commit    |
| `go install ...@latest`      | `dev`                     |
| `go build` with no `ldflags` | `dev`                     |

### Releases

goreleaser injects `{{ .Version }}`, which is the release tag with the leading `v` already stripped.
Tagging `v1.2.3` ships a binary that reports `1.2.3`. See
[Distribution]({{< ref "./distribution.md" >}}) for the pipeline that tag drives, and for why a
`go install` build is one of the two that report `dev`.

### Local builds

`task build` derives the string in the `build` task:

```sh
described=$(git describe --tags --always --dirty="-dev" 2>/dev/null || echo "dev"); echo "${described#v}"
```

Three things in that line are load-bearing:

- **`--always`** falls back to the abbreviated commit when git has no tag to describe from. An
  untagged repository — which labelsync is until its first release — therefore builds as a bare SHA.
  That is what a SHA means: no tags yet, not a broken build.
- **`${described#v}`** strips the leading `v`, so a local build and a release spell the same version
  the same way. Without it, tag `v1.2.3` gives `v1.2.3` locally and `1.2.3` from a release, and the
  string no longer tells you only what it was built from. The `--always` fallback is a hex SHA,
  which cannot start with a `v`, so nothing else is at risk of losing a character.
- **The fallback is captured before the strip.** Piping git's output into the strip would replace
  git's exit status with the strip's, so `|| echo "dev"` would never fire and a tree git cannot
  describe at all would build with an *empty* version — a binary that reports nothing is worse than
  one that reports `dev`.

`--dirty="-dev"` marks a build made from a tree with uncommitted changes, so a binary that does not
correspond to any commit says so.

## Reaching the user

The value is a result, not narration, so it goes to stdout through
[`WriteResult`]({{< ref "./output.md#a-result-that-is-not-a-table" >}}) — see
[Overview § How the tree is wired]({{< ref "./overview.md#how-the-tree-is-wired" >}}) for why `--version` is a
hand-rolled flag rather than Cobra's built-in one.

```sh
labelsync version                    # labelsync version 1.2.3
labelsync version --dont-prettify    # 1.2.3
labelsync --version                  # 1.2.3   — identical to --dont-prettify
labelsync version --output=json      # {"version":"1.2.3"}
```

The `version` key is a public contract in the same way `error_kind` is: it may be added to, never
renamed.

---

How the binary reaches a machine in the first place is the design record's subject:
[design.md § Distribution](https://github.com/specsnl/labelsync/blob/main/docs/design.md#distribution).
