---
title: Usage
weight: 5
---

How to drive `labelsync` from a terminal or a CI job. These pages are the reference; the reasoning
behind the choices they describe lives in the [architecture section](../architecture/).

```text
labelsync [flags] <command>
```

| Page                                     | Covers                                                                     |
|------------------------------------------|----------------------------------------------------------------------------|
| [Getting started](./getting-started.md)  | Install, export first, describe the repositories, dry run, apply           |
| [Configuration file](./configuration.md) | `version`, `groups`, `defaults`, `renames`, `labels`, and what is rejected |
| [Commands](./commands.md)                | Every command and flag: `sync`, `export`, `groups`, `init`, `cache`        |
| [Running in CI](./ci.md)                 | Exit codes, NDJSON, the workflow recipe, and the token CI needs            |

New to it? [Getting started](./getting-started.md) — and note the one thing that is not reversible
by rerunning: descriptions in the config file are authoritative, so
[`labelsync export`](./commands.md#labelsync-export) comes before your first sync against
repositories that already have labels.

## Output and exit codes, in one paragraph

**stdout is the product; stderr is the story of making it.** The result goes to stdout; progress,
warnings, failures, and `--debug` diagnostics go to stderr, so `labelsync ... > out.txt` captures
the answer and nothing else. `--output=json` emits NDJSON — one self-contained object per line,
with a stable `error_kind` on failures.

Exit codes follow `terraform plan -detailed-exitcode`: `0` in sync, `1` failed, and the **outcome
bits** `2` drift and `4` repositories skipped, which combine — a dry run that finds both exits `6`.
Test bits, not equality. In full: [Running in CI § Exit codes](./ci.md#exit-codes), and with the
reasoning, [Output & Exit Codes](../architecture/output.md).
