---
title: Distribution
weight: 12
---

A release is one git tag. Pushing `v1.2.3` runs
[`.github/workflows/release.yml`](https://github.com/specsnl/labelsync/blob/main/.github/workflows/release.yml),
which has two independent jobs: `release` runs goreleaser once — building every binary, creating the
GitHub release, and committing the cask *for that tag's channel* to
[`specsnl/homebrew-tap`](https://github.com/specsnl/homebrew-tap) — while `images` builds the two
container images and pushes them to GHCR. Neither needs anything from the other, so they run side by
side. Nothing else is manual, and there is no version to bump anywhere in the tree —
see [Versioning]({{< ref "./versioning.md" >}}).

The workflow's own `permissions:` block is `contents: read`, and each job asks for the one write
scope it needs: `contents: write` for the release, `packages: write` for the push to GHCR. Neither
job holds the other's token.

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

And two container images, each a manifest list over `linux/amd64` and `linux/arm64`:

| Package                            | Base               | Why it exists                                                     |
|------------------------------------|--------------------|-------------------------------------------------------------------|
| `ghcr.io/specsnl/labelsync`        | `scratch`          | The default. Binary, CA bundle, `/etc/passwd`, nothing else       |
| `ghcr.io/specsnl/labelsync/debian` | `debian:13.6-slim` | Has a shell, so it can be a base image or a multi-command CI step |

## The four channels

| Channel   | Command                                          | Version it reports    |
|-----------|--------------------------------------------------|-----------------------|
| Homebrew  | `brew install specsnl/tap/labelsync`             | the tag, e.g. `1.2.3` |
| Binaries  | download from the releases page                  | the tag               |
| Container | `docker run ghcr.io/specsnl/labelsync:1.2.3`     | the tag               |
| Go        | `go install github.com/specsnl/labelsync@latest` | `dev`                 |

Homebrew has a fourth, opt-in entry point: `brew install specsnl/tap/labelsync@rc` — see
[Stable and rc are two casks](#stable-and-rc-are-two-casks).

`go install` compiles from source with no `-ldflags`, so its binaries report `dev` — that is the
documented fallback, not a broken build. It is the only channel that does: the three that ship a
*release artifact* all carry a real version. Someone who needs `labelsync version` to mean something
should use any of the other three.

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

### Stable and rc are two casks

A tap file carries one version, so a single `Casks/labelsync.rb` tracks the most recent tag rather
than the most recent *stable* one — and `brew upgrade` would move everyone onto a release candidate
the moment one is tagged, without anyone opting in. The GitHub release is flagged pre-release
correctly throughout; Homebrew never consults that, only the cask.

The cask `name` therefore templates on `.Prerelease`, which holds `rc.1` for `v0.1.0-rc.1` and is
empty for `v0.1.0`. Each tag writes its own file, and neither channel can overwrite the other:

| Tag           | Cask file               | Token          | Install                                 |
|---------------|-------------------------|----------------|-----------------------------------------|
| `v0.9.9`      | `Casks/labelsync.rb`    | `labelsync`    | `brew install specsnl/tap/labelsync`    |
| `v0.9.9-rc.1` | `Casks/labelsync@rc.rb` | `labelsync@rc` | `brew install specsnl/tap/labelsync@rc` |

`binaries: [labelsync]` on the same entry is load-bearing. goreleaser defaults `binaries` to the
cask *name*, so templating the name alone emits `binary "labelsync@rc"` while the archive contains
`labelsync` — a cask that installs nothing, and one that contradicts its own quarantine hook, which
is hardcoded to `#{staged_path}/labelsync`. Nothing goes red until someone tries to install an rc,
which is why `release_test.go` asserts both the rendered names and the `binaries` list.

**The two casks cannot coexist.** Both link a command named `labelsync`, so installing
`labelsync@rc` replaces a stable `labelsync` rather than sitting beside it — the second install
fails at link time. Homebrew's answer is `conflicts_with`, but goreleaser's `conflicts[].cask` is
not templateable, so one templated entry cannot name the right counterpart on each side; giving the
rc its own command name would need `binary "labelsync", target: "labelsyncrc"`, which goreleaser
cannot emit at all. Going back to stable is `brew uninstall labelsync@rc && brew install labelsync`.

**The rc cask goes stale and stays.** After a stable ships, `Casks/labelsync@rc.rb` still points at
the last rc until the next one overwrites it. That is what a channel means — but it also means
installing it long after a series has ended gets something old. Pruning it is manual.

Writing to another repository needs a token that `secrets.GITHUB_TOKEN` cannot be: that one is
scoped to this repository alone. `HOMEBREW_TAP_GITHUB_TOKEN` is an organisation secret with write
access to the tap. It is read from the environment by the cask's `repository.token` template, which
means an absent secret is not caught by `goreleaser check` or by a snapshot build — only by a real
release, at the last step, after the GitHub release has already been created.

## The container images

Both images come out of the same
[`Dockerfile`](https://github.com/specsnl/labelsync/blob/main/Dockerfile) the local build uses: the
`images` job runs `docker/build-push-action` once per runtime stage, selecting it with `target:`.
`binary` is the `scratch` stage and publishes `ghcr.io/specsnl/labelsync`; `debian` publishes
`ghcr.io/specsnl/labelsync/debian`.

Three things about those stages are load-bearing, and none of them is visible in a `version` smoke
test:

- **The CA bundle, copied into `/etc/ssl/certs/` — with the trailing slash.** Without it the file
  lands as a *file* named `/etc/ssl/certs`, and Go, which looks in six fixed paths and not that one,
  fails every request with `failed to load system roots`. `debian:13.6-slim` ships no bundle at all,
  so its stage needs the same copy. The bundle is copied from the builder rather than `apt-get`
  installed because certificates are architecture-neutral, and installing one would put a
  foreign-architecture `apt-get` in the arm64 build.
- **`USER 65534:65534`** — nobody:nogroup, which both bases carry. The scratch stage copies
  `/etc/passwd` so that uid has a name. labelsync reads a config file and writes only its
  best-effort XDG cache, so root buys it nothing and costs a bind-mounted config its ownership.
- **`ENTRYPOINT`, not `CMD`.** `docker run …/labelsync:1 sync --dry-run` has to pass arguments to the
  binary; with a `CMD` it would try to execute `sync` instead. Anything that wants the shell the
  debian image exists for overrides the entrypoint — `--entrypoint`, or a container action's
  `entrypoint:`.

### Tags

| Git tag       | `ghcr.io/specsnl/labelsync`   | `…/labelsync/debian`          |
|---------------|-------------------------------|-------------------------------|
| `v1.2.3`      | `1.2.3`, `1.2`, `1`, `latest` | `1.2.3`, `1.2`, `1`, `latest` |
| `v1.3.0-rc.1` | `1.3.0-rc.1`                  | `1.3.0-rc.1`                  |
| `v0.4.0`      | `0.4.0`, `0.4`, `latest`      | `0.4.0`, `0.4`, `latest`      |

`docker/metadata-action` owns that policy declaratively, which is what keeps the pre-release
behaviour and the moving tags as config rather than as hand-written conditionals over `github.ref`:

```yaml
tags: |
  type=semver,pattern={{version}}
  type=semver,pattern={{major}}.{{minor}}
  type=semver,pattern={{major}},enable=${{ !startsWith(github.ref, 'refs/tags/v0.') }}
flavor: latest=auto
```

- **No `v` prefix.** `labelsync version` prints the tag without one — that is what goreleaser's
  `{{ .Version }}` renders, and what `metadata-action`'s `version` output is — so a `:v1.2.3` tag
  would disagree with the version the image reports. The images take that same value as the
  `LABELSYNC_VERSION` build arg, which is how both build paths agree.
- **`1.2.3` is immutable; `1.2`, `1` and `latest` move.** All four come out of the *same* build in the
  same push, so every tag of one release resolves to one manifest digest by construction —
  `docker buildx imagetools inspect` reports the same digest for each. Tagging `v1.2.4` re-points
  `:1.2` and `:1` at the new manifest.
- **No `:0` while labelsync is pre-1.0.** Semver allows a `0.x` bump to break, so a `:0` tag would
  promise stability across exactly the releases most likely to break. The `enable=` guard drops at
  1.0.0; until then `:0.4` is the narrowest honest moving tag, and it still moves on every patch.
- **A pre-release moves nothing.** `metadata-action` emits neither `{{major}}` nor
  `{{major}}.{{minor}}` for a prerelease semver tag, and `latest=auto` moves `latest` only for a
  non-prerelease — so an `-rc.N` tag publishes its own version tag and touches nothing else. The
  window between an rc and its stable release is exactly when `docker run ghcr.io/specsnl/labelsync`
  must not hand a stranger a release candidate. It is the same problem the tap has, and the reason
  that one ships [two casks](#stable-and-rc-are-two-casks) — but the cheaper answer to it: a tag
  pattern, rather than a second package per channel.

`org.opencontainers.image.source` is not decoration: without it GHCR does not link the package to the
repository, and an unlinked package inherits neither its visibility nor its permissions.
`metadata-action` emits it alongside `.version`, `.revision`, `.licenses` and `.description`, and
they are passed on as both labels and manifest annotations.

### Two platforms, no emulation

`platforms: linux/amd64,linux/arm64` is one build per image, and neither leg runs a foreign binary:

- the builder stage is `FROM --platform=$BUILDPLATFORM`, so `apt-get` and `go build` always run
  natively;
- the compile takes `GOOS=${GOOS:-$TARGETOS}` and `GOARCH=${GOARCH:-$TARGETARCH}`, so Go
  cross-compiles to the platform being built for, while an explicit arg still wins — which is how
  `task build` asks for the host's own platform;
- both runtime stages only `COPY`.

That is why the job needs no `setup-qemu-action` at all. Letting buildx pick the target platform for
the *builder* instead would run the whole `apt-get` and `go build` under QEMU for the arm64 leg.

`provenance: false` keeps the manifest list to the two platforms it claims; buildx otherwise attaches
provenance as extra `unknown/unknown` manifests, which several registries render as phantom
platforms.

### Two build paths for one binary

The images compile from source a second time rather than unpacking goreleaser's tarball, so the two
paths can silently disagree on build flags. `TestRelease_DockerfileBuildFlagsMatchGoreleaser` reads
both files and compares them: the `flags`, `CGO_ENABLED=0`, the `-s -w`, and the `-X` symbol the
version is injected into — including that the Dockerfile's `GO_MODULE` arg defaults to the module
goreleaser names.

goreleaser's `dockers_v2` would have removed the second compile entirely, and was not chosen for
three reasons: it has no `target` field, so one `Dockerfile` cannot yield two images without an
`ARG BASE` indirection or a second `Dockerfile.release`; the tag policy would become hand-written Go
templates with manual `{{ if not .Prerelease }}` conditionals where `metadata-action` has tested
patterns; and nothing about it is verifiable locally while the `goreleaser` service has no docker
socket. If the identical-bits property ever matters more than those three, that is the way back.

### The two manual steps

GHCR creates a package private on its first push, so each of the two needs its visibility flipped
once, by hand, before anyone can pull it. A nested name (`labelsync/debian`) is legal for an
organisation package and gets its own package page — the alternative, one package with a
`1.2.3-debian` tag suffix, halves that bookkeeping but makes the variants share a tag namespace and
hides the digest-per-variant distinction in the package list.

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

That is also the only way to see which cask a channel produces: tag the scratch clone `v0.9.9-rc.1`
and the rendered file is `dist/homebrew/Casks/labelsync@rc.rb`; tag it `v0.9.9` and it is
`labelsync.rb`. A snapshot has no pre-release component, so it always renders the stable name.

Two things the local run cannot tell you, both because they only exist at publish time: whether
`HOMEBREW_TAP_GITHUB_TOKEN` is present, and whether the tap accepts the commit.

The images are not part of that: they do not go through goreleaser, and the `goreleaser` service has
no docker socket to build them with. `task images` is their local equivalent — it builds both stages,
tags them `:dev`, loads them into the local docker, and runs `version` out of each:

```sh
task images
docker run --rm -e GH_TOKEN -v "$PWD/labels.yml:/labels.yml:ro" \
  ghcr.io/specsnl/labelsync:dev sync --dry-run --config /labels.yml
```

That second command is the check the `version` smoke test cannot make: it is the only one that proves
the CA bundle landed where Go looks for it.

Host platform only, because `--load` writes into the docker image store, which holds one platform per
tag. The multi-platform manifest list is the one part of the published result that only a real release
produces — `docker buildx build --platform linux/amd64,linux/arm64 --output type=cacheonly` proves
both legs *compile*, which is the part that can break.

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

## The documentation site

This site is the other thing the repository publishes, and it ships on its own schedule: not on a
tag, but on every push to `main` that touches `docs/**` or the workflow itself.
[`.github/workflows/docs.yml`](https://github.com/specsnl/labelsync/blob/main/.github/workflows/docs.yml)
runs `hugo --minify` in `docs/` and hands `docs/public/` to `actions/deploy-pages`. There is no
`gh-pages` branch: Pages is configured with **GitHub Actions** as its source, so the artifact the
workflow uploads *is* the deployment.

Three details are load-bearing:

- **`fetch-depth: 0`.** `hugo.toml` sets `enableGitInfo`, which dates each page from the last commit
  that touched it. A shallow clone has no such commit for most files, and the dates silently
  collapse onto the checkout.
- **`concurrency: { group: pages, cancel-in-progress: false }`.** Pages allows one deployment at a
  time. Cancelling in progress would abort a deploy midway and leave the live site on whatever the
  half-uploaded artifact contained, so runs queue instead.
- **The Hugo version is pinned to the `hugomods/hugo` tag in `compose.yml`.** Hextra is consumed as
  a Hugo module and tracks Hugo's template API; a floating `latest` in CI means the site that
  builds locally is not the site that builds on `main`. One version, two places, kept in step by
  hand.

The theme itself is cached across runs by `docs/go.sum`, which is why the module download does not
show up in the build time of a typical docs change.

### The custom domain

`labelsync.specs.dev` is set in Settings → Pages rather than in a `CNAME` file under
`docs/static/` — the same arrangement as `cli.specs.dev`. Keeping it in settings means a local
`hugo` build never emits a `CNAME` that could disagree with what the repository is actually
configured to serve.

DNS is a `CNAME` at `labelsync.specs.dev` pointing at **`specsnl.github.io`** — the *account*, not
the repository. A record pointing at `labelsync.github.io` resolves anyway, because `*.github.io`
is a wildcard onto the same Pages edge addresses and the edge routes on the `Host` header, but
GitHub's Pages DNS check flags it and it can block certificate issuance. Domain ownership is
already proven by the `_github-pages-challenge-specsnl` TXT record on the `specs.dev` apex, which
covers subdomains, so no per-repository verification step is needed.

---

The design record this grew out of — including why a `gh` CLI extension was considered and rejected
— is
[design.md § Distribution](https://github.com/specsnl/labelsync/blob/main/docs/design.md#distribution).
