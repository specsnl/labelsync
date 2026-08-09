---
title: GitHub Client
weight: 9
---

`internal/github/client.go` wraps [go-github](https://github.com/google/go-github) and classifies
its errors **once**, so that no caller anywhere else in the tree sniffs a status code. Credential
resolution is the other half of the package: [Authentication](./authentication.md).

## Why go-github

The deciding reason is typed `*github.RateLimitError` and `*github.AbuseRateLimitError`. Secondary
rate limits are this tool's most likely failure mode — hundreds of sequential writes against a
roughly 80/minute ceiling — and a distinguishable error *type* is what makes the backoff logic
clean and testable rather than a status-code guess. `resp.NextPage` for enumeration is a bonus.

## Construction

```go
client, err := github.New(token,
    github.WithBaseURL(server.URL),   // net/http/httptest, not GitHub Enterprise
    github.WithRetries(3),
    github.WithBackoff(500*time.Millisecond),
    github.WithSleep(fakeSleep),      // tests only
)
```

`WithBaseURL` exists so the suite can run against `net/http/httptest`. GitHub Enterprise Server is
a non-goal, which is why the host is otherwise a constant. A trailing slash is added if absent —
go-github resolves every request path relative to `BaseURL` and silently loses the last segment
without one.

An empty token is rejected at construction with `no_token`. `Resolve` never returns one, so an
empty token means a caller built a `Token` itself and skipped the chain; failing now beats a `401`
against every repository in the set.

## The taxonomy

`Classify` is the only function in `labelsync` that looks at an HTTP status, and `Client.Do` is the
only thing that calls it. Everything downstream reasons about `ErrRepoInaccessible` and
`IsAlreadyExists` instead.

| What came back                       | Classified as                                  | The run                  |
|--------------------------------------|------------------------------------------------|--------------------------|
| `403` archived, or no permission     | `RepoError` → `repo_inaccessible`              | continues, repo skipped  |
| `404` renamed, deleted, or invisible | `RepoError` → `repo_inaccessible`              | continues, repo skipped  |
| `410` gone                           | `RepoError` → `repo_inaccessible`              | continues, repo skipped  |
| `422` with `already_exists`          | `IsAlreadyExists` — a create becomes an update | continues                |
| Rate limit, primary or secondary     | passed through, typed                          | waits — see `ratelimit/` |
| Cancelled context                    | passed through                                 | stops                    |
| Anything else (`401`, `5xx`, …)      | passed through                                 | fails                    |

`RepoError` wraps `ErrRepoInaccessible`, so `errors.Is` matches it and `KindOf` renders
`repo_inaccessible` through the struct, while its fields carry what a summary line needs.

`410` is in the taxonomy for robustness rather than because it is expected: the
[GH-17 spike](https://github.com/specsnl/labelsync/issues/17) verified that repository-scoped label
endpoints are ungated on `has_issues`, so a repository with issues disabled syncs normally and does
not produce one. It costs a `case` to handle and would otherwise abort a run over a status nobody
predicted.

### Rate limits are checked before the taxonomy

A rate limit also arrives as a `403`, and it is emphatically not a repository that cannot be
reached — it is the whole run needing to wait. Mistaking one for the other would skip every
remaining repository and report success, which is the single worst outcome this code can produce.

The typed errors are checked first. Behind them sit two header checks that are **not** redundant:
go-github only produces an `AbuseRateLimitError` when the response body's `documentation_url`
carries the right suffix, and GitHub's error bodies are famously inconsistent. A `403` or `429`
carrying `Retry-After`, or a `403` with `X-RateLimit-Remaining: 0`, is a rate limit whatever the
body says.

### `422 already_exists` is an update, not a failure

Two perfectly ordinary situations produce it, and in both the desired end state is a label with the
configured values — which an update reaches:

- A plan computed against state that has since changed, including two runs overlapping.
- **Case-only drift.** A repository holding `bug` rejects the creation of `Bug`, because GitHub
  holds label names case-insensitively unique.

## Per-repository failures are non-fatal

`Client.Do` **records a per-repository failure before returning it**, so a caller that means to
continue does not have to remember to collect anything:

```go
if err := c.Do(ctx, repo, "list labels", call); err != nil {
    if errors.Is(err, labelsync.ErrRepoInaccessible) {
        continue // already collected; the summary will name it
    }
    return err // a real failure: the run stops
}
```

Recording inside `Do` rather than at the call site is deliberate. A skipped repository that never
reaches the summary is indistinguishable from one that synced cleanly, and that is the one mistake
here a user cannot detect.

`Failures` is safe for concurrent use, because reads run in parallel. `Failures.All` returns its
items **sorted** by repository and then operation — arrival order is whatever the parallel reads
happened to finish in, and a summary that reshuffles between two identical runs is not one anyone
can diff.

The end-of-run summary goes to **stderr**, through `Warn`. A skipped repository is the story of the
run, not its product: `labelsync groups --output=json | jq` has to keep working when three
repositories turn out to be archived.

`Failures.ExitCode` is `exit.Skipped` — code **`4`** — when anything was skipped. It is an outcome
*bit*, so the caller ORs it with whatever else the run concluded and a dry run that both drifted and
skipped exits `6`. See [Output & Exit Codes](./output.md#exit-codes).

## Retrying `5xx`

A retry wrapper sits under go-github's auth transport, so a retried request carries the
`Authorization` header the first one did. It is a `RoundTripper` rather than a loop at each call
site, so every request gets it — including the ones go-github issues itself for pagination.

- **Three retries** after the first attempt, so four requests at most.
- **Exponential backoff**: 500ms, then 1s, then 2s.
- **`5xx` only.** Repeating a `404` three times spends three requests of quota to be told the same
  thing.
- Label writes are idempotent enough that a retry is always safe: creating a label that now exists
  returns the `422` above, and applying the same `PATCH` twice is the same label.

A transport must not modify the request it is given, so each attempt goes out on a **clone with a
freshly rewound body** — a retried `POST` arriving with an empty body would create a label with no
name. A request whose body cannot be rewound is not retried at all; replaying half a body is worse
than surfacing the `5xx`. Each discarded response is drained and closed, or the connection is not
reused and a retry storm opens a new one every time.

The backoff sleep is injectable, which is what keeps the retry suite instant and makes the doubling
assertable rather than merely plausible. A cancelled context ends the wait immediately: a stopped
run must not sit out a backoff whose result nothing will use.
