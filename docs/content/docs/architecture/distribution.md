---
title: Distribution
weight: 11
---

A release is one git tag. Pushing `v1.2.3` runs
[`.github/workflows/release.yml`](https://github.com/specsnl/labelsync/blob/main/.github/workflows/release.yml),
which runs goreleaser once; goreleaser builds every binary, creates the GitHub release, and commits
the updated cask to [`specsnl/homebrew-tap`](https://github.com/specsnl/homebrew-tap). Nothing else
is manual, and there is no version to bump anywhere in the tree —
see [Versioning]({{< ref "./versioning.md" >}}).

## What a release produces

Four archives, one per platform, plus `checksums.txt`:

| Archive                         | Contents                            |
|---------------------------------|-------------------------------------|
| `labelsync_darwin_amd64.tar.gz` | `labelsync`, `LICENSE`, `README.md` |
| `labelsync_darwin_arm64.tar.gz` | as above                            |
| `labelsync_linux_amd64.tar.gz`  | as above                            |
| `labelsync_linux_arm64.tar.gz`  | as above                            |

Every binary is `CGO_ENABLED=0` and `-tags=netgo`, so it is statically linked and depends on nothing
on the target machine. `-trimpath` and `-s -w` keep build paths out of it and the symbol table small.

## The three channels

| Channel  | Command                                          | Version it reports    |
|----------|--------------------------------------------------|-----------------------|
| Homebrew | `brew install specsnl/tap/labelsync`             | the tag, e.g. `1.2.3` |
| Binaries | download from the releases page                  | the tag               |
| Go       | `go install github.com/specsnl/labelsync@latest` | `dev`                 |

`go install` compiles from source with no `-ldflags`, so its binaries report `dev` — that is the
documented fallback, not a broken build. The two channels that ship a *release artifact* are the two
that carry a real version. Someone who needs `labelsync version` to mean something should use
Homebrew or the tarball.

`@latest` resolves to the highest release tag, and falls back to the highest *pre-release* tag only
while no release tag exists. During a `v0.1.0-rc.N` series `@latest` therefore gets the rc; once
`v0.1.0` lands, it never returns an rc again.

## The Homebrew cask

It is a **cask**, not a formula: the tap ships the prebuilt binary rather than building from source
on the user's machine. That is what makes the install a download instead of a Go toolchain
requirement, and it is the same shape as `specs-cli`'s entry in the tap.

Casks of unsigned binaries downloaded over HTTP carry macOS's quarantine attribute, which turns the
first run into a Gatekeeper refusal. The cask therefore ships a `postflight` hook:

```ruby
system_command "/usr/bin/xattr", args: ["-dr", "com.apple.quarantine", "#{staged_path}/labelsync"]
```

Without it, `brew install` succeeds and the very next command fails — the worst possible split,
because the failure surfaces nowhere near its cause.

Writing to another repository needs a token that `secrets.GITHUB_TOKEN` cannot be: that one is
scoped to this repository alone. `HOMEBREW_TAP_GITHUB_TOKEN` is an organisation secret with write
access to the tap. It is read from the environment by the cask's `repository.token` template, which
means an absent secret is not caught by `goreleaser check` or by a snapshot build — only by a real
release, at the last step, after the GitHub release has already been created.

## Verifying it without publishing

`task release:dry-run` runs `goreleaser release --snapshot --clean`, which does everything except
publish and writes it all to `dist/`. Read the rendered cask at `dist/homebrew/Casks/labelsync.rb`
and run the binary for your own platform out of `dist/`; a snapshot reports
`0.0.0-SNAPSHOT-<sha>`, which is enough to prove the `-X` injection landed.

To see the version string a *tag* would actually produce, tag a scratch clone and skip only the
publishing:

```sh
task dc:run:goreleaser SUB_CMD="release --clean --skip=publish,validate,announce"
```

Two things the local run cannot tell you, both because they only exist at publish time: whether
`HOMEBREW_TAP_GITHUB_TOKEN` is present, and whether the tap accepts the commit.

### Snapshot versions inside a git worktree

goreleaser reads the git state from the directory it is given, and the container is only given the
project directory. In a linked worktree, `.git` is a *file* pointing at the main checkout, which is
outside the mount — so goreleaser reports `not a git repository`, accepts it because snapshots are
allowed to, and versions the build `0.0.0-SNAPSHOT-none`. The build itself is unaffected. A snapshot
whose version is `0.0.0` is a statement about where it ran, not about the release config.

## The toolchain is pinned by `go.mod`, once

The goreleaser image ships its own Go and sets `GOTOOLCHAIN=local`, which makes the image's Go
version a second, invisible pin that has to agree with the `go` directive in `go.mod` — and when it
does not, the release fails at `loading go mod information`, before it builds anything. `compose.yml`
sets `GOTOOLCHAIN=auto` so `go.mod` stays the only pin, matching the workflow, which resolves its
toolchain from `go-version-file: go.mod`.

---

The design record this grew out of — including why a `gh` CLI extension was considered and rejected
— is
[design.md § Distribution](https://github.com/specsnl/labelsync/blob/main/docs/design.md#distribution).
