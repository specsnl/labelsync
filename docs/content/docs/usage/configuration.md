---
title: Configuration file
weight: 2
---

One YAML file describes every label you want and which repositories should have them. It is the
source of truth: `labelsync` reconciles repositories towards it and never the other way round.

`labelsync init` writes a working example, and
[`labelsync export <owner/repo>`](./commands.md#labelsync-export) writes one from a repository that
already has labels — which is where to start if any of your repositories do. How the loader
implements all of this is in
[Architecture § Configuration](../architecture/configuration.md).

## Where the file is found

Without `--config`, the file is searched for in this order:

1. `./labels.yml` or `./labels.yaml` in the working directory
2. `$XDG_CONFIG_HOME/labelsync/labels.yml` (default `~/.config/labelsync/labels.yml`)

Both spellings in one directory is an error rather than a coin flip — in *either* directory, even
when the other one holds a perfectly good file. The ETag cache lives under
`$XDG_CACHE_HOME/labelsync` (default `~/.cache/labelsync`).

`--config` takes a file or a directory: `--config ./ops` searches `./ops` for both spellings, the
same way the working directory is searched.

## A complete example

```yaml
version: 1

# ── Which repositories ────────────────────────────────────────────────
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

# ── Groups for labels that name none of their own ─────────────────────
defaults:
  groups: [specs-all]

# ── Renames, applied first, preserving issue/PR associations ──────────
renames:
  - from: "bug"
    to: "type: bug"
  - from: "enhancement"
    to: "type: feature"

# ── The labels ────────────────────────────────────────────────────────
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

## Top-level keys

| Key        | Required | What it holds                                                    |
|------------|----------|------------------------------------------------------------------|
| `version`  | yes      | The schema version. `1` is the only accepted value               |
| `groups`   | yes      | Named repository selectors                                       |
| `labels`   | yes      | The labels themselves. At least one                              |
| `defaults` | no       | `groups:` — the groups a label with no `groups:` of its own gets |
| `renames`  | no       | `from` → `to` migrations, applied before anything else in a run  |

`version` has to be named rather than guessed: a file with no version is rejected, so a future
schema change can be introduced without having to infer which one an existing file meant.

## `labels`

| Field         | Type     | Required | Notes                                                                                            |
|---------------|----------|----------|--------------------------------------------------------------------------------------------------|
| `name`        | string   | yes      | 1–50 characters. Emoji permitted, but never *only* emoji. Colon-style markup (`:bug:`) permitted |
| `color`       | string   | yes      | 6 hex digits. A leading `#` is accepted and stripped; the value is lower-cased                   |
| `description` | string   | no       | Up to 100 characters. **Authoritative** — omitting it *clears* whatever the repository has       |
| `groups`      | []string | no       | Which groups get this label. When absent, `defaults.groups` applies                              |

**Descriptions are authoritative, and that is the one thing to know before a first run.** Omitting
`description` does not mean "leave it alone"; it means "the description is empty", and the next
sync makes it so. Run [`export`](./commands.md#labelsync-export) first and the descriptions you
already have are in the file, so this never happens by accident.

Both length bounds are GitHub's own and count **characters, not bytes** — a description of 100
emoji is exactly at the limit, the same as 100 letters.

Colours have to be **unique across the whole file**, not per repository. That is what lets the
planner treat a colour as an identity: an unconfigured label found sitting on a configured colour
is moved off it, onto a colour of its own from the
[generated palette](../architecture/palette.md).

## `groups`

A group has **exactly one** source — `org`, `user`, `repos`, or `include_groups`. Setting two is an
error, and so is setting none.

| Field            | Type     | Applies to | What it does                                                                        |
|------------------|----------|------------|-------------------------------------------------------------------------------------|
| `org`            | string   | source     | Every repository in an organisation                                                 |
| `user`           | string   | source     | Every repository owned by a user                                                    |
| `repos`          | []string | source     | An explicit `owner/repo` list                                                       |
| `include_groups` | []string | source     | The union of other groups. May nest; cycles are an error                            |
| `include`        | []string | org/user   | Glob allowlist on the repository name. Empty means everything                       |
| `exclude`        | []string | org/user   | Glob denylist on the repository name, applied after `include`                       |
| `skip_archived`  | bool     | org/user   | Default `true`. Archived repositories reject writes, so filtering beats discovering |
| `skip_forks`     | bool     | org/user   | Default `true`                                                                      |
| `visibility`     | string   | org/user   | `all` \| `public` \| `private`. Default `all`                                       |

- `include` and `exclude` are globs over the **repository name only**, never `owner/repo`, and
  match case-insensitively — GitHub's names do too. `exclude` runs after `include`.
- The five filter fields apply to `org` and `user` groups. A `repos` entry names a repository
  outright and is never filtered out from under you.
- `include_groups` is a union, and it may nest. Two groups including each other is an error, and
  the message prints the whole chain — `a -> b -> c -> a`.
- `user` sees private repositories **only for the account the token belongs to**. Asking for
  `visibility: private` for anybody else selects nothing, and says so on stderr rather than
  quietly doing nothing.

The rule the whole tool rests on: a repository gets every label whose `groups` contain a group that
selects it — and **if no group selects a repository, `labelsync` never touches it**.

[`labelsync groups`](./commands.md#labelsync-groups) prints what each group actually resolves to,
and on stderr why each filtered repository was dropped. Run it whenever a selector is doing
something you did not expect.

## `defaults`

```yaml
defaults:
  groups: [specs-all]
```

A label with no `groups:` of its own gets these. It is the difference between repeating the same
`groups: [specs-all]` on forty labels and writing it once — nothing more. A label that names its
own groups ignores `defaults` entirely rather than adding to it.

## `renames`

```yaml
renames:
  - from: "bug"
    to: "type: bug"
```

A rename moves an existing label onto a configured name **without losing anything**: it becomes a
`PATCH` carrying `new_name`, so the label keeps its id and every issue and pull request that
carried it still carries it. A delete plus a create would look identical in the label list and
would strip the label from all of them.

Both halves are needed. The `renames:` entry says which existing label to move; a `labels:` entry
for the new name is what it is moved onto — a rename whose `to:` no label declares is rejected,
because it would land on nothing.

Three rules decide whether a rename happens, and all three compare names **case-insensitively**,
because GitHub's label identity does:

| The repository has         | What happens                                                              |
|----------------------------|---------------------------------------------------------------------------|
| `bug`, and no `type: bug`  | The rename is applied                                                     |
| No `bug`                   | Skipped silently — already migrated, or never had it                      |
| Both `bug` and `type: bug` | Skipped — the target is taken. `type: bug` converges; `bug` is left alone |

Because a rename whose `from` no longer exists is skipped silently, an entry can be left in the
file indefinitely: it is a migration that has already happened, and it is also what migrates the
repository you add to the group next month. The worked end-to-end recipe is in
[Commands § Renaming labels](./commands.md#renames).

That is why labelsync's own
[`labels.yml`](https://github.com/specsnl/labelsync/blob/main/labels.yml) carries `bug` →
`type: bug` and `enhancement` → `type: feature`: GitHub creates those two in every new repository,
and both entries are already inert here — the targets exist — while being the thing that migrates the
next repository instead of unlabelling its issues.

A case-only rename such as `bug` → `Bug` is **rejected**, and needs no entry: casing drift is
converged anyway, by the same mechanism and with the same associations kept.

## What is tidied up as the file is read

A few things are accepted in more than one spelling and mean the same thing, so `export`, edit,
`export` shows your edits and nothing else:

| You write                       | It means                                                   |
|---------------------------------|------------------------------------------------------------|
| `color: "#D73A4A"`              | `d73a4a` — a leading `#` is optional, case does not matter |
| `name: "  type: bug  "`         | `type: bug` — surrounding whitespace is trimmed            |
| A label with no `groups`        | The groups listed under `defaults.groups`                  |
| A group with no `skip_archived` | `skip_archived: true`                                      |
| A group with no `skip_forks`    | `skip_forks: true`                                         |
| A group with no `visibility`    | `visibility: all`                                          |

Names are trimmed before any validation, so `"  bug  "` and `"bug"` are the same name, and a name
that only fits within 50 characters once trimmed is fine.

## What makes a file invalid

The whole file is checked as it is read, **before any request is sent**, and the run stops at the
first problem with the rule it broke. Every rule here is one GitHub enforces anyway; checking
locally is what turns a `422` halfway through a run into a message naming the line to fix.

| The file says                                        | Why it is rejected                                                                                    |
|------------------------------------------------------|-------------------------------------------------------------------------------------------------------|
| No `version`, or a `version` other than `1`          | The schema version has to be named, not guessed                                                       |
| No labels                                            | There is nothing to reconcile                                                                         |
| `bug` and `Bug`                                      | GitHub cannot hold both — names are case-insensitively unique                                         |
| Two labels with the same colour                      | Colours are unique across the whole file, not per repository                                          |
| `color: abc`, `color: "#gggggg"`                     | A colour is 6 hex digits, with or without a leading `#`                                               |
| A name longer than **50 characters**                 | GitHub's own bound                                                                                    |
| A description longer than **100 characters**         | GitHub's own bound                                                                                    |
| `name: "🐛"`                                         | A name may contain emoji, but never be only emoji — `🐛 bug` is fine                                  |
| A `groups:` entry naming a group that is not defined | Almost always a typo                                                                                  |
| A group setting both `org:` and `repos:`, or neither | A group has exactly one source                                                                        |
| `include_groups` that comes back round to itself     | The group cannot be resolved to a set of repositories                                                 |
| `repos: [labelsync]`, `repos: ["specs nl/x"]`        | Repositories are written `owner/repo`, in GitHub's own characters                                     |
| A rename `to:` a name no label declares              | The rename would land on nothing                                                                      |
| A rename whose `from:` is a label the file declares  | Including a case-only rename such as `bug` → `Bug`, which is unnecessary — casing is converged anyway |

`labelsync init` writes a file that satisfies every one of these, which is the quickest way to see
what a valid one looks like. The scaffold goes through the same rules as part of the test suite, so
it can never hand you a file the next command rejects.

Under `--output=json`, each of these carries a stable `error_kind` — `duplicate_label_name`,
`invalid_color`, `cyclic_group`, and so on. The full list is in
[Error Handling](../architecture/error-handling.md).
