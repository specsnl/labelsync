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
| `--token`        | resolved, below    | GitHub token (discouraged — prefer `GH_TOKEN`)              |
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

## Where the token comes from

`labelsync` never asks for a credential. It resolves one from four sources, in order, first
non-empty wins:

1. `--token`
2. `GH_TOKEN`, then `GITHUB_TOKEN`
3. The `gh` config file
4. `gh auth token`

If you are already logged in with `gh auth login`, steps 3 and 4 cover you and there is nothing to
set up. Steps 3 and 4 are both needed: `gh` keeps its token in the system keychain on macOS, where
reading the config file alone would not find it.

`--token` works, and it is **discouraged**: a token on the command line is in your shell history and
in every process list on the machine. `GH_TOKEN` is the same convenience without either. The value
is never written to a log, including under `--debug` — that only reports *which* of the four sources
won, which is worth knowing when a run turns out to have used a different account than you expected.

In CI, note that the `GITHUB_TOKEN` a workflow is handed automatically is scoped to the repository
the workflow runs in, so it **cannot** write labels to other repositories. Use a PAT in a secret.

With nothing to find, the run fails with `no_token` before any request is sent.

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

`labelsync init` writes a file that satisfies every one of these, which is the quickest way to see
what a valid one looks like.

Both length bounds count **characters, not bytes** — a description of 100 emoji is exactly at the
limit, the same as 100 letters. The counting matches GitHub's, so nothing that passes here is
rejected later for being too long.

Names are trimmed before any of this, so `"  bug  "` and `"bug"` are the same name, and a name
that only fits once trimmed is fine.

Under `--output=json`, each of these carries a stable `error_kind` — `duplicate_label_name`,
`invalid_color`, `cyclic_group`, and so on. The full list is in
[Error Handling](../architecture/error-handling.md).

## Commands

`sync --dry-run`, `groups`, `init`, and `version` are implemented so far. Applying, `export`, and
`cache` are the planned tree — see the
[design plan](https://github.com/specsnl/labelsync/blob/main/docs/design.md#cli).

### `labelsync sync --dry-run`

Computes what it would take to make every selected repository match the config, prints the diff,
and writes nothing.

```sh
labelsync sync --dry-run                              # every group
labelsync sync --dry-run --group websites             # only these groups, repeatable
labelsync sync --dry-run --repo specsnl/labelsync     # only these repositories, repeatable
labelsync sync --dry-run --mode prune                 # also list what would be removed
labelsync sync --dry-run --output=json                # NDJSON, one action per line
```

| Flag        | Default  | What it does                                                         |
|-------------|----------|----------------------------------------------------------------------|
| `--dry-run` | off      | Compute and print, write nothing. **Currently required**             |
| `--mode`    | `append` | `append` never deletes; `prune` also lists unconfigured labels       |
| `--group`   | all      | Restrict to a group, repeatable                                      |
| `--repo`    | —        | Restrict to an `owner/repo`, repeatable, bypassing group enumeration |

**Applying is not implemented yet**, so `--dry-run` is required and a `sync` without it fails
rather than printing a plan and quietly applying nothing.

`--repo` bypasses *enumeration*, not the config. A repository named on the command line still gets
only the labels the groups that select it ask for, and a repository no group selects gets nothing
at all — said out loud on stderr rather than silently. Bypassing that too would make `--repo` the
one way to touch a repository the config does not cover, which is the safety property the whole
tool rests on.

`--mode prune` reaches the planner, which lists every unconfigured label as a *removal candidate*.
Nothing is deleted: choosing which candidates go is a later step, and a dry run writes nothing
either way.

Run `labelsync export <owner/repo>` before the first sync against repositories that already have
labels. Descriptions are authoritative, so a label whose description your config does not carry has
its description cleared.

#### Exit codes

```sh
labelsync sync --dry-run; rc=$?
(( rc == 1 ))  && exit 1                                  # the run itself failed
(( rc & 2 ))   && echo "labels have drifted"
(( rc & 4 ))   && echo "some repositories were unreachable"
```

`2` and `4` are **bits and combine** — a dry run that finds drift *and* cannot reach a repository
exits `6`. Test bits, not equality. `1` stays exclusive: a failed run has no live state to report
on. Exit `2` prints no error line at all, because the drift *was* the successful result and the
diff is already on stdout.

### `labelsync groups`

Prints which repositories each group actually resolves to. It writes nothing, and it is the
command to run before a prune or before the first sync against a new selector.

```sh
labelsync groups                                        # every group
labelsync groups --group specs-all --group personal     # only these, repeatable
labelsync groups --output=json | jq 'select(.repositories == 0)'
```

**stdout** is the table — one row per group, with the group, where its repositories come from, how
many it selected, and which ones. In JSON, `repositories` is a **number** and `repos` is an array,
so a consumer filters on them rather than matching prose:

```json
{"group":"websites","source":"org: specsnl","repositories":2,"repos":["specsnl/a","specsnl/b"]}
```

**stderr** is the explanation, and it is most of the point of the command:

- every repository a group's filters removed, with the reason — `archived, and skip_archived is
  on`, `a fork, and skip_forks is on`, `visibility: public`, `matched by an exclude glob`,
  `matched by no include glob`. The absence of a repository you expected is the thing this command
  exists to explain, so it is one line per repository rather than a count;
- a warning for any group that resolves to **no repositories at all**;
- a warning when `visibility: private` was asked for a user who is not the one the token belongs
  to, which GitHub can only ever answer with nothing;
- the usual end-of-run summary of repositories that could not be reached.

Pipe stdout and you keep all of that on the terminal. `2>/dev/null` keeps only the table.

A `--group` naming a group the config does not define is an error (`unknown_group`), not an empty
table: reporting nothing for a typo is how a working selector gets blamed. An owner that cannot be
listed does not stop the run — the other groups still resolve, and the exit code carries `4`.

### `labelsync init`

Writes a starter `labels.yml` — a worked example with a group per source kind, `defaults.groups`,
a rename, and four labels — into the working directory.

```sh
labelsync init                            # ./labels.yml
labelsync --config ops init               # ops/labels.yml
labelsync --config ops/labels.yml init    # exactly there
labelsync init --force                    # overwrite what is already there
```

`--config` chooses the destination: a path writes there, a directory writes `labels.yml` inside it.

| Situation                                       | What happens                                        |
|-------------------------------------------------|-----------------------------------------------------|
| Nothing is there                                | The file is written, and its path goes to stdout    |
| The file already exists                         | `config_exists` — nothing is written; use `--force` |
| The **other** spelling is there (`labels.yaml`) | `ambiguous_config_file` — even with `--force`       |

The last row is not `--force` being over-cautious. A directory holding both `labels.yml` and
`labels.yaml` is rejected by *every later run*, a step removed from the command that caused it, and
there is no version of "I know what I am doing" that makes the result loadable. Remove the other
file first.

The scaffolded config is guaranteed to validate: it goes through the same rules any other config
file does, as part of the test suite, so `init` can never hand you a file the next command rejects.

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
