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

Both spellings in one directory is an error rather than a coin flip. The ETag cache lives under
`$XDG_CACHE_HOME/labelsync` (default `~/.cache/labelsync`).

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
