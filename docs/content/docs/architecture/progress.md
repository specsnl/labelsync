---
title: Progress
weight: 11
---

`internal/util/progress` draws the status line a long run shows while it works. It grew out of
[issue #132](https://github.com/specsnl/labelsync/issues/132): against a real organisation, `sync`
spends most of its time in three phases that print nothing of their own, and each of them looked like
a hang.

## The phases

| Phase       | Who reports it                    | Commands         | The line                                                                  |
|-------------|-----------------------------------|------------------|---------------------------------------------------------------------------|
| Enumeration | `github.Client.Select`            | `sync`, `groups` | `enumerating… 2/4 groups · 230 repositories`                              |
| Label reads | `github.Client.ReadLabels`        | `sync`           | `reading labels… 23/65 repositories`                                      |
| Apply       | `apply.Apply`, via `WithProgress` | `sync`           | `applying… 12/65 repositories · 48/179 writes · specsnl/specsops-ansible` |

The enumeration line counts selectors rather than repositories, because nobody knows how many
repositories there are until the last page of the last listing is in. The repository count beside it
is a running sum of what the selectors found, before the union removes repositories two groups share,
so it can briefly read higher than the count the next phase starts with.

The read side is reported from inside `internal/github`, not from `runSync`. `groups` calls `Select`
and `sync` reaches it through `Enumerate`, so one hook covers both commands. `internal/plan` and
`internal/palette` know nothing about progress, and `internal/apply` only hears about it through an
option, the same way it hears about the client: as an interface it is handed.

Every phase ends with `Done`, deferred, so the line is gone whether the phase finished or failed.
Whatever is printed next, whether the plan, the table, the `applied` record or an error, starts on a
clean row.

## One rendering, or none

| Context                             | Rendering                                   |
|-------------------------------------|---------------------------------------------|
| stderr is a TTY + `--output=pretty` | One line, rewritten in place                |
| stderr is not a TTY                 | Nothing                                     |
| `--output=json`                     | Nothing: no events, the stream is unchanged |
| `--debug`                           | Nothing                                     |

Progress draws on **stderr**, so stdout is byte-for-byte what it would have been without it, and a
dry run's stdout still matches the stdout of the apply that follows it.

A log or a JSON stream gets nothing rather than a periodic line. The end of every phase is already in
the output, as the plan, the table or the `applied` record, and a CI log gains nothing from a count
it cannot watch move. The [rate-limit countdown]({{< ref "./rate-limiting.md#the-countdown" >}}) is
the exception and keeps reporting there, because a wait is the one silence that can last for minutes.
`--debug` turns progress off because the debug log writes whole lines to the same stream, and every
one of them would land in the middle of the status line.

The in-place line is a carriage return, `ESC[K` (erase to the end of the line), and the text. Erasing
rather than padding means a shorter line never leaves the tail of a longer one behind. Each line is
cut to one column short of the terminal's width, which is asked before every redraw: a line that
wraps puts the cursor on a second row, and the next carriage return would only go back to the start
of that one.

## Sharing the row

Two other things draw on the terminal while a phase is running.

- **The rate-limit countdown** draws on the same row. `cmd.pausing` wraps the countdown's reporter:
  `Start` pauses the progress line before the countdown draws, and `Done` resumes it after the
  countdown has cleared itself. Pauses nest, because reads run in parallel and two of them can wait
  at once. A phase that advances during a wait updates the text without drawing it, so the line comes
  back with the latest count rather than the one it had when the wait began.
- **The prune prompt** draws a whole form. No phase is running when it does: the read phase has ended,
  and taken its line down, before the plan is rendered, and the apply phase starts only after
  `selectRemovals` returns.

## Wiring

`App.Progress` holds the reporter. Left nil, `App.progress()` picks one with `progress.New` on first
use, from `--output`, `--debug` and whether `App.Stderr` is a terminal. The client, the countdown
wrapper and the apply then all share that one instance. It is also the test seam: a terminal file
descriptor is not something a test can fake portably, so the end-to-end tests hand in a
`progresstest.Recorder` and assert the order of what it was told, and `progress.NewLive` is driven
directly over a buffer to pin the rendering.
