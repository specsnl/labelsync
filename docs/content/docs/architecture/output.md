---
title: Output & Exit Codes
weight: 3
---

Everything a user is meant to read goes through `output.Writer`. Everything only a maintainer is
meant to read goes through `log/slog`. The two never mix, and the split is settled in
`internal/util/output` so that no later package has to decide it again.

Process exit codes live next door, in `internal/util/exit`.

## The boundary

| Channel         | Stream         | Audience              | On a normal run                   |
|-----------------|----------------|-----------------------|-----------------------------------|
| `output.Writer` | stdout, stderr | The person running it | Everything they were meant to see |
| `log/slog`      | stderr         | Someone debugging     | **Silent** — nothing at all       |

`slog` is a debug-only diagnostic channel. `fmt.Println` is never used for reporting: it cannot be
captured in a test, cannot switch to JSON, and has no idea which stream it belongs on.

## `output.Writer`

```go
type Writer interface {
    Info(format string, args ...any)   // stdout
    Warn(format string, args ...any)   // stderr
    Error(format string, args ...any)  // stderr
    WriteErr(err error)                // stderr
    Table(headers []string, rows [][]string)
}
```

Informational output and tables go to **stdout**; warnings and errors go to **stderr**. That split
is what keeps `labelsync groups --output=json | jq` working when a repository turns out to be
inaccessible halfway through.

Two implementations back the `--output` flag:

| Implementation | `--output` | Rendering                                           |
|----------------|------------|-----------------------------------------------------|
| `PrettyWriter` | `pretty`   | lipgloss-styled text, colour-degraded to the stream |
| `JSONWriter`   | `json`     | NDJSON — one self-contained object per line         |

Both take their streams as constructor arguments, so a test captures output by passing
`bytes.Buffer`s rather than by intercepting the process.

### Writes are best-effort

`Writer` has no error channel. Reporting a failed write would itself need a working stream, so
output calls discard their error. This is why `.golangci.yml` excludes `fmt.Fprintln` and
`lipgloss.Fprintln` from `errcheck`, and why `writeJSONLine` discards the encoder's error with an
explicit `_ =`.

## Colour and TTY detection

`NewPrettyWriter` wraps each stream once, at construction, in a `colorprofile.Writer`. That writer
asks the stream what it can render — truecolor, 256-colour, 16-colour, or nothing at all — and
downsamples or strips the escape sequences to match. `NO_COLOR`, `CLICOLOR`, and `CLICOLOR_FORCE`
are honoured.

The consequence that matters: a run piped into a CI log gets the same text with the escapes
removed, not a wall of `\x1b[`. Styling degrades; it does not have to be switched off by hand.

Detection happens once rather than per line, and the environment is a constructor argument rather
than an ambient read, so the golden tests are the same on a developer's terminal and in CI.

`output.IsTTY` exists for the decisions that are *not* about colour, and that colour degradation
therefore cannot make for us:

- the rate-limit countdown, which rewrites one line with `\r` on a terminal but logs at a fixed
  interval into a pipe, because control characters in a CI log are unreadable;
- the prune prompt, which must never be shown to a pipe — a CI job blocked on an invisible prompt
  hangs until someone cancels it.

## NDJSON

`JSONWriter` emits exactly one complete JSON object per line. Not one array: an array could only be
closed once the last row was known, and a run killed halfway would leave a truncated document
behind. One object per line means a consumer can parse the stream as it arrives, and whatever was
written before the kill is still valid.

```json
{"level":"info","message":"resolving 3 groups"}
{"group":"websites","repositories":"12","source":"org: specsnl"}
{"error_kind":"repo_inaccessible","level":"error","message":"repository is inaccessible: specsnl/old-thing"}
```

`WriteErr` adds `error_kind` when the error wraps a known sentinel — see
[Error Handling](./error-handling.md). The message is prose and may be reworded; `error_kind` is the
contract.

Table headers are normalised into JSON keys by `output.JSONKey`: lowercased, with runs of
non-alphanumeric characters collapsed to `_`. `"New colour"` becomes `new_colour`. Pretty and JSON
output share one set of headers with two different audiences, and normalising here means rewording
a column heading does not silently break someone's `jq` filter — only an actual rename does.

HTML escaping is off. Label names legitimately contain `&` and `<`, and rendering them as `&amp;`
helps nobody reading a log.

## Table rendering

Two renderers, because the diff and the list commands want different things:

| Function        | Shape                                 | Used by                                       |
|-----------------|---------------------------------------|-----------------------------------------------|
| `RenderColumns` | Aligned columns, no header, no border | The pretty diff                               |
| `RenderTable`   | Bordered table with a header row      | `PrettyWriter.Table` — `groups`, `cache info` |

The diff is a list, not a table: there is nothing to put in a header row, and a box around it would
fight the per-repository grouping. But the columns still have to line up down the page or the
changes are unreadable.

```text
+  create    type: bug       #d73a4a            "Something isn't working"
~  recolour  wontfix         #d73a4a → #16a3c4  (displaced by "type: bug")
=  ok        priority: high
```

Column widths are measured in **display width**, not bytes. A label name may contain an emoji, and
`len()` would over-pad the column by several characters.

## Debug logging

```go
func SetupLogger(stderr io.Writer, format Format, debug bool) *slog.LevelVar
```

`SetupLogger` installs the process-wide default `slog` logger on stderr and returns the `LevelVar`
gating it. Without `--debug` the level is `output.LevelSilent` — above every level `slog` defines,
so nothing is emitted at all. A silent default is the point: without it, warn- and error-level
diagnostics would leak onto stderr and interleave with the real output.

The level is returned rather than baked in because Cobra parses persistent flags after the command
tree is built: construct the logger once, then `level.Set(slog.LevelDebug)` from `PersistentPreRunE`
once `--debug` is known.

Records follow the output format, so `--debug --output=json` gives a stderr stream that is JSON all
the way down. Stderr and never stdout, for the same reason warnings go there.

## Exit codes

`internal/util/exit`. Borrowed from `terraform plan -detailed-exitcode`: a dry run that finds
pending work exits non-zero without that meaning the tool broke. Without it, a CI dry-run can only
ever pass, which makes it useless as a check.

| Constant       | Code | Meaning                                                                        |
|----------------|------|--------------------------------------------------------------------------------|
| `exit.OK`      | `0`  | In sync — no changes needed, or every action applied and no repository skipped |
| `exit.Error`   | `1`  | The run failed. Nothing about the live state can be inferred                   |
| `exit.Drift`   | `2`  | Completed without writing and found pending actions — `sync --dry-run`         |
| `exit.Skipped` | `3`  | Applied successfully, but one or more repositories could not be reached        |

The numbers are a public contract — pipelines branch on them — so they may be added to, never
renumbered. `exit_test.go` spells each value out literally so a renumbering fails there rather than
in someone's pipeline.

## Testing

Golden files under `internal/util/output/testdata/`, regenerated with:

```sh
task dc:run:go-builder -- go test ./internal/util/output/ -update
```

The pretty rendering has **two** goldens per stream: one produced with an empty environment, where
every escape sequence is stripped, and one with `CLICOLOR_FORCE=1` where they are not. The pair is
the actual claim — that the plain rendering is colour *degrading cleanly*, rather than styles never
having been applied.

The JSON tests assert the NDJSON property directly: every line of both streams parses on its own as
a complete object.
