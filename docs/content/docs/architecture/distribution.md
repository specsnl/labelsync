---
title: Distribution
weight: 12
---

A release is one git tag. Pushing `v1.2.3` runs
[`.github/workflows/release.yml`](https://github.com/specsnl/labelsync/blob/main/.github/workflows/release.yml),
which has two independent halves: `release` runs goreleaser once — building every binary, creating
the GitHub release, and committing the cask *for that tag's channel* to
[`specsnl/homebrew-tap`](https://github.com/specsnl/homebrew-tap) — while four `image*` jobs call the
organisation's shared image pipeline and push the container images to GHCR. Neither half needs
anything from the other, so they run side by side. Nothing else is manual, and there is no version to
bump anywhere in the tree — see [Versioning]({{< ref "./versioning.md" >}}).

The workflow's own `permissions:` block is `contents: read`, and each job asks for the one write
scope it needs: `contents: write` for the release, `packages: write` for the push to GHCR. Neither
half holds the other's token, and a reusable workflow inherits nothing it is not granted, so the
`packages: write` sits on the calling job rather than at the top of the file.

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

And two container images — one package, two variants — each a manifest list over `linux/amd64` and
`linux/arm64`:

| Reference                                | Base               | Why it exists                                                     |
|------------------------------------------|--------------------|-------------------------------------------------------------------|
| `ghcr.io/specsnl/labelsync:1.2.3`        | `scratch`          | The default. Binary, CA bundle, `/etc/passwd`, nothing else       |
| `ghcr.io/specsnl/labelsync:1.2.3-debian` | `debian:13.6-slim` | Has a shell, so it can be a base image or a multi-command CI step |

## The four channels

| Channel   | Command                                          | Version it reports    |
|-----------|--------------------------------------------------|-----------------------|
| Homebrew  | `brew install specsnl/tap/labelsync`             | the tag, e.g. `1.2.3` |
| Binaries  | download from the releases page                  | the tag               |
| Container | `docker run ghcr.io/specsnl/labelsync:1.2.3`     | the tag               |
| Go        | `go install github.com/specsnl/labelsync@latest` | `dev`                 |

Homebrew has a fourth, opt-in entry point: `brew install specsnl/tap/labelsync@rc`, which tracks
the most recent tag whether it is stable or not — see
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

There are therefore two static `homebrew_casks` entries, one per token, and which tags each one
publishes on is `skip_upload` and nothing else:

| Tag           | `Casks/labelsync.rb` | `Casks/labelsync@rc.rb` |
|---------------|----------------------|-------------------------|
| `v0.9.9-rc.1` | untouched            | `0.9.9-rc.1`            |
| `v0.9.9`      | `0.9.9`              | `0.9.9`                 |

`skip_upload: auto` on the stable entry skips it for any tag carrying a semver pre-release
component, which is exactly the stable/rc split. It is evaluated per entry, so the rc entry — which
sets no `skip_upload` at all — publishes unconditionally. **`@rc` means the leading edge, not the rc
series**: a stable tag writes both files at the same version, and because Homebrew's `Version`
comparison ranks an `rc` token below a bare version, `brew upgrade` moves someone on `@rc` from a
candidate onto the stable that supersedes it rather than seeing nothing newer for the whole gap
between series. Any pre-release token lands there, not just `rc.N` — a `-beta.1` tag publishes to
`@rc` too, which under two static entries is the design rather than an accident of a template.

The shared body — `repository`, `homepage`, `description`, `directory`, `hooks` — is a YAML anchor on
the rc entry that the stable entry merges, so the two cannot drift; the entry order is what the
anchor needs, since goreleaser rejects an unknown top-level key to hang one on. Stable overrides only
`name` and adds `skip_upload`.

`binaries: [labelsync]` on both entries is load-bearing. goreleaser defaults `binaries` to the cask
*name*, so the rc entry would otherwise emit `binary "labelsync@rc"` while the archive contains
`labelsync` — a cask that installs nothing, and one that contradicts its own quarantine hook, which
is hardcoded to `#{staged_path}/labelsync`. Nothing goes red until someone tries to install an rc,
which is why `release_test.go` reads both entries back and asserts the tap coordinates, the
`binaries` list, the quarantine hook, and the `skip_upload` split — the last being the whole channel
policy, and invisible until a real tag.

**The two casks cannot coexist.** Both link a command named `labelsync`, so installing
`labelsync@rc` replaces a stable `labelsync` rather than sitting beside it — the second install
fails at link time. Homebrew's answer is `conflicts_with`, but goreleaser's `conflicts[].cask` is
not templateable, so neither entry can name its counterpart; giving the rc its own command name
would need `binary "labelsync", target: "labelsyncrc"`, which goreleaser cannot emit at all. Going
back to stable is `brew uninstall labelsync@rc && brew install labelsync` — less pressing than it
was, since staying on `@rc` no longer means missing stable releases.

Writing to another repository needs a token that `secrets.GITHUB_TOKEN` cannot be: that one is
scoped to this repository alone. `HOMEBREW_TAP_GITHUB_TOKEN` is an organisation secret with write
access to the tap. It is read from the environment by the cask's `repository.token` template, which
means an absent secret is not caught by `goreleaser check` or by a snapshot build — only by a real
release, at the last step, after the GitHub release has already been created.

## The container images

Both images come out of the same
[`Dockerfile`](https://github.com/specsnl/labelsync/blob/main/Dockerfile) the local build uses, and
both are published by the organisation's shared pipeline in
[`specsnl/github-actions`](https://github.com/specsnl/github-actions) — `build-go-cli.yml` per
platform, then `merge-go-cli.yml` to merge the digests into a manifest list. The whole of it is four
jobs of configuration:

```yaml
  image:
    strategy:
      matrix:
        runner:
          - { os: ubuntu-24.04, platform: linux/amd64 }
          - { os: ubuntu-24.04-arm, platform: linux/arm64 }
    uses: specsnl/github-actions/.github/workflows/build-go-cli.yml@2.4.3
    with:
      image-name: ghcr.io/specsnl/labelsync
      target: binary
      version-build-arg: LABELSYNC_VERSION
```

plus the same again for `target: debian`, and a manifest job per target — the debian one carrying
`variant: debian`. Login, buildx, the digest export, the tag policy, the OCI labels and
`provenance: false` all live in the shared workflow, along with a layer cache in the Actions cache
keyed per Dockerfile, platform and target.

What stays here is what only this repository can know: **which stages to publish**, **what the image
is called**, and **what the version build arg is named**. Everything else the pipeline works out from
the tag. There is no `version:` input either — the shared workflow defaults it to the tag without its
leading `v`, which is exactly what goreleaser injects, so the image and the tarball cut from one tag
cannot disagree about what they are.

Three things about the runtime stages are load-bearing, and none of them is visible in a `version`
smoke test:

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

| Git tag       | Scratch                                 | Debian                                                              |
|---------------|-----------------------------------------|---------------------------------------------------------------------|
| `v1.2.3`      | `1.2.3`, `1.2`, `1`, `v1.2.3`, `latest` | `1.2.3-debian`, `1.2-debian`, `1-debian`, `v1.2.3-debian`, `debian` |
| `v1.3.0-rc.1` | `1.3.0-rc.1`, `v1.3.0-rc.1`             | `1.3.0-rc.1-debian`, `v1.3.0-rc.1-debian`                           |
| `v0.4.0`      | `0.4.0`, `0.4`, `v0.4.0`, `latest`      | `0.4.0-debian`, `0.4-debian`, `v0.4.0-debian`, `debian`             |

**The debian image is a tag suffix, not a package of its own.** That is how `node`, `python` and
`postgres` publish their base-image variants, and it is what the organisation's shared pipeline is
built around: `variant: debian` on the merge job turns into a `-debian` suffix on every version tag
plus a bare `:debian`. The variants share a tag namespace and each still has its own digest. It cost
one thing to adopt — `ghcr.io/specsnl/labelsync/debian`, the nested package the first four release
candidates pushed to, is frozen at `0.1.0-rc.4` and receives nothing further.

`docker/metadata-action`, inside the shared workflow, owns the policy declaratively, which is what
keeps the pre-release behaviour and the moving tags as config rather than as hand-written
conditionals over `github.ref`:

- **The version tag has no `v`.** `labelsync version` prints the tag without one — that is what
  goreleaser's `{{ .Version }}` renders, and what `metadata-action`'s `version` output is. `v1.2.3`
  is published too, as an alias onto the same digest, because the shared pipeline also emits
  `type=ref,event=tag`; nothing in labelsync reads it, and `:1.2.3` stays the form the documentation
  and the CI recipe pin.
- **`1.2.3` is immutable; `1.2`, `1` and `latest` move.** They all come out of the *same* digests in
  the same merge, so every tag of one release resolves to one manifest by construction —
  `docker buildx imagetools inspect` reports the same digest for each. Tagging `v1.2.4` re-points
  `:1.2` and `:1` at the new manifest.
- **No `:0` while labelsync is pre-1.0.** Semver allows a `0.x` bump to break, so a `:0` tag would
  promise stability across exactly the releases most likely to break. The shared workflow guards the
  bare `{{major}}` on `!startsWith(github.ref, 'refs/tags/v0.')`, so the guard drops by itself at
  1.0.0; until then `:0.4` is the narrowest honest moving tag, and it still moves on every patch.
- **A pre-release moves nothing.** `metadata-action` collapses `{{major}}` and `{{major}}.{{minor}}`
  onto the full version for a prerelease semver tag, and the shared workflow sets `latest=false` and
  re-adds `:latest` itself, guarded on a tag with no `-` in it — so an `-rc.N` tag publishes its own
  version tag and touches nothing else. The window between an rc and its stable release is exactly
  when `docker run ghcr.io/specsnl/labelsync` must not hand a stranger a release candidate. It is the
  same problem the tap has, and the reason that one ships
  [two casks](#stable-and-rc-are-two-casks) — but the cheaper answer to it: a tag pattern, rather
  than a second package per channel.

`org.opencontainers.image.source` is not decoration: without it GHCR does not link the package to the
repository, and an unlinked package inherits neither its visibility nor its permissions.
`metadata-action` emits it alongside `.version`, `.revision`, `.licenses` and `.description`, and the
shared pipeline passes them on as both labels and manifest annotations.

### One runner per platform

Each platform is built on a runner of its own architecture — `linux/amd64` on `ubuntu-24.04`,
`linux/arm64` on `ubuntu-24.04-arm` — and the two digests are merged into a manifest list afterwards.
Nothing is emulated, and nothing has to be: `setup-qemu-action` appears nowhere in the pipeline.

The Dockerfile could have supplied both legs from one runner, and still can:

- the builder stage is `FROM --platform=$BUILDPLATFORM`, so `apt-get` and `go build` always run
  natively;
- the compile takes `GOOS=${GOOS:-$TARGETOS}` and `GOARCH=${GOARCH:-$TARGETARCH}`, so Go
  cross-compiles to the platform being built for, while an explicit arg still wins — which is how
  `task build` asks for the host's own platform;
- both runtime stages only `COPY`.

That property is what makes `docker buildx build --platform linux/arm64` on an amd64 laptop finish in
seconds rather than crawling through QEMU, and `TestRelease_ImagesCrossCompileRatherThanEmulate`
keeps it. The release no longer depends on it, but the
[pull-request guard](#the-pull-request-guard) needs the native runners for a different reason
entirely: it *runs* what it builds.

`provenance: false`, which the shared `build-image` action sets, keeps the manifest list to the two
platforms it claims; buildx otherwise attaches provenance as extra `unknown/unknown` manifests, which
several registries render as phantom platforms.

### The pull-request guard

A broken Dockerfile should fail on the pull request, not while a tag is being cut. The `image` job in
[`ci.yml`](https://github.com/specsnl/labelsync/blob/main/.github/workflows/ci.yml) builds both
stages on both architectures — four jobs — and runs
[`test/image.bats`](https://github.com/specsnl/labelsync/blob/main/test/image.bats) against each
result.

It calls the shared `build-image` action directly rather than `build-go-cli.yml`, because the image
has to be built and run in the same job: a reusable workflow would load it into a daemon the caller
cannot reach. `load: true` builds one platform into the runner's own daemon and reports the reference
to run — which is also why each leg needs a runner of its own architecture. A build that only
compiles the foreign architecture proves nothing about the image it produced.

What the script asserts is the set of things a release would otherwise be the first to find out:

- **the version the binary reports.** The image is built with `LABELSYNC_VERSION=ci-<sha>`, a string
  no fallback can produce, and the binary has to print it back. This is the one input that fails
  *silently*: rename the build arg on either side and buildx warns about an unused arg, the build
  succeeds, and the image reports `dev`.
- **that it runs as `65534`,** read off `Config.User` rather than from `id`, which the scratch image
  has no shell to run.
- **that the CA bundle is at `/etc/ssl/certs/ca-certificates.crt`,** copied out of a created
  container — again, no shell — so the missing trailing slash is caught here rather than by the first
  API call someone makes.
- **that a bind-mounted config is readable and parses.** A valid one fails on the *token*, which is
  how the script knows the YAML was read; an invalid one comes back with
  `"error_kind":"invalid_color"`.
- **the shell in the debian image, and `/etc/passwd` in the scratch one** — the two properties that
  belong to one stage each.

The same script runs locally: `task image:smoke` builds both images and drives bats through a
container that reaches the host daemon over a socket proxy.

The four `Image (...)` checks are not in the branch ruleset's required checks, so they report but do
not gate.

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

### The manual step

GHCR creates a package private on its first push, so its visibility has to be flipped once, by hand,
before anyone can pull it. That is one package now rather than two, which is the bookkeeping the
`-debian` suffix buys: `ghcr.io/specsnl/labelsync` is already public, and the variant lands inside
it.

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

Both cask files are rendered either way — `skip_upload` gates the publishing pipe, not the
rendering, so `dist/homebrew/Casks/` holds `labelsync.rb` and `labelsync@rc.rb` even for a
pre-release tag. What a local run proves about the tap is that each file names the right version and
the right `binary`; **which of the two a tag actually commits, nothing local shows** — only a real
tag's workflow log, where a stable tag lands two commits in the tap and a pre-release lands one.

Three things the local run cannot tell you, all because they only exist at publish time: which casks
are uploaded, whether `HOMEBREW_TAP_GITHUB_TOKEN` is present, and whether the tap accepts the commit.

The images are not part of that: they do not go through goreleaser, and the `goreleaser` service has
no docker socket to build them with. Two tasks cover them instead — `task image:build` loads both
stages into the local docker as `:dev` and `:dev-debian`, mirroring the published names, and
`task image:smoke` builds them and then runs the same `test/image.bats` the pull-request guard runs:

```sh
task image:smoke
```

The one check neither makes is the CA bundle *working*, as opposed to being present — that needs the
network:

```sh
docker run --rm -e GH_TOKEN -v "$PWD/labels.yml:/labels.yml:ro" \
  ghcr.io/specsnl/labelsync:dev sync --dry-run --config /labels.yml
```

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
