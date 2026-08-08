---
title: Output & Exit Codes
weight: 3
---

Everything a user is meant to read goes through `output.Writer`. Everything only a maintainer is
meant to read goes through `log/slog`. The two never mix, and the split is settled in
`internal/util/output` so that no later package has to decide it again.

Process exit codes live next door, in `internal/util/exit`.

The conventions here are Unix ones, not house style; the reasons are what make them worth following
rather than memorising. The [review checklist](#review-checklist) at the end is the short version.

## The one rule

**stdout is the product. stderr is the story of making it.**

- **stdout** — the thing a user would pipe into another command or redirect to a file. The rendered
  diff, the NDJSON action stream, a resolved group listing. If `> out.txt` should capture it, it is
  stdout.
- **stderr** — everything else. Warnings, errors, progress, prompts, spinners, debug logs.

This is not a stylistic preference: it is the only thing that makes
`labelsync groups --output=json | jq '.[] | .repo'` work. One narration line on stdout corrupts the
pipe. And because diagnostics are on stderr, the user still sees them on the terminal *while* piping
stdout elsewhere — which is also why `2>/dev/null` stays meaningful.

Ask of every new line of output: **would someone want this in the file if they redirected stdout?**
If no, it is stderr.

## The boundary

| Channel         | Stream         | Audience              | On a normal run                   |
|-----------------|----------------|-----------------------|-----------------------------------|
| `output.Writer` | stdout, stderr | The person running it | Everything they were meant to see |
| `log/slog`      | stderr         | Someone debugging     | **Silent** — nothing at all       |
| `fmt.Println`   | —              | —                     | **Never**                         |

`fmt.Println` is never used for reporting. It cannot be captured in a test, cannot switch to JSON,
and has no idea which stream it belongs on. Three defects, no upside.

### Choosing between them

| You want to…                                  | Use               | Lands on |
|-----------------------------------------------|-------------------|----------|
| Emit the result the command exists to produce | `w.Table(...)`    | stdout   |
| Tell the user what the code is doing          | `w.Info(...)`     | stderr   |
| Report a recoverable problem (skipped repo)   | `w.Warn(...)`     | stderr   |
| Report a failure                              | `w.Error(...)`    | stderr   |
| Report a failure carrying a sentinel          | `w.WriteErr(err)` | stderr   |
| Tell a maintainer what the code is doing      | `slog.Debug(...)` | stderr   |

`WriteErr` over `Error("%v", err)` whenever you hold an `error`: it runs `labelsync.KindOf` and adds
`error_kind` to the JSON object. `Error` with a formatted string cannot.

## `output.Writer`

```go
type Writer interface {
    Info(format string, args ...any)   // stderr
    Warn(format string, args ...any)   // stderr
    Error(format string, args ...any)  // stderr
    WriteErr(err error)                // stderr
    Table(headers []string, rows [][]string)  // stdout
}
```

`Table` is the only method on stdout, and the asymmetry is the point: **`Writer` has exactly one
product channel, and everything else narrates.** `Info` is progress — "resolving 3 groups",
"applying 12 actions" — and progress is not what `> out.txt` is for.

In JSON mode it is not only a stream-choice question. Narration on stdout would interleave objects
that have none of the fields the data objects have:

```json
{"level":"info","message":"resolving 3 groups"}
{"group":"websites","repositories":"12","source":"org: specsnl"}
```

Valid NDJSON, and `jq -r .group` still yields a `null` for the first line. With `Info` on stderr the
stdout stream is uniformly typed and `jq` never sees a record it cannot type — which
`TestJSONWriter_StdoutIsUniformlyTyped` asserts directly, by requiring every stdout line to carry
the data key and no `level`.

`specs-cli` puts `Info` on stdout and has commands depending on that. labelsync diverges
deliberately: nothing here called `Info` when the decision was made, so there was no contract to
break, and inheriting the defect to stay symmetrical is the wrong trade. If a command ever needs a
*result* line that is not a table, it gets a new product-level method — `Info` does not move back.

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

Handled for you — do not re-implement it per call site.

`NewPrettyWriter` wraps each stream once, at construction, in a `colorprofile.Writer`. That writer
asks the stream what it can render — truecolor, 256-colour, 16-colour, or nothing at all — and
downsamples or strips the escape sequences to match. `NO_COLOR`, `CLICOLOR`, and `CLICOLOR_FORCE`
are honoured.

The consequence that matters: a run piped into a CI log gets the same text with the escapes
removed, not a wall of `\x1b[`. Styling degrades; it does not have to be switched off by hand.

Detection happens once rather than per line, and the environment is a constructor argument rather
than an ambient read, so the golden tests are the same on a developer's terminal and in CI. Two
consequences worth internalising:

- **Anything written through the writer is safe.** Add a styled renderer later and it inherits the
  detection automatically.
- **Anything written around it is not.** `fmt.Fprintln(os.Stdout, someStyledString)` sends raw ANSI
  into a redirected file. Wrapping the stream — rather than styling call-by-call — is what makes the
  safe path the default one.

### What `IsTTY` is still for

Colour is handled, so it is tempting to conclude the helper has no remaining job. It has two, and
neither is about styling.

Progress UI — spinners, progress bars, the rate-limit countdown — is stderr like every other
diagnostic, and is *additionally* drawn only when the stream is a terminal. This is the standard
shape: npm, cargo, docker, and git all do it. The animation relies on `\r` and cursor moves that are
meaningless in a redirected file, so into a pipe or CI log it degrades to periodic plain lines, or
to nothing at all.

- the **rate-limit countdown** — `\r` on a terminal, periodic log lines into a pipe, because control
  characters in a CI log are unreadable;
- the **prune prompt** — never shown to a pipe. A CI job blocked on an invisible prompt hangs until
  someone cancels it. That is `ErrInteractiveRequired`.

Note which stream each one asks about. The countdown asks about **stderr**, where it draws. The
prompt asks about **stdin**, because the hang it prevents is a read with nobody to answer it — a job
with a terminal attached to stderr and its stdin closed must still refuse to prompt. Two questions,
two streams; check the one the decision actually depends on.

```go
func IsTTY(stream any) bool
```

`IsTTY` therefore takes `any` and type-asserts for `Fd() uintptr`, rather than taking an
`io.Writer`. The writer signature made `IsTTY(os.Stdin)` compile only by accident — `*os.File`
happens to have a `Write` method — and read as though stdin were out of scope. Anything without a
file descriptor, a `bytes.Buffer` in a test included, is not a terminal.

## NDJSON

`JSONWriter` emits exactly one complete JSON object per line. Not one array: an array could only be
closed once the last row was known, and a run killed halfway would leave a truncated document
behind. One object per line means a consumer can parse the stream as it arrives, and whatever was
written before the kill is still valid.

stdout carries the data, one object per row, every line the same shape:

```json
{"group":"websites","repositories":"12","source":"org: specsnl"}
{"group":"platform","repositories":"3","source":"repos:"}
```

stderr carries the narration, every line carrying a `level`:

```json
{"level":"info","message":"resolving 3 groups"}
{"level":"warn","message":"skipping specsnl/old-thing: archived"}
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
diagnostics would leak onto stderr and interleave with the real output, and the two channels stop
being distinguishable.

The trap that follows from that: **`slog.Warn` and `slog.Error` are invisible.** They are not
"quieter", they are gone. If a user needs to know, it is `w.Warn` / `w.Error`. `slog` levels above
Debug exist for the `--debug` reader only.

```go
slog.Debug("read labels", "repo", repo, "count", len(labels), "etag_hit", hit)  // good
slog.Warn("skipping " + repo)                                                    // wrong — user-facing, and silent
w.Warn("skipping %s: archived", repo)                                            // right
```

The level is returned rather than baked in because Cobra parses persistent flags after the command
tree is built: construct the logger once, then `level.Set(slog.LevelDebug)` from `PersistentPreRunE`
once `--debug` is known.

Records follow the output format, so `--debug --output=json` gives a stderr stream that is JSON all
the way down. Stderr and never stdout, for the same reason warnings go there.

## Wiring it in Cobra

The command tree does not exist yet — it lands with `internal/cmd` — but the wiring it has to use is
part of this contract, because getting it wrong is what makes the rest untestable.

### Take the writers from the command, not the process

```go
// in PersistentPreRunE, where the flags are parsed
switch outputFormat {
case string(output.FormatJSON):
    app.Out = output.NewJSONWriter(cmd.OutOrStdout(), cmd.ErrOrStderr())
default:
    app.Out = output.NewPrettyWriter(cmd.OutOrStdout(), cmd.ErrOrStderr(), nil)
}

app.LogLevel = output.SetupLogger(cmd.ErrOrStderr(), format, debug)
```

`cmd.OutOrStdout()` resolves to `os.Stdout` in production, so nothing is lost — but in a test
`cmd.SetOut(buf)` / `cmd.SetErr(buf)` captures everything with no global-state hacking.

`NewDefaultPrettyWriter`, `NewDefaultJSONWriter`, and `SetupDefaultLogger` hardcode `os.Stdout` /
`os.Stderr`. They exist for a pre-flag-parse fallback and for one-off tools. **Do not reach for them
from a command** — that is how output silently escapes a test's buffers.

This applies to `slog` too. Pass `cmd.ErrOrStderr()` to `SetupLogger`, not `os.Stderr`, or `--debug`
output becomes the one thing a command test cannot see.

Read piped input through `cmd.InOrStdin()` for the same reason, so
`cat labels.yml | labelsync ...` is testable the same way as the output side.

### Silence Cobra and handle the error once

```go
root := &cobra.Command{
    SilenceUsage:  true,  // usage on a runtime failure is noise
    SilenceErrors: true,  // main prints it once, so it reliably lands on stderr
}
```

On a returned error, current Cobra already writes to stderr — the error line via `ErrOrStderr()`,
and the usage block via `OutOrStderr()`, which itself *falls back to stderr* when `cmd.SetOut` is
never called. So the stream is not the problem. Two other things are, and both flags earn their
place:

- **`SilenceUsage`** — without it, *every* returned error appends the full usage block after the
  message. Usage helps with a bad flag, not with "repository is inaccessible"; on a runtime failure
  it is noise that buries the one line that matters.
- **`SilenceErrors`** — without it Cobra prints its own bare `Error: <err>` *in addition* to
  whatever `main` prints, so the failure shows up twice and Cobra's copy carries no `error_kind`.
  Owning the single print in `main` is what lets a JSON run put `error_kind` on its final line.

## Exit codes

`internal/util/exit`. The idea is `terraform plan -detailed-exitcode`: without a code that means
"succeeded, and found work", a CI dry-run can only ever pass, which makes it useless as a check.

| Constant       | Code | Bit | Meaning                                                                        |
|----------------|------|-----|--------------------------------------------------------------------------------|
| `exit.OK`      | `0`  | —   | In sync — no changes needed, or every action applied and no repository skipped |
| `exit.Error`   | `1`  | —   | The run failed. Nothing about the live state can be inferred                   |
| `exit.Drift`   | `2`  | `1` | Completed without writing and found pending actions — `sync --dry-run`         |
| `exit.Skipped` | `4`  | `2` | Applied successfully, but one or more repositories could not be reached        |

### Outcomes combine; failure does not

A single run can satisfy more than one outcome — a `--dry-run` that finds pending actions *and*
cannot reach a repository is both drift and skipped. Rather than rank them, the outcome codes are
**disjoint bits and OR together**: that run exits `6`. The next outcome added takes `8`.

```go
code := exit.OK
if dryRun && len(plan.Actions) > 0 {
    code |= exit.Drift
}
if len(result.Skipped) > 0 {
    code |= exit.Skipped
}
```

`exit.Error` is deliberately **not** a bit in that space. It is the classic Unix generic failure and
it is exclusive: when the run failed, the live state is unknown, so "failed *and* drifted" is not a
statement labelsync can honestly make. A failure returns `1` and nothing else. That is also what
keeps every combination meaningful — with `Error` in the mask, `3` would have to mean "failed and
drifted", which is precisely the claim the failure invalidates.

The cost is real and worth naming: `[ $? -eq 2 ]` is no longer how a caller tests for drift once a
second outcome exists. Callers test bits.

```sh
labelsync sync --dry-run; rc=$?
(( rc == 1 ))    && exit 1     # the run itself failed
(( rc & 2 ))     && echo "labels have drifted"
(( rc & 4 ))     && echo "some repositories were unreachable"
```

`if labelsync sync; then` is unaffected — zero still means clean, non-zero still means look closer.

The numbers are a public contract: pipelines branch on them, so bits may be **added**, never
reassigned, and `exit.Error` stays `1` forever. `exit_test.go` spells each value out literally, and
asserts the outcome codes are single non-overlapping bits, so either mistake fails there rather than
in someone's pipeline.

### Getting a code out of a command

Codes travel to `main` on the error, because `RunE` returns an `error` and nothing else. That is
what `exit.Err` is for:

```go
type Err struct {
    Code Code
    Err  error   // nil when the code reports an outcome rather than a failure
}

func (e *Err) Unwrap() error { return e.Err }

// Of returns OK for nil, the carried code for an *Err, and Error for anything else.
func Of(err error) Code
```

A command returns `&exit.Err{Code: code}` when `code != exit.OK`, and a plain wrapped sentinel
otherwise — an ordinary failure needs no carrier, because `Of` already maps it to `exit.Error`. A
carrier with a wrapped failure but no code still reports `Error`: a zero exit on a failed run is the
one wrong answer a pipeline cannot detect.

`Unwrap` matters as much as `Code`. A carrier that hid the error it wraps would strip `error_kind`
from exactly the failures that carry a code.

`main` unwraps once, prints only if there is something to print, and exits:

```go
func main() {
    app := cmd.NewApp()

    err := cmd.ExecuteContext(ctx, app)

    var ex *exit.Err
    if err != nil && !(errors.As(err, &ex) && ex.Err == nil) {
        app.Out.WriteErr(err)
    }

    os.Exit(int(exit.Of(err)))
}
```

A nil `Err` field means silent, and that guard is the whole reason the type exists: exit code `2` on
a drifting dry run must not also print an error line, because the drift *was* the successful result
and the diff has already been rendered on stdout.

Routing the error through `Writer` rather than `fmt.Fprintln(os.Stderr, err)` is what gives JSON runs
an `error_kind` on their final line.

`os.Exit` belongs in `main` and nowhere else. It skips deferred cleanup, so called from inside a
command it leaks temp files, unreleased locks, and unflushed writers. Return the error; let `main`
decide.

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

## Review checklist

- [ ] Would a user want this in the file after `> out.txt`? If not, it is stderr.
- [ ] No `fmt.Println` / `fmt.Printf` outside tests.
- [ ] Writers and the logger come from `cmd.OutOrStdout()` / `cmd.ErrOrStderr()`, not `os.*`.
- [ ] Nothing user-facing goes through `slog`; nothing debug-only goes through `Writer`.
- [ ] `WriteErr(err)` rather than `Error("%v", err)` whenever an `error` is in hand.
- [ ] No `os.Exit` outside `main`.
- [ ] Nothing styled is written around the writer.
- [ ] Any new prompt is guarded by a TTY check **on stdin**.
- [ ] Outcome codes are OR'd, never assigned over each other; `exit.Error` is not OR'd with anything.
- [ ] A non-zero code that is not a failure returns `&exit.Err{Code: ...}` with no wrapped error, so
      nothing is printed.

## Pitfalls, with receipts

Real defects in `specs-cli`, kept here because they are the failure modes these rules exist to
prevent, not hypotheticals.

**Styling written around the wrapped writer.** `HumanWriter.Table` uses `fmt.Fprintln` on the raw
stream while `RenderTable` applies `Bold` and `ANSIColor(240)`. Escape codes land in redirected files
and `NO_COLOR` is ignored — the sibling methods are safe only because they happen to use
`lipgloss.Fprintln`. Wrapping the stream once removes the chance to get this wrong. See
[specsnl/specs-cli#109](https://github.com/specsnl/specs-cli/issues/109) for a related `Table` issue.

**A default log level that depends on nobody using it.** `specs-cli` defaults to `slog.LevelInfo`, so
any `slog.Info` anywhere prints on an ordinary run. Nothing leaks today only because the codebase
happens to log at Debug — an invariant nobody wrote down. `LevelSilent` makes the guarantee
structural.

**A handler chosen by the wrong condition.** `specs-cli` installs the JSON slog handler only when
`--debug` **and** `--output=json` are both set, so `--output=json` alone leaves text records on
stderr. Handler format should follow the output format, independently of verbosity.

**`slog` pointed at `os.Stderr` instead of the command's writer.** `cmd.SetErr(buf)` then cannot
capture `--debug` output, which is precisely the testability the accessors exist to provide.
