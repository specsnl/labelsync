# specsnl/labelsync

Synchronise GitHub issue/PR labels across a set of repositories from a local YAML file.

`labelsync` is a reconciler, not a script: for each target repository it reads the current labels,
resolves the desired set, computes an ordered plan, and then applies it — or prints it, under
`--dry-run`.

> **Status: in development.** The command tree, the output layer, and the exit codes are in place.
> The reconciling commands — `sync`, `export`, `init`, `groups`, `cache` — are not implemented yet;
> `labelsync version` is the only one that does anything today.

## Usage

```text
labelsync [flags] <command>
```

Run `labelsync --help` for the tree, or `labelsync <command> --help` for one command.

### Global flags

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

Without `--config`, the config file is searched for in this order:

1. `./labels.yml` or `./labels.yaml` in the working directory
2. `$XDG_CONFIG_HOME/labelsync/labels.yml` (default `~/.config/labelsync/labels.yml`)

Both spellings in one directory is an error. The ETag cache lives under `$XDG_CACHE_HOME/labelsync`.

### Commands

#### `labelsync version`

Prints the version. It is a result, not narration, so it goes to **stdout** and can be captured:

```sh
labelsync version                    # labelsync version 1.2.3
labelsync version --dont-prettify    # 1.2.3
labelsync --version                  # 1.2.3   — same as --dont-prettify
labelsync version --output=json      # {"version":"1.2.3"}
```

`--version` is a root flag rather than a global one: it answers for the binary, so
`labelsync sync --version` is not a question `sync` has to have an opinion about. It honours
`--output`, so `labelsync --output=json --version` gives you the record.

### Where the version comes from

| Build                        | Version string            |
|------------------------------|---------------------------|
| A released binary            | `1.2.3` — the release tag |
| `task build`, on a tag       | `1.2.3`                   |
| `task build`, past a tag     | `1.2.3-31-g69fca8f`       |
| `task build`, uncommitted    | `1.2.3-31-g69fca8f-dev`   |
| `task build`, no tags yet    | `69fca8f` — the commit    |
| `go build` with no `ldflags` | `dev`                     |

`task build` derives it with `git describe --tags --always --dirty=-dev`, strips the leading `v`,
and injects the result with `-ldflags -X`. Releases use goreleaser's `{{ .Version }}`, which is the
tag without the `v` already — so a local build and a release agree on how a version is spelled.

A tree with **no tags at all** has nothing to describe, so `--always` falls back to the abbreviated
commit hash. A bare SHA means the repository is untagged, not that the build is broken.

## Output

**stdout is the product; stderr is the story of making it.** The result — a rendered diff, a group
listing, the version — goes to stdout. Progress, warnings, failures, and `--debug` diagnostics go to
stderr, so `labelsync ... > out.txt` captures the answer and nothing else.

`--output=json` emits **NDJSON**: one self-contained JSON object per line, parseable as the run
proceeds rather than only at the end. stdout carries the data, one object per row:

```json
{"group":"websites","repositories":12,"source":"org: specsnl"}
```

stderr carries the narration, every line with a `level`, and failures with a stable `error_kind`:

```json
{"level":"warn","message":"skipping specsnl/old-thing: archived"}
{"error_kind":"repo_inaccessible","level":"error","message":"repository is inaccessible: specsnl/old-thing"}
```

Messages are prose and may be reworded; `error_kind` and the row keys are a contract — added to,
never renamed.

## Exit codes

Borrowed from `terraform plan -detailed-exitcode`: without a code meaning "succeeded, and found
work", a CI dry run can only ever pass, which makes it useless as a check.

| Code | Meaning                                                                        |
|------|--------------------------------------------------------------------------------|
| `0`  | In sync — no changes needed, or every action applied and no repository skipped |
| `1`  | The run failed. Nothing about the live state can be inferred                   |
| `2`  | Drift detected — a dry run found pending actions                               |
| `4`  | Applied successfully, but one or more repositories could not be reached        |

The **outcome codes are disjoint bits and combine**: a dry run that finds drift *and* cannot reach a
repository exits `6`. Test bits, not equality. `1` stays exclusive — a failed run cannot also report
on a live state it never established.

```sh
labelsync sync --dry-run; rc=$?
(( rc == 1 )) && exit 1                                    # the run itself failed
(( rc & 2 ))  && echo "labels have drifted"
(( rc & 4 ))  && echo "some repositories were unreachable"
```

`if labelsync sync; then` is unaffected: zero still means clean.

## Development

Every command runs through [Task](https://taskfile.dev), which wraps the Docker Compose services
that pin the Go, golangci-lint, and Node versions. Run `task --list` for the full set.

```sh
task checkall   # lint, test, md:check — run this before opening a pull request
task build      # build the binary into the working directory
```

Conventions and workflow: [AGENTS.md](./AGENTS.md). Design plan: [docs/design.md](./docs/design.md).
Architecture: [docs/content/docs/architecture/](./docs/content/docs/architecture/).
