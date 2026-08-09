---
title: Usage
weight: 5
---

How to drive `labelsync` from a terminal or a CI job. The reasoning behind these choices lives in
the [architecture section](../architecture/); this page is the reference.

```text
labelsync [flags] <command>
```

`labelsync --help` prints the tree; `labelsync <command> --help` prints one command.

## Global flags

Every command accepts these.

| Flag             | Default            | What it does                                                |
|------------------|--------------------|-------------------------------------------------------------|
| `-c`, `--config` | search order below | Path to the config file                                     |
| `-o`, `--output` | `pretty`           | Output format: `pretty` or `json`                           |
| `--debug`        | off                | Write debug diagnostics to stderr                           |
| `--no-cache`     | off                | Ignore the ETag cache for this run                          |
| `--concurrency`  | `8`                | Maximum repositories read in parallel                       |
| `--write-rate`   | `70`               | Maximum label writes per minute                             |
| `--max-wait`     | `15m`              | Longest a rate-limit backoff may sleep before the run fails |

A value the flags cannot honour is rejected before any work starts: an `--output` that is neither
format, a `--concurrency` or `--write-rate` below `1`, a negative `--max-wait`.

`--version` is a root flag rather than a global one — it answers for the binary, so
`labelsync sync --version` is not a question `sync` has to have an opinion about.

## Where the config file is found

Without `--config`, the file is searched for in this order:

1. `./labels.yml` or `./labels.yaml` in the working directory
2. `$XDG_CONFIG_HOME/labelsync/labels.yml` (default `~/.config/labelsync/labels.yml`)

Both spellings in one directory is an error rather than a coin flip — in *either* directory, even
when the other one holds a perfectly good file. The ETag cache lives under
`$XDG_CACHE_HOME/labelsync` (default `~/.cache/labelsync`).

`--config` takes a file or a directory: `--config ./ops` searches `./ops` for both spellings, the
same way the working directory is searched.

### What the file is tidied into

The config file is normalised as it is read, so a few things are accepted in more than one
spelling and mean the same thing:

| You write                       | It means                                                   |
|---------------------------------|------------------------------------------------------------|
| `color: "#D73A4A"`              | `d73a4a` — a leading `#` is optional, case does not matter |
| `name: "  type: bug  "`         | `type: bug` — surrounding whitespace is trimmed            |
| A label with no `groups`        | The groups listed under `defaults.groups`                  |
| A group with no `skip_archived` | `skip_archived: true`                                      |
| A group with no `skip_forks`    | `skip_forks: true`                                         |
| A group with no `visibility`    | `visibility: all`                                          |

Omitting a label's `description` is **not** shorthand for "leave it alone" — descriptions are
authoritative, so an omitted one clears whatever the repository has. Use `export` to capture the
descriptions you already have before the first run.

The full file reference — every field of `groups`, `defaults`, `renames`, and `labels` — is in the
[design plan](https://github.com/specsnl/labelsync/blob/main/docs/design.md#configuration).

### How a group selects repositories

A group has **exactly one** source — `org`, `user`, `repos`, or `include_groups`. Setting two is an
error, and so is setting none.

```yaml
groups:
  specs-all:
    org: specsnl
    include: ["boilr-*"]         # allowlist; empty means everything
    exclude: ["*-archive"]       # denylist, applied after include
  personal:
    user: Ilyes512
  everything:
    include_groups: [specs-all, personal]
```

- `include` and `exclude` are globs over the **repository name only**, never `owner/repo`, and
  match case-insensitively — GitHub's names do too. `exclude` runs after `include`.
- `include`, `exclude`, `skip_archived`, `skip_forks`, and `visibility` apply to `org` and `user`
  groups. A `repos` entry names a repository outright and is never filtered out from under you.
- `include_groups` is a union, and it may nest. Two groups including each other is an error, and
  the message prints the whole chain — `a -> b -> c -> a`.
- `user` sees private repositories **only for the account the token belongs to**. Asking for
  `visibility: private` for anybody else selects nothing, and says so on stderr rather than
  quietly doing nothing.

The rule the whole tool rests on: a repository gets every label whose `groups` contain a group that
selects it — and **if no group selects a repository, `labelsync` never touches it**.

### What makes a config file invalid

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

Both length bounds count **characters, not bytes** — a description of 100 emoji is exactly at the
limit, the same as 100 letters. The counting matches GitHub's, so nothing that passes here is
rejected later for being too long.

Names are trimmed before any of this, so `"  bug  "` and `"bug"` are the same name, and a name
that only fits once trimmed is fine.

Under `--output=json`, each of these carries a stable `error_kind` — `duplicate_label_name`,
`invalid_color`, `cyclic_group`, and so on. The full list is in
[Error Handling](../architecture/error-handling.md).

## Commands

Only `version` is implemented so far. `sync`, `export`, `init`, `groups`, and `cache` are the
planned tree — see the [design plan](https://github.com/specsnl/labelsync/blob/main/docs/design.md#cli).

### `labelsync version`

Prints the version. It is a result, not narration, so it goes to **stdout** and can be captured.

```sh
labelsync version                    # labelsync version 1.2.3
labelsync version --dont-prettify    # 1.2.3
labelsync --version                  # 1.2.3   — identical to --dont-prettify
labelsync version --output=json      # {"version":"1.2.3"}
```

What the string itself looks like depends on how the binary was built:
[Versioning](../architecture/versioning.md).

## Output and exit codes

**stdout is the product; stderr is the story of making it.** The result goes to stdout; progress,
warnings, failures, and `--debug` diagnostics go to stderr, so `labelsync ... > out.txt` captures the
answer and nothing else. `--output=json` emits NDJSON — one self-contained object per line, with a
stable `error_kind` on failures.

Exit codes follow `terraform plan -detailed-exitcode`: `0` in sync, `1` failed, and the **outcome
bits** `2` drift and `4` repositories skipped, which combine — a dry run that finds both exits `6`.
Test bits, not equality.

Both contracts in full, with the reasoning:
[Output & Exit Codes](../architecture/output.md).
