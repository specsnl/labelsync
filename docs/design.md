# Label Sync — Design Plan

> **Repo:** `specsnl/labelsync` · **module:** `github.com/specsnl/labelsync` · **binary:** `labelsync`
> Distributed as a standalone binary (goreleaser + Homebrew tap), **not** as a `gh` CLI extension —
> see [Distribution](#distribution).

A Go CLI that synchronises GitHub issue/PR labels across a configured set of repositories,
using a local YAML file as the source of truth.

Structure, conventions, and library choices deliberately mirror
[`specsnl/specs-cli`](https://github.com/specsnl/specs-cli).

---

## Goals

- Declare labels once in YAML (`name`, `color`, optional `description`) and converge any number
  of repositories onto that definition.
- Target repositories individually, by organisation, or by personal account — grouped into
  reusable, composable named selectors.
- Two reconciliation modes: **append** (additive, never deletes) and **prune** (repo matches
  config exactly).
- Treat configured labels as the **authoritative owners of their colours**: if an unconfigured
  label is squatting on a colour a configured label wants, recolour the squatter.
- Be safe and re-runnable: idempotent, convergent, dry-run first, non-destructive by default.
- Be a good CI citizen from day one: non-interactive flags, JSON output, meaningful exit codes,
  no prompts without a TTY.
- Respect GitHub rate limits proactively, and wait gracefully with a visible countdown rather
  than failing.

## Non-goals

- Managing **organisation default labels**. These exist (Org settings → Repository →
  Repository defaults → Repository labels) but there is **no REST endpoint** for them — the org
  surface has no `/orgs/{org}/labels`. They are UI-only, and they only affect *newly created*
  repositories. See [Relationship to org default labels](#relationship-to-org-default-labels).
- Managing issues, PRs, milestones, or label *assignments* on issues. This tool manages the
  repository-level label catalogue only.
- Full Bubbletea TUI. `huh` prompts + `lipgloss` styling are sufficient, matching specs-cli.

---

## Prior art

"Sync labels from a YAML file" is well-trodden ground. Surveying it is worth doing, because it
clarifies what is actually new here.

| Tool                                                                                                                         | Shape                                                                | Multi-repo                 | Config              |
|------------------------------------------------------------------------------------------------------------------------------|----------------------------------------------------------------------|----------------------------|---------------------|
| `gh label clone` *(built into gh)*                                                                                           | Copies one repo's labels to another                                  | one target                 | none                |
| [shanduur/labeler](https://github.com/shanduur/labeler) (Go)                                                                 | `upload` / `download` a YAML label list                              | no, `--repository` per run | YAML list           |
| [Financial-Times/github-label-sync](https://github.com/Financial-Times/github-label-sync) (Node)                             | The well-known one. Minimises destructive ops, does renames, dry-run | no                         | JSON                |
| [giantswarm/github-label-sync](https://github.com/giantswarm/github-label-sync)                                              | Source repo → many targets, rule-based                               | yes                        | rules file          |
| [tommy6073/takolabel](https://github.com/tommy6073/takolabel) (Go)                                                           | Create/delete across repos from YAML                                 | yes                        | YAML                |
| [EndBug/label-sync](https://github.com/EndBug/label-sync), [DrBarnabus/label-sync](https://github.com/DrBarnabus/label-sync) | GitHub Actions                                                       | no (running repo only)     | YAML or source repo |

Three things in this design have **no prior art found**:

1. **Colour ownership with perceptual reallocation.** Configured labels claim their colours;
   unconfigured squatters are recoloured to the maximally-distant free colour via CIEDE2000.
   Nothing surveyed attempts this.
2. **Named, composable repo groups with per-label targeting.** Every existing tool applies all
   labels to all targets. None allow label *X* to go only to group *Y*.
3. Organisation *and* personal-account enumeration with glob filters in one config, combined with
   ETag-cached dry-runs and a rate-limit countdown.

Note for naming: `labeler`, `label-sync`, and `github-label-sync` are all taken by the projects
above. `labelsync` was chosen with that adjacency understood and accepted.

---

## Concepts

### Reconciler

The core is a reconciler, not a script. For each target repository:

1. **Read** the current label set.
2. **Resolve** the desired label set for that specific repository (from groups).
3. **Plan** — compute an ordered list of actions to converge current → desired.
4. **Apply** — execute the actions (or print them, under `--dry-run`).

Steps 2–3 are pure functions with no network access, which is what makes the interesting logic
(group resolution, colour allocation, prune semantics) unit-testable without touching GitHub.

### Groups

A **group** is a named repository selector. Labels opt into one or more groups. A repository that
no group resolves to is **never touched** — this is the primary safety property.

### Modes

| Mode                 | Behaviour                                                                                                                    |
|----------------------|------------------------------------------------------------------------------------------------------------------------------|
| `append` *(default)* | Create missing configured labels, update existing configured labels, recolour displaced squatters. **Never deletes.**        |
| `prune`              | Everything `append` does, plus removal of unconfigured labels. **Report-first** — lists them and prompts for what to remove. |

`prune` is never implicit. It requires `--mode=prune`, and removal requires either an interactive
selection or `--prune=all`.

---

## Configuration

### Location

Resolution order for the config file:

1. `--config <path>` flag
2. `./labels.yml` (or `./labels.yaml`) in the working directory
3. `$XDG_CONFIG_HOME/labelsync/labels.yml` (default `~/.config/labelsync/labels.yml`)

Having both `labels.yml` and `labels.yaml` in the same directory is an error
(`ErrAmbiguousConfigFile`), matching specs-cli's handling of `project.yml`/`project.yaml`.

### Full example

```yaml
version: 1

# ── Repository selectors ──────────────────────────────────────────────
groups:
  specs-all:
    org: specsnl
    exclude: ["*-archive", "sandbox-*"]
    skip_archived: true          # default: true
    skip_forks: true             # default: true
    visibility: all              # all | public | private   (default: all)

  laravel:
    repos:
      - specsnl/example-website
      - specsnl/example-platform

  personal:
    user: Ilyes512
    include: ["boilr-*", "docker-php-*"]

  everything:
    include_groups: [specs-all, personal]

# ── Fallback groups for labels that don't specify any ─────────────────
defaults:
  groups: [specs-all]

# ── Renames, applied before matching (preserves issue/PR associations) ─
renames:
  - from: "bug"
    to: "type: bug"
  - from: "enhancement"
    to: "type: feature"

# ── Labels ────────────────────────────────────────────────────────────
labels:
  - name: "type: bug"
    color: "d73a4a"
    description: "Something isn't working"

  - name: "type: feature"
    color: "0e8a16"
    description: "New functionality"

  - name: "priority: high"
    color: "b60205"

  - name: "laravel"
    color: "ff2d20"
    description: "Laravel-specific"
    groups: [laravel, personal]
```

### `groups` reference

Each group has **exactly one source**: `org`, `user`, `repos`, or `include_groups`. Mixing
sources in a single group is an error — it makes filter semantics ambiguous.

| Field            | Type     | Applies to | Description                                                                                               |
|------------------|----------|------------|-----------------------------------------------------------------------------------------------------------|
| `org`            | string   | source     | Enumerate all repositories in an organisation                                                             |
| `user`           | string   | source     | Enumerate repositories owned by a user                                                                    |
| `repos`          | []string | source     | Explicit `owner/repo` list                                                                                |
| `include_groups` | []string | source     | Union of other groups (composition). Cycles are an error                                                  |
| `include`        | []string | org/user   | Glob allowlist on repo name. Empty = all                                                                  |
| `exclude`        | []string | org/user   | Glob denylist on repo name, applied after `include`                                                       |
| `skip_archived`  | bool     | org/user   | Default `true`. Archived repos reject writes with `403`, so filtering is strictly better than discovering |
| `skip_forks`     | bool     | org/user   | Default `true`                                                                                            |
| `visibility`     | string   | org/user   | `all` \| `public` \| `private`. Default `all`                                                             |

Globs use `github.com/danwakefield/fnmatch` — the same matcher specs-cli uses for
`.specsverbatim`, for consistency. Patterns match the **repo name only**, not `owner/repo`.

**`user` has two code paths.** When the value equals the authenticated user, enumeration uses
`GET /user/repos?affiliation=owner`, which includes private repositories. For any other user it
uses `GET /users/{user}/repos`, which returns public repos only. If `visibility` requests
`private` for a non-authenticated user, the tool warns that the result will be empty rather than
silently returning nothing.

### `labels` reference

| Field         | Type     | Required | Notes                                                                                             |
|---------------|----------|----------|---------------------------------------------------------------------------------------------------|
| `name`        | string   | yes      | 1–50 code points. Emoji permitted, but never *only* emoji. Colon-style markup (`:bug:`) permitted |
| `color`       | string   | yes      | 6-digit hex. `#` accepted on input and stripped. Normalised to lowercase                          |
| `description` | string   | no       | Max 100 code points. **Authoritative** — omitting it *clears* any existing description            |
| `groups`      | []string | no       | Group names. When absent, `defaults.groups` applies                                               |

**Descriptions are authoritative.** Omitting `description` sets the remote description to empty.
This makes the YAML the single source of truth, but it means a first run against existing repos
will wipe descriptions you did not transcribe. The `export` command exists precisely to avoid
this — see [CLI](#cli).

**Both length bounds are the API's own**, they count Unicode code points, and overflow is a `422`
rather than a silent truncation — confirmed against the live API, and recorded under
[Name and description lengths are counted in code points](#name-and-description-lengths-are-counted-in-code-points).
That is also where the emoji-only name rule and GitHub's whitespace trimming are pinned down.

### Resolution rule

Stated precisely, because prune semantics depend on it:

> For repository *R*, the **desired set** is every label whose resolved groups contain *R*.
> In `prune` mode, any label present in *R* but outside that set is **unconfigured**.
> If no group resolves to *R*, the tool never touches *R*.

---

## Validation

> **Landed.** Every rule below is implemented in `internal/config/validate.go` and run by
> `LoadFile`. What was built, and the decisions behind it, is described in
> [Architecture → Configuration](./content/docs/architecture/configuration.md#validation).

All validation happens at config load, before any network call, and fails fast with a wrapped
sentinel error.

| Rule                                                       | Sentinel                      |
|------------------------------------------------------------|-------------------------------|
| `version` present and supported                            | `ErrUnsupportedConfigVersion` |
| At least one label defined                                 | `ErrEmptyConfig`              |
| Label names unique, **compared case-insensitively**        | `ErrDuplicateLabelName`       |
| **Colours globally unique across all labels**              | `ErrDuplicateLabelColor`      |
| Colour is valid 6-digit hex                                | `ErrInvalidColor`             |
| Description ≤ 100 code points                              | `ErrDescriptionTooLong`       |
| Label name 1–50 code points, non-empty after trim          | `ErrInvalidLabelName`         |
| Label name is not emoji only                               | `ErrInvalidLabelName`         |
| Every referenced group exists                              | `ErrUnknownGroup`             |
| Every group has exactly one source                         | `ErrAmbiguousGroupSource`     |
| No cycle in `include_groups`                               | `ErrCyclicGroup`              |
| `repos` entries match `owner/repo`                         | `ErrInvalidRepoRef`           |
| Rename `to` targets a configured label; no chained renames | `ErrInvalidRename`            |
| Rename `from` is not itself a configured label name        | `ErrInvalidRename`            |

**Colour uniqueness is global**, not per-repository. Two labels in groups that never share a repo
could technically reuse a colour without conflict, but global uniqueness is far easier to reason
about and can be validated offline. This is a deliberate, slightly conservative choice.

**Case-insensitive name comparison** mirrors GitHub's own uniqueness rule: a repo cannot hold both
`Bug` and `bug`, confirmed against the live API in
[#16](https://github.com/specsnl/labelsync/issues/16) and recorded under
[Label names are case-insensitively unique](#label-names-are-case-insensitively-unique). Two config
entries differing only by case describe a state GitHub cannot represent, so rejecting them at load
turns an unavoidable apply-time `422` into a clear config error before any network call.

The same case-insensitive comparison applies to both `ErrInvalidRename` checks. In particular,
`from` not being a configured label name is compared case-insensitively, which means a case-only
rename such as `bug` → `Bug` is **rejected as a rename**. That is deliberate and not a limitation:
casing drift needs no rename entry, because step 5 of the algorithm already converges it.

---

## Reconciliation algorithm

Pure function, no I/O:

```go
// internal/plan
func Compute(repo string, desired []config.Label, current []plan.Label, mode Mode, renames []config.Rename) RepoPlan
```

**Partly landed** ([#26](https://github.com/specsnl/labelsync/issues/26)) — steps 2 to 5, append
mode. Renames ([#27](https://github.com/specsnl/labelsync/issues/27)) and prune
([#28](https://github.com/specsnl/labelsync/issues/28)) are still to come; both parameters are
already in the signature, so they land as a change to that function rather than to every call site.

The signature above is the one that was built, and it is not the one this section originally
sketched. That sketch read
`Compute(desired []config.Label, current []github.Label, mode Mode, renames []Rename) Plan`, and
predated the rule that `internal/plan` never imports `internal/github` — which the planner cannot
satisfy while taking a `github.Label`. Three consequences, each argued in
[Architecture § Planner](./content/docs/architecture/plan.md#the-signature-and-where-it-departs-from-the-design-sketch):
the current labels arrive as a `plan.Label`, `Compute` is told which repository it is reconciling
because both `Action` and `RepoPlan` carry one, and it returns that single repository's `RepoPlan`
for a run to assemble into a `Plan`.

### Ordering

Actions within a repository are emitted in this order, and the order matters:

1. **Renames** — first, so subsequent matching sees the new names and rename+recolour of the same
   label collapses into coherent steps.
2. **Recolour displaced squatters** — before the configured label claims the colour, so every
   intermediate state is clean if the run aborts mid-repository. (GitHub permits duplicate
   colours, so the API accepts either order; this is about crash-consistency, not validity.)
3. **Create** missing configured labels.
4. **Update** existing configured labels (colour and/or description).
5. **Delete** — last, and only in `prune` mode.

**Both existence checks in step 1 are case-insensitive**, and this is the one place a rename can
collide. GitHub rejects a `PATCH` whose `new_name` matches an existing label in any casing with the
same `422 already_exists` it uses for creates, so a rename to `Bug` fails when the repo already
holds `bug`. Skipping the rename when `to` already exists — compared case-insensitively — is what
keeps that unreachable. A case-only *drift* (step 5) can never collide this way, since the only
label its target can match is the label being renamed itself.

### Steps

```text
1. Apply renames
     for each rename {from, to}:
       if `from` exists remotely and `to` does not:   # both checks case-insensitive
         emit Update{Name: from, NewName: to}   # PATCH preserves issue/PR associations
         rewrite local view: from → to

2. Partition remote labels (case-insensitive name match against desired set)
     matched     → present in config
     unconfigured → not in config

3. Reserve colours
     reserved := { desired.color for every desired label }

4. Detect squatters
     squatters := { u in unconfigured : u.color ∈ reserved }
     for each squatter, sorted by name ascending (determinism):
       newColor := palette.Allocate(used, reserved)
       emit Update{Name: u.name, Color: newColor}
       used += newColor

5. Converge configured labels
     for each d in desired, sorted by name ascending:
       if no match:
         emit Create{d.name, d.color, d.description}
       else:
         if m.color != d.color
         || m.description != d.description      # "" when omitted — authoritative
         || m.name != d.name                     # casing drift
           emit Update{...only changed fields...}
         else:
           emit NoOp   # retained for reporting, not sent to the API

6. Prune (mode == prune only)
     for each remaining unconfigured label:
       record as removal candidate
     removal set determined by:
       --prune=all      → all candidates
       interactive TTY  → huh.MultiSelect over candidates
       non-interactive  → error: prune requires --prune=all without a TTY
     emit Delete for each selected
```

### The `Action` type

Deliberately a plain serialisable struct with no behaviour and no client reference. This is what
keeps a future `plan -o file` / `apply file` split a thin shell rather than a refactor.

```go
type Kind string // "create" | "update" | "delete" | "noop"

type Action struct {
    Kind        Kind    `json:"kind"`
    Repo        string  `json:"repo"`          // owner/repo
    Name        string  `json:"name"`          // current name (lookup key)
    NewName     *string `json:"new_name,omitempty"`
    Color       *string `json:"color,omitempty"`
    Description *string `json:"description,omitempty"`
    Reason      string  `json:"reason,omitempty"` // e.g. `displaced by "type: bug"`
}
```

`Reason` exists for reporting: a recolour that looks arbitrary in a diff becomes obvious when
annotated with which configured label displaced it.

A `Plan` groups actions per repository, as a slice rather than a map, because the order actions are
emitted in is the order they have to be applied:

```go
type Plan struct {
    Repos []RepoPlan `json:"repos"`
}

type RepoPlan struct {
    Repo    string   `json:"repo"` // owner/repo
    Actions []Action `json:"actions"`
}
```

**Landed** ([#25](https://github.com/specsnl/labelsync/issues/25)) — the pointer contract, the wire
form, and why `Repo` appears on both an action and its group are documented in
[Architecture § Planner](./content/docs/architecture/plan.md).

---

## Colour allocation

`internal/palette`. Uses `github.com/lucasb-eyer/go-colorful`, which provides `Hex()`, `Lab()`,
and `DistanceCIEDE2000()` directly — and is already an indirect dependency via lipgloss, so it
adds nothing to the dependency tree.

### Rule

A displaced label is reassigned the candidate colour with the **maximum minimum perceptual
distance** (CIEDE2000 in CIELAB space) from every colour currently in use — i.e. the colour
that is *most different from everything else present*.

```go
func Allocate(used, reserved []colorful.Color) Allocation
```

**Landed** ([#24](https://github.com/specsnl/labelsync/issues/24)) — see
[Architecture § The allocation rule](./content/docs/architecture/palette.md#the-allocation-rule).
The return type is an `Allocation` rather than a bare colour: the exhaustion warning below has to
reach the caller somehow, and the hex form and the winning score are what the caller reports.

### Candidates

A deterministic HSL grid, filtered for legibility, converted to hex and sorted ascending:

- Hue: `0…350` step `10` (36 values)
- Saturation: `0.45`, `0.65`, `0.85`
- Lightness: `0.35`, `0.50`, `0.65`

≈324 candidates, deduplicated. Lightness is bounded away from the extremes because GitHub picks
label text colour automatically from background luminance; near-white and near-black backgrounds
produce poor contrast.

**Landed** ([#23](https://github.com/specsnl/labelsync/issues/23)) — the grid, its bounds, and the
determinism guarantees it carries are documented in
[Architecture § Colour Palette](./content/docs/architecture/palette.md).

### Determinism

Three things guarantee stable output across runs, which matters because re-running must not churn
colours:

1. Candidates are generated in fixed order and sorted by hex.
2. Ties break on first-wins via strict `>`, so the lowest hex value always wins.
3. Squatters are processed in ascending name order, and each newly assigned colour is added to
   `used` before the next allocation — so two squatters can never receive the same colour.

### Exhaustion

If every candidate is within a minimum-distance floor of an existing colour, the allocator
returns the best available anyway and the action carries a warning in `Reason`. It never fails
the run — a suboptimal colour is better than an aborted sync.

The floor is ΔE2000 ≈ 5. `Allocation.Exhausted` reports crossing it; turning that into a `Reason`
is the plan's half, and is still to come.

---

## GitHub API surface

| Operation             | Endpoint                              | Notes                                          |
|-----------------------|---------------------------------------|------------------------------------------------|
| List org repos        | `GET /orgs/{org}/repos`               | Paginated, 100/page                            |
| List own repos        | `GET /user/repos?affiliation=owner`   | Includes private                               |
| List user repos       | `GET /users/{user}/repos`             | Public only                                    |
| List repo labels      | `GET /repos/{o}/{r}/labels`           | Paginated, 100/page                            |
| Create label          | `POST /repos/{o}/{r}/labels`          | `name` + `color` required                      |
| Update / rename label | `PATCH /repos/{o}/{r}/labels/{name}`  | `new_name` **preserves** issue/PR associations |
| Delete label          | `DELETE /repos/{o}/{r}/labels/{name}` | **Destructive** — removes from all issues/PRs  |
| Rate limit status     | `GET /rate_limit`                     | Free — does not count against the limit        |

### Label names are case-insensitively unique

**Confirmed empirically** against the live API ([#16](https://github.com/specsnl/labelsync/issues/16)).
Four behaviours, all of which the reconciler depends on:

| Request                                                       | Result                                               |
|---------------------------------------------------------------|------------------------------------------------------|
| `POST` `bug`, then `POST` `Bug`                               | `422` `{"resource":"Label","code":"already_exists"}` |
| `GET /labels/{name}` with any casing                          | `200` — lookup is case-**in**sensitive               |
| `PATCH /labels/bug` with `new_name: Bug`                      | `200`, casing changes, **same label `id`**           |
| `PATCH` whose `new_name` collides with another label's casing | `422`, same `already_exists` shape                   |

Two consequences, and they pull in different directions, so both matter:

- **A repository can never hold two labels differing only by case.** Matching remote labels against
  the desired set case-insensitively is therefore unambiguous — at most one remote label can match
  a given configured name, so no tie-break rule is needed.
- **Casing drift is still a real state.** A repo holding `bug` while the config asks for `Bug` is
  perfectly possible; the two facts above do *not* collapse into "casing never differs". It is
  repaired by an ordinary `PATCH` with `new_name`, in one call, preserving the label `id` and every
  issue/PR association. This is why step 5 of the algorithm treats `m.name != d.name` as a genuine
  update rather than a no-op.

A `GET` or `PATCH` path segment resolves regardless of casing, but requests always address a label
by its **observed** remote name, with the desired spelling carried in `new_name`. That keeps the
request consistent with the state the plan was computed against, and keeps the ETag cache keyed on
what the API actually returned.

### Name and description lengths are counted in code points

**Confirmed empirically** against the live API ([#18](https://github.com/specsnl/labelsync/issues/18)).
The documented bounds are right, but the unit and the overflow behaviour were not:

| Request                                     | Result                                                     |
|---------------------------------------------|------------------------------------------------------------|
| `POST` name of 50 / description of 100      | `201`, stored byte-for-byte as sent                        |
| `POST` name of 51 / description of 101      | `422` `{"code":"custom","message":"... is too long"}`      |
| `PATCH` at the same boundaries              | Identical — create and update agree exactly                |
| `POST` description of 100 emoji (400 bytes) | `201` — the unit is code points, not bytes or UTF-16 units |
| `POST` name of only emoji                   | `422` `name must contain more than native emoji`           |
| `POST` name with surrounding whitespace     | `201`, stored **trimmed**                                  |

**GitHub never truncates.** Overflow is rejected outright, at one threshold — 150, 255, 256, and
1000 all fail the same way, so there is no second, larger bound hiding behind the documented one.
Local validation is therefore *mirroring* the server rather than being stricter than it, which is
what makes rejecting at config load a plain convenience: the same input would fail anyway, but
per-repository and partway through a run.

**The unit is Unicode code points**, and this is the part that cannot be guessed. A 100-emoji
description is 400 bytes and 200 UTF-16 units and is accepted; 101 emoji is not. The same boundary
holds for CJK and Latin-1 text regardless of encoded size. So `utf8.RuneCountInString` is correct
and `len()` is not — the latter would reject a valid 100-emoji description as though it were 400.

It is code points, **not grapheme clusters**: a ZWJ family emoji (👨‍👩‍👧) spends 5 of the 100, a
regional-indicator flag spends 2, and a decomposed `é` (`e` + U+0301) spends 2. Nothing is
Unicode-normalised on the way in — the decomposed form round-trips decomposed.

Two rules follow that the bounds alone do not imply:

- **A name may not consist only of emoji.** A separate `422`, independent of length: `🐛` is
  rejected, and so is `🐛` followed by a space, but `🐛 bug` is accepted. Emoji in names are
  permitted, just never as the whole of one. Worth rejecting locally for the same reason as the
  bounds — it is otherwise an apply-time failure on every targeted repository.
- **GitHub trims surrounding whitespace from names.** `"  bug  "` is stored as `"bug"` — the one
  case in the probe where the stored value differed from what was sent. Validation trims before
  checking the bound and before comparing against the remote label, so the desired name and the
  stored name always agree; otherwise a name with a stray space would produce a diff that never
  converges.

### Labels work when issues are disabled

Repository-scoped label endpoints are **not** gated on issues being enabled. Two pieces of
evidence:

- The docs list required permissions as *"Issues" (write)* **or** *"Pull requests" (write)* —
  reflecting that labels are a shared resource between issues and PRs.
- Repo-scoped label endpoints document `200/201/301/404/422` and **no `410`**, whereas the
  *issue-scoped* endpoints (`/issues/{n}/labels`) do document `410 Gone`, which is the
  "issues are disabled" response.

A repo with issues off still needs labels for pull requests, which is consistent with this.

**This has not been verified empirically** — treat as high-confidence, not certain. The design
tolerates being wrong: per-repository failures are non-fatal (see below), so a `410` would be
recorded and skipped rather than aborting the run.

### Per-repository failures are non-fatal

`403` (archived, or insufficient permission), `404` (renamed/deleted between enumeration and
sync), and `410` are collected per repository, the run continues, and a summary of skipped
repositories prints at the end. The process exit code reflects whether any repository failed.

A `422 already_exists` on create is **reclassified as an update**, not a failure — this handles
races and plans computed against slightly stale state.

---

## Request efficiency

### Cost model

| Phase       | Requests                         | Notes                                      |
|-------------|----------------------------------|--------------------------------------------|
| Enumeration | `ceil(N/100)` per org/user group | Response already carries everything needed |
| Label reads | `N` (1 per repo)                 | >100 labels in one repo is rare            |
| Writes      | 1 per action                     | The genuinely expensive phase              |

50 repositories ≈ 51 read requests against a 5,000/hour primary budget. **The read path is
already cheap**; the writes are what need managing.

### Filtering is free

`GET /orgs/{org}/repos` returns `archived`, `fork`, `private`, and `has_issues` on every entry.
All group filtering happens against the enumeration response — the tool must **never** issue a
per-repository `GET` just to check attributes.

### ETag conditional requests

The single most valuable optimisation. GitHub's REST best-practices documentation confirms that a
conditional request returning `304 Not Modified` **does not count against the primary rate limit**
when correctly authorised.

- Cache each repository's label list plus its `ETag` under `$XDG_CACHE_HOME/labelsync/`.
- Send `If-None-Match` on subsequent reads.
- On `304`, serve from cache at zero quota cost.

Because labels change rarely, hit rates should be very high — making repeat dry-runs both fast
and effectively free.

Cache entries are keyed by `owner/repo` and carry a schema version so a tool upgrade invalidates
cleanly. `--no-cache` bypasses; `cache clear` purges.

### Bounded parallel reads

Reads via `golang.org/x/sync/errgroup` with a concurrency limit (default 8, `--concurrency`).
Reads are not subject to the content-creation secondary limit, so this is purely about wall-clock
time while staying polite.

### Startup budget check

`GET /rate_limit` is free. Called once at startup to report the remaining budget in `--debug`, and
to refuse to begin an apply that obviously cannot finish.

---

## Rate limiting

`internal/github/ratelimit` wraps the client and does both proactive and reactive work.

### Proactive

- **Token bucket on writes**, default ~70/minute (`--write-rate`), staying under GitHub's
  content-creation ceiling of roughly 80/minute so the limit is usually never hit at all.
- **Header tracking** — after every response, read `x-ratelimit-remaining` and
  `x-ratelimit-reset`. If remaining falls below a threshold, sleep until reset *before* issuing
  the next request rather than racing into a `403`.

### Reactive

go-github's typed errors are the reason for choosing it:

| Error                         | Handling                                                                                   |
|-------------------------------|--------------------------------------------------------------------------------------------|
| `*github.RateLimitError`      | Primary limit — sleep until `Rate.Reset`                                                   |
| `*github.AbuseRateLimitError` | Secondary limit — honour `RetryAfter`; if absent, exponential backoff with jitter from 60s |
| `5xx`                         | Retry up to 3× with exponential backoff                                                    |
| `422 already_exists`          | Reclassify as update                                                                       |

Label create/update/delete are idempotent enough that retry is always safe.

### Countdown rendering

Three renderings, selected by context. Getting this wrong is how CLIs become unusable in CI.

| Context                 | Rendering                                                                   |
|-------------------------|-----------------------------------------------------------------------------|
| TTY + `--output=pretty` | Single line rewritten with `\r`, lipgloss-styled — see the sample below     |
| Non-TTY                 | Periodic log lines at a fixed interval — no control characters in log files |
| `--output=json`         | Structured events, no animation                                             |

```text
⏳ Secondary rate limit — resuming in 04:32 · 143 writes remaining
```

```json
{"level":"warn","event":"rate_limit_wait","kind":"secondary","seconds":272,"resume_at":"2026-07-31T14:22:10Z","writes_remaining":143}
```

`--max-wait <duration>` caps total waiting so a CI job cannot idle for an hour burning minutes.
Exceeding it exits with an error and a summary of what remained.

---

## CLI

> **Partly landed.** The root command, the persistent flags, and `version` are implemented in
> `internal/cmd` — see
> [Overview § How the tree is wired](./content/docs/architecture/overview.md#how-the-tree-is-wired).
> The subcommands below are still the plan.

```text
labelsync [--config <path>]
          [--debug]
          [--output pretty|json]          default: pretty
          [--no-cache]
          [--concurrency N]               default: 8
          [--write-rate N]                writes/min, default: 70
          [--max-wait <duration>]         default: 15m
│
├── sync                                  reconcile labels
│     [--dry-run]                         compute and print, write nothing
│     [--mode append|prune]               default: append
│     [--prune all]                       non-interactive: remove all unconfigured
│     [--group <name>]...                 restrict to specific groups
│     [--repo <owner/repo>]...            restrict to specific repos (bypasses groups)
│
├── export <owner/repo>                   dump a repo's labels as config YAML
│     [-o <file>]
│
├── init                                  scaffold a labels.yml
│
├── groups                                resolve and list group → repo membership
│     [--group <name>]...
│
├── cache
│   ├── clear
│   └── info
│
└── version [--dont-prettify]
```

`export` matters more than it looks: because descriptions are authoritative, a first run against
existing repositories will clear any description not present in the config. `export` produces a
faithful starting point so that never happens by accident.

`groups` is a pure read command — invaluable for confirming a selector matches what you think
before running a prune.

### Exit codes

> **Landed** as `internal/util/exit` — see
> [Output & Exit Codes](./content/docs/architecture/output.md#exit-codes).

Borrowed from `terraform plan -detailed-exitcode`. Without this, a CI dry-run can only ever pass,
which makes it useless as a check.

| Code | Meaning                                                          |
|------|------------------------------------------------------------------|
| `0`  | In sync — no changes needed / applied successfully with no drift |
| `1`  | Error (config invalid, auth failure, unrecoverable API error)    |
| `2`  | Drift detected — `--dry-run` found pending actions               |
| `4`  | Applied successfully, but one or more repositories were skipped  |

The outcome codes are disjoint bits and combine: a dry run that finds drift *and* cannot reach a
repository exits `6`. `1` stays exclusive — a failed run cannot also report on a live state it never
established.

### Non-interactive guard

If `--mode=prune` is requested without `--prune=all` and **stdin is not a TTY**, the tool exits
with `ErrInteractiveRequired` immediately. It must never present a `huh` prompt to a pipe — that
hangs a CI job indefinitely, which is the most common way interactive CLIs break in pipelines.

---

## Package structure

Mirrors specs-cli.

```text
labelsync/
├── main.go                       # XDG init, cmd.Execute()
├── go.mod
└── internal/
    ├── labelsync/                # ≈ specs-cli's internal/specs
    │   ├── configuration.go      # XDG paths, file name constants
    │   └── errors.go             # sentinel errors + KindOf()
    ├── cmd/                      # one file per Cobra command
    │   ├── root.go
    │   ├── app.go                # App struct, persistent flags
    │   ├── sync.go
    │   ├── export.go
    │   ├── init.go
    │   ├── groups.go
    │   ├── cache.go
    │   └── version.go
    ├── config/
    │   ├── config.go             # YAML load, defaults, normalisation
    │   ├── validate.go           # all rules from the Validation section
    │   └── resolve.go            # groups → repo sets, include_groups cycles
    ├── github/
    │   ├── auth.go               # token resolution chain
    │   ├── client.go             # go-github wrapper
    │   ├── repos.go              # enumeration + filtering
    │   ├── labels.go             # label CRUD
    │   ├── cache.go              # ETag store
    │   └── ratelimit/            # token bucket, backoff, countdown
    ├── plan/
    │   ├── plan.go               # Compute() — pure, no network
    │   ├── action.go             # Action, Kind, serialisation
    │   └── render.go             # human + JSON diff rendering
    ├── palette/
    │   ├── palette.go            # Allocate()
    │   └── candidates.go         # deterministic HSL grid
    ├── apply/
    │   └── apply.go              # executes a Plan, prune prompts
    └── util/
        ├── exit/                 # exit codes
        ├── output/               # lipgloss logger + table renderer
        └── validate/             # shared validators
```

### Why `plan` and `palette` are isolated

Neither imports `internal/github`. `plan.Compute` takes plain structs and returns plain structs;
`palette.Allocate` takes colours and returns a colour. Two consequences:

1. The interesting logic — group resolution, prune semantics, colour allocation, determinism — is
   testable with table-driven stdlib tests and zero HTTP mocking.
2. A future `plan -o file` / `apply file` split becomes a thin serialisation shell rather than a
   restructuring exercise.

---

## Error handling

Sentinel errors live in `internal/labelsync/errors.go`, are always wrapped with `%w`, and are
surfaced as a stable `error_kind` string in JSON output — exactly the specs-cli pattern.

```go
func KindOf(err error) string // stable kind string, or "" when no known sentinel is wrapped
```

**The wrapping rule is the package rule.** A call site with context to add never returns a
sentinel bare, and never renders one with `%v` or into a freshly constructed error — either
breaks both `errors.Is` matching and `KindOf`:

```go
return fmt.Errorf("%w: %s", labelsync.ErrInvalidColor, raw)
```

The kind strings are a public contract. They may be added to, never renamed.

| Sentinel                      | Kind string                  |
|-------------------------------|------------------------------|
| `ErrConfigNotFound`           | `config_not_found`           |
| `ErrAmbiguousConfigFile`      | `ambiguous_config_file`      |
| `ErrUnsupportedConfigVersion` | `unsupported_config_version` |
| `ErrEmptyConfig`              | `empty_config`               |
| `ErrDuplicateLabelName`       | `duplicate_label_name`       |
| `ErrDuplicateLabelColor`      | `duplicate_label_color`      |
| `ErrInvalidColor`             | `invalid_color`              |
| `ErrInvalidLabelName`         | `invalid_label_name`         |
| `ErrDescriptionTooLong`       | `description_too_long`       |
| `ErrUnknownGroup`             | `unknown_group`              |
| `ErrAmbiguousGroupSource`     | `ambiguous_group_source`     |
| `ErrCyclicGroup`              | `cyclic_group`               |
| `ErrInvalidRepoRef`           | `invalid_repo_ref`           |
| `ErrInvalidRename`            | `invalid_rename`             |
| `ErrNoToken`                  | `no_token`                   |
| `ErrInteractiveRequired`      | `interactive_required`       |
| `ErrRepoInaccessible`         | `repo_inaccessible`          |
| `ErrMaxWaitExceeded`          | `max_wait_exceeded`          |

**Adding a sentinel means adding a row here, a `KindOf` case, and an entry in the `allSentinels`
test table.** The test derives its expected set by parsing the package source for exported `Err*`
variables, so a sentinel that is declared but not tabled — or tabled after being removed — fails
the build rather than silently escaping `KindOf` and rendering an empty `error_kind`.

---

## Output

> **Landed.** `internal/util/output` is built; what it actually does is documented in
> [Output & Exit Codes](./content/docs/architecture/output.md).

All user-facing output goes through `output.Writer`, with `pretty` and `json` implementations
selected by `--output`. `log/slog` is a **debug-only diagnostic channel on stderr**, silent on a
normal run, and never used for user-facing reporting.

### Pretty diff

```text
specsnl/example-website
  + create   type: bug           #d73a4a  "Something isn't working"
  ~ update   type: feature       #1d76db → #0e8a16
  ~ recolour wontfix             #d73a4a → #16a3c4   (displaced by "type: bug")
  = ok       priority: high
  - delete   old-label                                (unconfigured)

specsnl/example-platform
  = ok       (5 labels, no changes)

3 repositories · 2 created · 2 updated · 1 deleted · 6 unchanged
```

### JSON

NDJSON per action plus a final summary object, so it streams and is parseable mid-run.

---

## CI integration

CI is a day-one target. The pattern needs no plan artifact:

- **Pull request check:** `labelsync sync --dry-run --output=json`. Exit `2` fails the check when
  the config and the live state disagree.
- **On merge / scheduled:** `labelsync sync`. Safe to run repeatedly — the reconciler is
  convergent, so a scheduled drift-correction job is a no-op when there is nothing to fix.

```yaml
name: labels
on:
  pull_request:
    paths: ["labels.yml"]
  push:
    branches: [main]
    paths: ["labels.yml"]
  schedule:
    - cron: "0 6 * * 1"        # weekly drift correction

jobs:
  sync:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: specsnl/labelsync-action@v1     # or `go install`
      - name: Check for drift
        if: github.event_name == 'pull_request'
        run: labelsync sync --dry-run --output=json
        env:
          GITHUB_TOKEN: ${{ secrets.LABELSYNC_TOKEN }}
      - name: Apply
        if: github.event_name != 'pull_request'
        run: labelsync sync --output=json
        env:
          GITHUB_TOKEN: ${{ secrets.LABELSYNC_TOKEN }}
```

### The Actions `GITHUB_TOKEN` will not work

The token automatically injected into a workflow is scoped to **the repository the workflow runs
in**. It cannot write labels to other repositories in the organisation. CI therefore needs one of:

| Option                       | Trade-off                                                                                                          |
|------------------------------|--------------------------------------------------------------------------------------------------------------------|
| **PAT as a secret**          | Works today with zero extra code — the auth resolver already falls back to `GITHUB_TOKEN`. Expires; needs rotation |
| **GitHub App install token** | No expiry to manage, higher rate limits, scoped to selected repositories. Requires an App and token-minting step   |

Start with a PAT secret. A GitHub App can be added later behind the same resolver interface
without touching call sites.

---

## Authentication

`internal/github/auth.go`, resolved in order, reporting which source won under `--debug`:

1. `--token` flag *(discouraged — visible in shell history and process lists)*
2. `GH_TOKEN`, then `GITHUB_TOKEN`
3. `github.com/cli/go-gh/v2/pkg/auth` → `auth.TokenForHost("github.com")`
4. Shell out to `gh auth token`

**Step 4 is not redundant.** Modern `gh` stores tokens in the system keychain by default on macOS,
which is *not* in `hosts.yml` — so `go-gh`'s config reader alone can miss a perfectly valid `gh`
login. Shelling out covers that case.

Failure to resolve any token exits with `ErrNoToken` and a message naming all four options.

---

## Relationship to org default labels

Organisation default labels are a real GitHub feature (shipped October 2019), configured at
*Org settings → Code, planning, and automation → Repository → Repository defaults →
Repository labels*. Two constraints shape how this tool relates to them:

- **No API.** There is no `/orgs/{org}/labels` endpoint. They are UI-only, so this tool cannot
  manage them.
- **New repos only.** GitHub's docs are explicit that adding, editing, or deleting a default label
  does not propagate to existing repositories. Anyone with write access can also edit or delete
  labels in their own repo afterwards.

| Layer              | Mechanism    | Scope                                        |
|--------------------|--------------|----------------------------------------------|
| Org default labels | UI, one-time | New repos only; drifts immediately           |
| `labelsync`        | API          | All repos; enforces convergence; re-runnable |

Because defaults do not propagate and drift is unpreventable, this tool is inherently a
*post-creation* reconciler — which is the argument for making it trivially re-runnable and
eventually wiring it into repo scaffolding.

**Possible later addition:** `labelsync check-org-defaults <org>` — a read-only command that diffs
the config against the org defaults and prints what to paste into the UI. Low priority; closes the
loop without pretending an API exists.

---

## Testing strategy

Stdlib `testing` only — specs-cli carries no test framework dependency, and the design does not
need one.

| Package            | Approach                                                                                                                                                                                                           |
|--------------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `config`           | Table-driven: every validation rule, valid + invalid; golden files for normalisation                                                                                                                               |
| `config` (resolve) | Group composition, cycle detection, glob include/exclude precedence, `skip_*` defaults                                                                                                                             |
| `plan`             | **The core suite.** Table-driven over `(desired, current, mode, renames) → expected actions`. Covers append vs prune, squatter detection, rename-before-match, casing drift, description clearing, action ordering |
| `palette`          | Determinism (same input → same output, repeated), no-duplicate-allocation across multiple squatters, exhaustion behaviour, legibility bounds                                                                       |
| `github`           | `net/http/httptest` fake: pagination, ETag `304` handling, `403`/`404`/`410` per-repo skip, `422` reclassification                                                                                                 |
| `ratelimit`        | Injected clock. Primary vs secondary backoff, `Retry-After` honouring, `--max-wait` ceiling                                                                                                                        |
| `output`           | Golden files for pretty and JSON renderings                                                                                                                                                                        |

Determinism deserves an explicit test that runs the full planner twice over the same fixtures and
asserts byte-identical output. Colour churn on re-run is the most likely subtle regression.

---

## Dependencies

| Package                                 | Purpose                                       | Shared with specs-cli                            |
|-----------------------------------------|-----------------------------------------------|--------------------------------------------------|
| `github.com/spf13/cobra`                | CLI command tree                              | yes                                              |
| `gopkg.in/yaml.v3`                      | Config parsing                                | yes                                              |
| `charm.land/huh/v2`                     | Interactive prune selection (`MultiSelect`)   | yes                                              |
| `charm.land/lipgloss/v2`                | Output styling, diff table, countdown         | yes                                              |
| `github.com/adrg/xdg`                   | Config + cache directory resolution           | yes                                              |
| `github.com/danwakefield/fnmatch`       | Repo include/exclude globs                    | yes                                              |
| `golang.org/x/sync`                     | `errgroup` bounded parallel reads             | yes                                              |
| `log/slog`                              | Debug logging (stdlib)                        | yes                                              |
| `github.com/google/go-github/v76`       | GitHub REST client                            | **new**                                          |
| `github.com/cli/go-gh/v2`               | Token resolution only                         | **new**                                          |
| `github.com/lucasb-eyer/go-colorful`    | CIELAB + CIEDE2000                            | **new (direct)** — already indirect via lipgloss |
| `github.com/charmbracelet/colorprofile` | Colour downsampling per output stream         | **new (direct)** — already indirect via lipgloss |
| `github.com/charmbracelet/x/term`       | `IsTerminal` for the non-colour TTY decisions | **new (direct)** — already indirect via lipgloss |

**Why go-github rather than a hand-rolled client or `go-gh`'s REST client:** the deciding factor
is typed `*github.RateLimitError` and `*github.AbuseRateLimitError`. Secondary rate limits are the
most likely failure mode for this tool — hundreds of sequential writes against an ~80/minute
ceiling — and having that as a distinguishable error type rather than a status code to sniff makes
the backoff logic clean and testable. `resp.NextPage` for enumeration is a bonus.

`go-gh` is used **only** for auth, because it is the only way to transparently reuse an existing
`gh` login.

---

## Distribution

Distributed as a **standalone binary**, matching specs-cli:

| Channel  | Command                                          |
|----------|--------------------------------------------------|
| Homebrew | `brew install specsnl/tap/labelsync`             |
| Go       | `go install github.com/specsnl/labelsync@latest` |
| Binaries | GitHub Releases via goreleaser                   |

Repo name, module name, and binary name all match. No `-cli` suffix — specs-cli needs one because
`specs` alone is ambiguous as a repo name, whereas `labelsync` is not.

### Why not a `gh` CLI extension

A `gh` extension (repo `gh-labelsync`, invoked as `gh labelsync`) was seriously considered. It
would delete the auth code entirely, need no release pipeline, and `gh` is preinstalled on
GitHub-hosted runners. It was rejected for four reasons:

1. **The auth benefit is already captured.** The resolver falls back to `gh`'s own token, so a
   developer with `gh` logged in never touches a PAT. The extension would additionally save only
   the ~80-line resolver — not worth restructuring distribution for.
2. **This is infrastructure, not a `gh` convenience.** Extensions suit interactive, gh-adjacent
   helpers. This tool reads a committed config file, runs on a schedule in CI, and reconciles
   state — closer in shape to Terraform than to `gh pr checkout`.
3. **Consistency compounds.** One Taskfile pattern, one goreleaser config, one Homebrew tap across
   all Specs Go tooling. Diverging here forces every future CLI to pick a side.
4. **`gh label` already exists** as a built-in command group (`list`/`create`/`edit`/`delete`/
   `clone`). `gh label` and `gh labelsync` being unrelated things is a permanent source of
   confusion that documentation cannot fix.

A thin `gh-labelsync` shim repo can be added later without touching this codebase, if the
extension ergonomics are ever wanted.

---

## Repository scaffolding

Matching specs-cli, so the developer experience is identical:

| File                      | Role                                                                                                     |
|---------------------------|----------------------------------------------------------------------------------------------------------|
| `Dockerfile`              | Pinned Go + tooling; the source of truth for reproducible builds                                         |
| `compose.yml`             | Wires Dockerfile stages into named services                                                              |
| `Taskfile.dist.yml`       | All developer workflows; wraps Docker Compose so it is never called directly                             |
| `.goreleaser.yml`         | Binaries, GitHub Releases, Homebrew tap                                                                  |
| `.golangci.yml`           | Linting                                                                                                  |
| `.markdownlint-cli2.yaml` | Docs linting                                                                                             |
| `.editorconfig`           |                                                                                                          |
| `AGENTS.md` / `CLAUDE.md` | Agent conventions, including the rule that code changes require tests + docs + README in the same change |
| `docs/`                   | Architecture docs under `docs/content/docs/architecture/`                                                |

---

## Milestones

| # | Scope                                                                                           | Network? |
|---|-------------------------------------------------------------------------------------------------|----------|
| 1 | `internal/config` (load, validate, resolve) + `internal/palette` + full test suites             | no       |
| 2 | `internal/plan` with complete append + prune logic and determinism tests                        | no       |
| 3 | `internal/github`: auth chain, enumeration + filtering, label reads, ETag cache                 | yes      |
| 4 | `sync --dry-run` end to end, pretty + JSON rendering, exit codes, `groups`, `export`, `init`    | yes      |
| 5 | `internal/apply` append mode + `ratelimit` (bucket, backoff, countdown in all three renderings) | yes      |
| 6 | Prune mode: report, `huh.MultiSelect`, `--prune=all`, non-TTY guard                             | yes      |
| 7 | Renames, `cache` commands, goreleaser + Homebrew, CI workflow, docs site                        | —        |

Milestones 1–2 are the entire interesting core and need no GitHub access at all — worth building
and testing first.

These exist as GitHub milestones `M0 · Foundation` through `M7 · Ship`. `M0` was added when the
tracker was seeded: the design assumes the sentinel errors, XDG paths, Cobra spine, and output
writer already exist, and they did not.

### Build order

Work is tracked as eight epics — one per subsystem — each with sub-issues carrying a milestone.
Epics deliberately carry no milestone, because several span more than one stage.

The critical path is **foundation → config load → planner → `sync --dry-run` → apply**. Everything
else hangs off it:

| Wave | Available in parallel                                                                          |
|------|------------------------------------------------------------------------------------------------|
| 1    | ruleset fix, sentinels + XDG paths, `AGENTS.md`, all three spikes, colour candidates           |
| 2    | output writer, config load, `Allocate()`, the `Action` type, auth, client                      |
| 3    | Cobra spine, group resolution, validation, `Compute()`, enumeration, label CRUD, rate limiting |
| 4    | renames, prune semantics, rendering, ETag cache, `init`, `groups`, `export`, docs              |
| 5    | determinism suite, countdown, `sync --dry-run`, `cache`                                        |
| 6    | apply, then prune, renames end to end, release verification, dogfood CI                        |

The spikes are the ones to start today: they need no code, they are fully parallel, and two of them
gate validation rules.

The `internal/github` epic depends only on the foundation and on the config package's selector
types, so it is fully independent of the config, palette, and plan epics. That is the cleanest
split when more than one person is working at once.

Issues labelled `parallel-safe` have no unlanded dependencies; `blocked` ones name what they wait
on. `gh issue list --label parallel-safe` answers "what can I pick up right now".

---

## Open questions

1. **Labels on issues-disabled repos.** High confidence they work, unverified. Confirm against a
   scratch repo with issues disabled. The design already tolerates being wrong.
2. **`has_issues: false` reporting.** Should such repos produce an informational note in the diff,
   even though syncing works? Probably yes, since it is surprising to see label changes on a repo
   with issues off.
3. **`plan`/`apply` split timing.** Deferred by design, with the planner kept pure so it stays a
   thin addition. Revisit if the CI approval-gate workflow becomes desirable.
4. **GitHub App auth timing.** PAT-as-secret is sufficient for CI initially. An App becomes
   worthwhile if PAT rotation becomes annoying or rate limits bite.

**Answered:**

- Case-insensitive label uniqueness ([#16](https://github.com/specsnl/labelsync/issues/16))
  — see [Label names are case-insensitively unique](#label-names-are-case-insensitively-unique).
- Name and description length limits ([#18](https://github.com/specsnl/labelsync/issues/18))
  — see [Name and description lengths are counted in code points](#name-and-description-lengths-are-counted-in-code-points).

---

## References

- [REST API endpoints for labels](https://docs.github.com/en/rest/issues/labels)
- [Best practices for using the REST API](https://docs.github.com/rest/guides/best-practices-for-using-the-rest-api) — conditional requests and `304` rate-limit exemption
- [Managing default labels for repositories in your organization](https://docs.github.com/en/organizations/managing-organization-settings/managing-default-labels-for-repositories-in-your-organization)
- [Create default labels at the organization level](https://github.blog/changelog/2019-10-17-create-default-labels-at-the-organization-level/) — changelog
- [specsnl/specs-cli — architecture overview](https://github.com/specsnl/specs-cli/blob/main/docs/content/docs/architecture/overview.md)
- [specsnl/specs-cli — library decisions](https://github.com/specsnl/specs-cli/blob/main/docs/content/docs/architecture/library-decisions.md)
- [google/go-github](https://github.com/google/go-github)
- [cli/go-gh — api package](https://pkg.go.dev/github.com/cli/go-gh/v2/pkg/api)
- [lucasb-eyer/go-colorful](https://github.com/lucasb-eyer/go-colorful)
