---
title: Rate Limiting
weight: 10
---

`internal/github/ratelimit` keeps a run inside GitHub's limits, proactively and reactively. It is
wired into the client as a transport and as a retry loop around `Client.Do`; the client itself is
[GitHub Client](./github-client.md).

## Writes are the problem, not reads

Reads are already cheap — roughly 51 requests for 50 repositories against 5,000 an hour, and a
[cached read](./github-client.md#the-etag-cache) costs nothing at all. **Writes are what need
managing**: a few hundred label operations against a content-creation ceiling of roughly 80 a minute,
and that ceiling is an undocumented, body-shaped *secondary* limit rather than a header-shaped
primary one.

## Proactive

| Mechanism       | What it does                                                                |
|-----------------|-----------------------------------------------------------------------------|
| Token bucket    | Paces **writes** at `--write-rate` a minute, default 70 — under the ceiling |
| Header tracking | Reads `x-ratelimit-remaining` / `x-ratelimit-reset` off **every** response  |
| Threshold pause | Sleeps until the reset when the budget drops to the last 20 requests        |
| Startup reading | `GET /rate_limit` is free, so a run starts informed rather than guessing    |

The bucket starts **full**: a run's first writes should go out immediately, and pacing is about the
sustained rate rather than the first request. It refills at the configured rate and is capped at a
minute's worth, so an idle run cannot bank an unlimited burst.

The threshold is not zero. A run that spends its last request discovering it has none left has
already lost, and the margin absorbs the requests that were in flight when the reading was taken. A
budget that has never been read never causes a wait: on the first request of a run, "nothing has been
observed yet" and "nothing remains" are the same zero, and treating them alike would stall every run
at its first request.

### The bucket lives in the transport

The write/read distinction is made from the **HTTP method**, in a `RoundTripper`, because a call site
can label itself wrong and a `POST` cannot. It also catches the requests nothing in this tree issues
explicitly — the ones go-github makes on its own for pagination.

It sits *under* the [5xx retry wrapper](./github-client.md#retrying-5xx), closest to the network, so
a retried attempt is paced and observed like any other request rather than slipping past the bucket
because the first attempt already paid for it.

## Reactive

Typed errors are the reason go-github was chosen, and this is what they are for:

| Error                         | Wait                                                               |
|-------------------------------|--------------------------------------------------------------------|
| `*github.RateLimitError`      | Until `Rate.Reset` — the primary limit                             |
| `*github.AbuseRateLimitError` | `Retry-After` when present — the secondary limit                   |
| `*github.AbuseRateLimitError` | Otherwise a minute, doubling per consecutive hit, jittered, to 15m |
| Anything else                 | Not ours: returned untouched                                       |

A rate limit is **not an outcome**. It is the API asking for the same request later, so `Client.Do`
waits it out and retries, and a caller never sees one. A repository that was rate-limited is not a
repository that could not be reached, and recording it as skipped would be a lie about one that
synced.

The backoff is jittered by ±20% so that several runs that hit the same secondary limit do not resume
in lockstep and trip it again. The jitter function is injectable, which is what makes the doubling
assertable in a test rather than merely plausible. An explicit `Retry-After` resets the escalation:
a limit that said how long to wait is not the case the doubling exists for.

### go-github's own limiter is turned off

go-github remembers a limit it has seen and refuses later requests **without issuing them**, against
the real clock. That is a sensible default and the wrong one here: this tool waits limits out itself
through an injected clock, and a second opinion that cannot be told time has passed turns a retry
into a refusal — under test, into a run that spends its whole `--max-wait` without making a single
request. `DisableRateLimitCheck` is set for that reason. Waiting is the limiter's job, and it has to
be the only thing doing it.

## `--max-wait` is refused, not taken

`--max-wait` (default 15m) caps the **total** time a run may spend asleep for limits. Exceeding it
fails with `max_wait_exceeded`, and the message says how long was asked for, what the ceiling is, and
how much of it has already gone — the answer to *raise it to what?* has to be in it.

The refusal arrives **instead of** the wait. A CI job that idles for an hour and then fails has
wasted both the hour and the reason.

Pacing is deliberately **not** counted against the ceiling. That budget is for sitting out somebody
else's limit; the bucket is this tool spacing its own requests, it is sub-second, and counting it
would quietly turn `--max-wait` into a cap on how much work a run may do.

## The countdown

Waiting is expected and fine. Waiting **silently** is not — a CLI that goes quiet for four minutes
reads as one that has hung — and neither is waiting while spraying carriage returns into a log file,
which is how a CI job's output becomes unreadable. Getting this wrong is how CLIs become unusable in
CI, so there are three renderings and the context picks one:

| Context                             | Rendering                                                      |
|-------------------------------------|----------------------------------------------------------------|
| stderr is a TTY + `--output=pretty` | One line, rewritten in place with `\r`, lipgloss-styled        |
| stderr is not a TTY                 | A log line every 30s — **no control characters** in a log file |
| `--output=json`                     | A structured event every 30s, no animation                     |

```text
⏳ Secondary rate limit — resuming in 04:32 · 143 writes remaining
```

```json
{"level":"warn","event":"rate_limit_wait","kind":"secondary","seconds":272,"resume_at":"2026-07-31T14:22:10Z","writes_remaining":143}
```

All three draw on **stderr**. The countdown is progress, not the product: it must not land in the
file when a user redirects stdout, and it has to stay visible on the terminal while they do. That is
also why `output.IsTTY` is asked about **stderr** — the stream being drawn to — rather than about
stdout, which by then is somewhere else entirely.

The terminal rendering redraws every second, because a countdown that does not count is just a
message; the other two every thirty, because one line a second for fifteen minutes is nine hundred
lines nobody will read. The in-place line pads itself to the longest it has been, so shortening from
`10:00` to `9:59` does not leave a stale digit behind, and clears itself when the wait ends.

Two of the three renderings are **one implementation**. The difference between a log line and a
structured event is `output.Writer`'s business, not the countdown's, which is what
`Writer.WriteEvent` is for: pretty output writes the sentence, JSON output marshals the record. Only
the in-place rendering takes the raw `stderr`, because a carriage return with no newline after it is
a thing no `Writer` method can express and should not learn to.

### Why it reports a write count

"Resuming in 04:32" is a delay. "Resuming in 04:32 · 143 writes remaining" is progress — sitting out
four minutes is a different proposition when three writes are left and when three hundred are. The
command tells the limiter its plan's write count with `ExpectWrites`, and the bucket counts it down
as tokens are spent. A run that never set one — a dry run — says nothing about writes rather than
reporting a zero that would read as a finished job.

The number is an estimate in one direction: a write retried after a `5xx` or a rate limit spends
another token and counts again, so it overstates what is left rather than reaching zero and carrying
on.

### The loop is the limiter's, and it is conditional

The reporter never reads a clock. The limiter owns the sleeping and calls `Tick` every
`Reporter.Interval` with what is left, measured against a deadline computed once — so the injected
clock drives the whole animation and a four-minute countdown renders in a test in no time at all.

Without a reporter, a wait is a **single** `Sleep` of its whole length rather than a loop of slices.
That is not only an optimisation: a run nobody is watching should not wake up sixty times to say
nothing, and every existing caller is entitled to assume a wait is a wait rather than a sequence of
them.

## Nothing sleeps for real under test

Every wait goes through an injected `Clock`. A rate-limit suite that waits out its own backoffs is a
suite nobody runs, and one that asserts on wall-clock time is one that fails on a loaded machine. The
fake clock records what it was asked to wait for and *advances*, which is what makes the token
bucket's refill observable at all.

## Where the decisions are not

`Limiter.Affordable` answers whether a number of writes fits in what is known to be left, and an
unknown budget is affordable — refusing on no information would stop a run that would have succeeded.
Turning that answer into a refusal is `sync`'s, because whether a half-finished run is worse than none
at all is a policy question rather than a rate-limiting one — see
[Apply § The startup budget check](./apply.md#the-startup-budget-check).
