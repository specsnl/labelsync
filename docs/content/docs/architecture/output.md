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

| You want to…                                  | Use                          | Lands on |
|-----------------------------------------------|------------------------------|----------|
| Emit the result the command exists to produce | `output.Table(w, rows, ...)` | stdout   |
| Emit a result that is a single value          | `w.WriteResult(rec, ...)`    | stdout   |
| Emit a rendered plan                          | `plan.Render(w, p)`          | stdout   |
| Tell the user what the code is doing          | `w.Info(...)`                | stderr   |
| Report a recoverable problem (skipped repo)   | `w.Warn(...)`                | stderr   |
| Report a failure                              | `w.Error(...)`               | stderr   |
| Report a failure carrying a sentinel          | `w.WriteErr(err)`            | stderr   |
| Tell a maintainer what the code is doing      | `slog.Debug(...)`            | stderr   |

`WriteErr` over `Error("%v", err)` whenever you hold an `error`: it runs `labelsync.KindOf` and adds
`error_kind` to the JSON object. `Error` with a formatted string cannot.

## `output.Writer`

```go
type Writer interface {
    Info(format string, args ...any)                    // stderr
    Warn(format string, args ...any)                    // stderr
    Error(format string, args ...any)                   // stderr
    WriteErr(err error)                                 // stderr
    WriteTable(t TableData)                             // stdout
    WriteDiff(d DiffData)                               // stdout
    WriteResult(record any, format string, args ...any) // stdout
}
```

`WriteTable`, `WriteDiff`, and `WriteResult` are the only methods on stdout, and the asymmetry is the
point:
**`Writer`'s product channel is small and closed, and everything else narrates.** `Info` is
progress — "resolving 3 groups", "applying 12 actions" — and progress is not what `> out.txt` is
for.

In JSON mode it is not only a stream-choice question. Narration on stdout would interleave objects
that have none of the fields the data objects have:

```json
{"level":"info","message":"resolving 3 groups"}
{"group":"websites","repositories":12,"source":"org: specsnl"}
```

Valid NDJSON, and `jq -r .group` still yields a `null` for the first line. With `Info` on stderr the
stdout stream is uniformly typed and `jq` never sees a record it cannot type — which
`TestJSONWriter_StdoutIsUniformlyTyped` asserts directly, by requiring every stdout line to carry
the data key and no `level`.

The house CLI this one's conventions are borrowed from puts `Info` on stdout, and has commands
depending on that. labelsync diverges deliberately: nothing here called `Info` when the decision was
made, so there was no contract to break, and inheriting the defect to stay symmetrical is the wrong
trade. A command that needs a
*result* which is not a table gets a new product-level method instead — that is
[`WriteResult`](#a-result-that-is-not-a-table) and [`WriteDiff`](#a-diff-is-neither-a-table-nor-a-value),
and `Info` does not move back.

Two implementations back the `--output` flag:

| Implementation | `--output` | Rendering                                           |
|----------------|------------|-----------------------------------------------------|
| `PrettyWriter` | `pretty`   | lipgloss-styled text, colour-degraded to the stream |
| `JSONWriter`   | `json`     | NDJSON — one self-contained object per line         |

Both take their streams as constructor arguments, so a test captures output by passing
`bytes.Buffer`s rather than by intercepting the process.

### Tables are typed rows, not strings

Commands do not call `WriteTable` directly. They call `output.Table`, which takes the rows as they
already exist and a description of how to display them:

```go
type GroupRow struct {
    Name         string `json:"group"`
    Repositories int    `json:"repositories"`
    Source       string `json:"source"`
}

output.Table(app.Out, groups,
    output.Col("Group",        func(g GroupRow) string { return g.Name }),
    output.Col("Repositories", func(g GroupRow) string { return strconv.Itoa(g.Repositories) }),
    output.Col("Source",       func(g GroupRow) string { return g.Source }),
)
```

**The two audiences get different projections of the same row.** The pretty table comes from the
columns. The JSON comes from marshalling the row itself, so its keys and its types are the struct's
own — `{"repositories":12}`, a number a consumer can filter on, not the `"12"` a shared
string-table would have forced. Those `json` tags are a public contract in the same way
`error_kind` is: added to, never renamed.

That split is also what lets a column be formatted or computed without the record paying for it. A
size is an `int64` in the record and `1.2 MiB` in the table; a timestamp is RFC 3339 in the record
and `3 days ago` in the table. Under a shared `[][]string` the choice was to have one or the other.

`Table` is a generic function rather than a method because Go does not allow type parameters on
methods. It prepares a `TableData` — headers, cells, and the source records — and that constructor
is what guarantees every row has exactly one cell per header. The old
`Table(headers []string, rows [][]string)` could not: a ragged or reordered row was a runtime
surprise, and the writer had to defend against short rows on every call.

### A result that is not a table

`version` has an answer, and it is not a table. Its answer is a value a user pipes —
`v=$(labelsync version --dont-prettify)` — so it cannot be `Info` on stderr, and boxing a single
string in a bordered table to reach stdout would be pretending it is something it is not.

`WriteResult` is the product-level line the earlier note said this case would get. It takes both
projections at once, and each audience gets only its own:

```go
app.Out.WriteResult(versionRow{Version: Version}, "labelsync version %s", Version)
```

| `--output`                   | stdout                    |
|------------------------------|---------------------------|
| `pretty`                     | `labelsync version 1.2.3` |
| `pretty` + `--dont-prettify` | `1.2.3`                   |
| `json`                       | `{"version":"1.2.3"}`     |

The pretty rendering comes from the format string; the JSON comes from marshalling the record, the
same split `WriteTable` makes. That is what keeps the NDJSON invariant intact — every line on stdout
is still one typed object, never a sentence — and what makes `--dont-prettify` a choice of *phrasing*
rather than a second output path: JSON has no prose in it to strip, so the flag has no effect there.

**This is not a general-purpose print.** Reach for it only when the command's whole answer is one
value and a user piping stdout would expect exactly that value in the file. Rows go through
`output.Table`; anything narrating the work is `Info`.

### A diff is neither a table nor a value

A rendered plan is the product of `sync --dry-run` — exactly what `> plan.txt` is for — but it is
grouped under repository headings, its rows are ragged, and it ends in a summary line. `Table` cannot
express it and `WriteResult` is for a single value, so it gets the third product-level method:

```go
type DiffData struct {
    Text    string // the assembled pretty rendering, no trailing newline
    Records []any  // one object per NDJSON line, in order
}

w.WriteDiff(d)
```

The same split as everywhere else: pretty writes `Text`, JSON marshals `Records` one per line and
ignores the text entirely, so no line of stdout is ever prose.

**Do not build a `DiffData` by hand.** `output` deliberately knows nothing about actions —
`plan.Render(w, p)` owns the vocabulary and prepares both projections. See
[Planner § Rendering](./plan.md#rendering).

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
{"group":"websites","repositories":12,"source":"org: specsnl"}
{"group":"platform","repositories":3,"source":"repos:"}
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

Row keys come from the row struct's `json` tags — see
[Tables are typed rows](#tables-are-typed-rows-not-strings). Nothing derives them from a column
heading, so rewording a heading cannot disturb a `jq` filter and there is no normalisation step to
reason about.

HTML escaping is off. Label names legitimately contain `&` and `<`, and rendering them as `&amp;`
helps nobody reading a log.

## Table rendering

Two renderers, because the diff and the list commands want different things:

| Function        | Shape                                 | Used by                                            |
|-----------------|---------------------------------------|----------------------------------------------------|
| `RenderColumns` | Aligned columns, no header, no border | The pretty diff, via `plan.Render`                 |
| `RenderTable`   | Bordered table with a header row      | `PrettyWriter.WriteTable` — `groups`, `cache info` |

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

The command tree lives in `internal/cmd`, and this is the wiring it uses. Getting it wrong is what
makes the rest untestable, so it is part of this contract rather than that package's private
business.

### Take the writers from the command, not the process

```go
// internal/cmd/root.go — App.resolveFlags, called from PersistentPreRunE
if a.Format == output.FormatJSON {
    a.Out = output.NewJSONWriter(cmd.OutOrStdout(), cmd.ErrOrStderr())
} else {
    a.Out = output.NewPrettyWriter(cmd.OutOrStdout(), cmd.ErrOrStderr(), nil)
}

a.LogLevel = output.SetupLogger(cmd.ErrOrStderr(), a.Format, a.Debug)
```

An unrecognised `--output` is wired as `pretty` *before* it is rejected: the rejection has to have
somewhere to go, and a writer that does not exist yet cannot report that it could not be built.

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

`TestRoot_SilencesUsageAndErrors` asserts both, and asserts the consequence too: after a failing
command, neither stream carries a usage block or Cobra's own copy of the message.

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

    err := cmd.Execute(app)

    report(app.Out, err)

    os.Exit(int(exit.Of(err)))
}

// report prints err, unless there is nothing to print.
func report(w output.Writer, err error) {
    if err == nil {
        return
    }

    var carrier *exit.Err
    if errors.As(err, &carrier) && carrier.Err == nil {
        return
    }

    w.WriteErr(err)
}
```

The guard is a function rather than four lines inline for one reason: `os.Exit` cannot be tested,
and everything above it can. `main_test.go` drives `report` over each case — nil, a plain failure, a
carrier holding a failure, a silent carrier — and asserts what lands on stderr and what does not.

Note which field the silence follows. It is `Err`, not `Code`: a carrier with `Code: exit.Skipped`
*and* a wrapped failure still prints, because the failure is real. Only a nil `Err` means the
non-zero code was the answer.

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
task test:update
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
- [ ] A single-value result goes through `WriteResult` with a tagged record — not `Info`, and not a
      one-row table.
- [ ] A rendered plan goes through `plan.Render`, not a hand-built `DiffData`.
- [ ] Tables go through `output.Table` with a row struct whose `json` tags carry the machine
      contract — never a hand-built `TableData`, and never a struct field pre-formatted into a
      string just to make the table read well.
- [ ] Any new prompt is guarded by a TTY check **on stdin**.
- [ ] Outcome codes are OR'd, never assigned over each other; `exit.Error` is not OR'd with anything.
- [ ] A non-zero code that is not a failure returns `&exit.Err{Code: ...}` with no wrapped error, so
      nothing is printed.

## Pitfalls, with receipts

Real defects, observed in a sibling CLI sharing these conventions and kept here because they are the
failure modes these rules exist to prevent, not hypotheticals.

**Styling written around the wrapped writer.** A human writer's `Table` used `fmt.Fprintln` on the
raw stream while its `RenderTable` applied `Bold` and `ANSIColor(240)`. Escape codes land in
redirected files and `NO_COLOR` is ignored — the sibling methods were safe only because they
happened to use `lipgloss.Fprintln`. Wrapping the stream once removes the chance to get this wrong.

**A default log level that depends on nobody using it.** Defaulting to `slog.LevelInfo` means any
`slog.Info` anywhere prints on an ordinary run. Nothing leaks only because the codebase happens to
log at Debug — an invariant nobody wrote down. `LevelSilent` makes the guarantee structural.

**A handler chosen by the wrong condition.** Installing the JSON slog handler only when `--debug`
**and** `--output=json` are both set leaves `--output=json` alone emitting text records on stderr.
Handler format should follow the output format, independently of verbosity.

**`slog` pointed at `os.Stderr` instead of the command's writer.** `cmd.SetErr(buf)` then cannot
capture `--debug` output, which is precisely the testability the accessors exist to provide.
