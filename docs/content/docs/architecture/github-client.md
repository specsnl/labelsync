---
title: GitHub Client
weight: 9
---

`internal/github/client.go` wraps [go-github](https://github.com/google/go-github) and classifies
its errors **once**, so that no caller anywhere else in the tree sniffs a status code. Credential
resolution is the other half of the package: [Authentication]({{< ref "./authentication.md" >}}).

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

| What came back                       | Classified as                                  | The run                                                       |
|--------------------------------------|------------------------------------------------|---------------------------------------------------------------|
| `403` archived, or no permission     | `RepoError` → `repo_inaccessible`              | continues, repo skipped                                       |
| `404` renamed, deleted, or invisible | `RepoError` → `repo_inaccessible`              | continues, repo skipped                                       |
| `410` gone                           | `RepoError` → `repo_inaccessible`              | continues, repo skipped                                       |
| `422` with `already_exists`          | `IsAlreadyExists` — a create becomes an update | continues                                                     |
| Rate limit, primary or secondary     | passed through, typed                          | waits — see [Rate Limiting]({{< ref "./rate-limiting.md" >}}) |
| Cancelled context                    | passed through                                 | stops                                                         |
| Anything else (`401`, `5xx`, …)      | passed through                                 | fails                                                         |

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

## Repository enumeration

`internal/github/repos.go` turns the selectors [`config.Resolve`]({{< ref "./configuration.md#resolution" >}})
produced into concrete repositories:

```go
selections, err := client.Select(ctx, resolution.Selectors(), concurrency)   // per selector
repos, err := client.Enumerate(ctx, resolution.Selectors(), concurrency)     // their union
func Union(selections []Selection) []config.Repo
```

`Select` is the primitive and `Enumerate` is `Union(Select(...))`. A run only ever wants the union;
`labelsync groups` wants the per-selector answer, and having the two share one walk is what stops
the command that explains enumeration from enumerating differently to the command that uses it.

`Select` returns one `Selection` per selector, **in the caller's order** rather than in whatever
order the parallel walks finished, because that order is the group order a report reads down.

The union is deduplicated, sorted by owner and then name — a map is not ordered, and every
downstream artefact is compared between runs. Deduplication is case-insensitive on `owner/repo`:
two groups selecting the same repository is ordinary, and is how one ends up with the labels of
both. The **first** spelling seen wins, so which of `Owner/Repo` and `owner/repo` survives is
decided by selector order rather than by a race.

| Selector kind           | Endpoint                            | Notes                          |
|-------------------------|-------------------------------------|--------------------------------|
| `org`                   | `GET /orgs/{org}/repos`             | 100/page                       |
| `user`, the token's own | `GET /user/repos?affiliation=owner` | The only one that sees private |
| `user`, anybody else    | `GET /users/{user}/repos`           | Public only                    |
| `repos`                 | none                                | Passed through unenumerated    |

Which of the two `user` endpoints applies was decided in `config.Resolve` from the authenticated
login, so this package does not ask who the token belongs to a second time. The authenticated
request carries `affiliation=owner` and nothing else — GitHub rejects a request carrying both
`affiliation` and `type`.

### Filtering is free and stays free

A repository listing already carries `archived`, `fork`, `private` and `has_issues` on **every
entry**, so every filter is answered from the enumeration response. Nothing here issues a
`GET /repos/{owner}/{repo}` to check an attribute: that would turn one request per hundred
repositories into one per repository, for information already in hand. A test asserts no such
request is made, because the cost only shows up as a slow run against a large org.

The filters themselves live in `config.Selector.Reject`, offline and testable without an HTTP mock.
This file's job is to produce the `config.Repo` values it judges — including `HasIssues`, which is
carried through and **never** filtered on. Repositories with issues disabled sync normally; the diff
merely notes them, and excluding one stays the user's choice through the group filters.

Enumeration is the only place `HasIssues` is ever known, which is why it is a `*bool`. An explicit
`repos:` entry is passed through unenumerated, so its flag stays `nil` — *not known* — rather than
defaulting to a note about a repository nothing looked at.

### What was filtered out is kept, not dropped

```go
type Selection struct {
    Selector config.Selector
    Repos    []config.Repo   // selected
    Rejected []Rejected      // listed, then filtered out, with the reason
}
```

A repository the listing returned and a filter removed is carried on the `Selection` rather than
discarded, because the absence of an expected repository is exactly what `labelsync groups` exists
to explain. Nothing else reads it: enumeration's answer is `Repos`, and the union ignores
`Rejected` entirely.

The reason comes from `config.Selector.Reject`, which is `Matches` with its reasoning — `""` when
the repository belongs, and otherwise which filter removed it. They are one function rather than
two so that the reason a repository was filtered out cannot disagree with whether it was.

A `repos:` selector never populates `Rejected`. Nothing is enumerated for one, so nothing can be
filtered out of it — a repository the config named outright is never dropped from under the user.

### Who the token belongs to

```go
func (c *Client) Login(ctx context.Context) (string, error)
```

`GET /user`, cached for the life of the client. It exists for one caller:
[`config.Resolve`]({{< ref "./configuration.md#the-user-split" >}}) needs the authenticated login to decide which
of the two `user` endpoints a selector calls.

A command asks for it **only when the config actually has a `user:` group**, because it costs a
request and decides nothing for a config without one. A failure is not fatal either: a token that
cannot read `/user` — a GitHub App installation token, most likely — lists organisations perfectly
well, and `Resolve` reads an empty login as "somebody else", which is the conservative answer.

### Parallel walks, and what a failure means

Selectors are walked in parallel through `errgroup`, bounded by `--concurrency` (default 8). Reads
are not subject to the content-creation secondary limit, so the bound is round-trip latency and
politeness rather than a quota concern.

An owner that cannot be listed is recorded and skipped, and its selection comes back empty rather
than missing: one mistyped org in a config naming four reports itself in the end-of-run summary
rather than taking the other three down with it. Anything
else — a `401`, a cancelled context — ends the run, because continuing would report a successful run
that synced nothing.

## Label operations

`internal/github/labels.go` holds the four operations and nothing else. Every one goes through
`Client.Do`, so an inaccessible repository is recorded and skipped rather than ending the run.

```go
labels, err := client.ListLabels(ctx, owner, repo)
err = client.CreateLabel(ctx, owner, repo, github.Label{Name: "bug", Color: "d73a4a"})
err = client.UpdateLabel(ctx, owner, repo, "bug", github.Label{Name: "defect", Color: "d73a4a"})
err = client.DeleteLabel(ctx, owner, repo, "wontfix")
```

`ListLabels` walks every page at 100 per page. More than 100 labels in one repository is rare, but a
truncated list does not read as "truncated" — it reads as "these labels do not exist", and the plan
would plan their creation on every run.

`UpdateLabel` takes the **observed** remote name as its own argument and the desired label as the
body, so the request stays consistent with the state the plan was computed against. The desired name
always goes out as `new_name`, even when it is identical to the path, which keeps a rename and a
recolour one code path instead of two.

### Reading many repositories at once

```go
func (c *Client) ReadLabels(ctx context.Context, repos []config.Repo, concurrency int) ([]RepoLabels, error)
```

The read half of a run, bounded by `--concurrency` the same way enumeration is. Two properties the
caller depends on:

- **The order is the caller's**, not the order the parallel reads finished in. The result becomes a
  plan, and a plan that reshuffled between two identical runs is not one anyone can diff.
- **A repository that could not be reached is absent**, not present with an empty label set. The two
  would otherwise be indistinguishable, and the second reading is the dangerous one — an empty label
  set is exactly what a repository needing every label created looks like. The failure is recorded
  in `Failures` on the way past, which is what becomes the skipped outcome bit.

### The update request is hand-built

go-github's `EditLabel` sends the label's `name` field. GitHub's update endpoint reads **`new_name`**
and ignores `name`, so `EditLabel` would return a cheerful `200` having renamed nothing and the
drift would come back on every run. `UpdateLabel` therefore builds its own `PATCH` body:

| Field         | Always sent | Why                                                                     |
|---------------|-------------|-------------------------------------------------------------------------|
| `new_name`    | yes         | The rename, and the casing repair. Preserves the label `id`             |
| `color`       | yes         | The value the config asks for                                           |
| `description` | yes         | Descriptions are authoritative; omitting it leaves a stale one in place |

A rename is a `PATCH` and **never** a delete plus a create, because `new_name` preserves every issue
and pull-request association. Delete-and-recreate would strip the label from every issue that used
it — the damage `DeleteLabel` does, for a rename nobody asked to be destructive.

#### Verified against the live API

The whole feature rests on that preservation, so it is checked against GitHub itself rather than
inferred from the documentation. Against a scratch repository holding the stock labels, with one
issue labelled `bug`, `labelsync sync` with `renames: [{from: bug, to: "type: bug"}]`:

| Before                        | After                               |
|-------------------------------|-------------------------------------|
| label `bug`, id `11801741280` | label `type: bug`, id `11801741280` |
| issue `#1` carries `bug`      | issue `#1` carries `type: bug`      |
| —                             | `GET /labels/bug` → `404`           |

The id and the `node_id` are unchanged, which is *why* the association survives: the issue points at
the label, and the label was edited rather than replaced. The second run reports
`0 created · 0 updated · 0 deleted · 1 unchanged` and exits `0` — an applied rename finds no source
and emits nothing, so a `renames:` entry can stay in the config indefinitely. The full transcript is
in the pull request for [#45](https://github.com/specsnl/labelsync/issues/45).

### `DeleteLabel` is guarded at the call site

Deleting a label removes it from every issue and pull request that carried it, and nothing restores
that. It is called only in prune mode, on a candidate the user has been shown and has accepted. The
planner emits removal *candidates* and never decides which are deleted; this function is why that
separation exists.

### Label names are escaped into paths

`area/api` is an ordinary label name and two path segments if interpolated raw — go-github does not
escape, so `UpdateLabel` and `DeleteLabel` build their own escaped paths. Getting this wrong on a
`DELETE` destroys something else entirely. Owner and repository names need no escaping, having been
through `config.ParseRepoRef`'s character set.

## The ETag cache

`internal/github/cache.go` is the single most valuable optimisation in the tool: a conditional
request that comes back `304 Not Modified` **does not count against the primary rate limit**. Labels
change rarely, so hit rates are high — which is what makes `sync --dry-run` cheap enough to run on
every pull request.

`ListLabels` sends the cached `ETag` as `If-None-Match`; a `304` is answered from disk, and the
labels are never sent again. A `200` stores the new list and its `ETag`.

Entries live under `$XDG_CACHE_HOME/labelsync/`, one file per repository, and carry a **schema
version**. An entry written by a labelsync whose entry shape meant something else is discarded rather
than deserialised into the current struct — a new field with a zero value that reads as `false` is
exactly what the version exists to catch.

The file name is the SHA-256 of the lowercased `owner/repo`, not the reference itself. A repository
name is not a file name — it carries a slash, and a cache key that can escape its own directory is
not a cache key. Lowercasing keeps `Owner/Repo` and `owner/repo`, which address the same repository,
on one entry. The reference is stored *inside* the file, so an entry is still identifiable by eye.

Writes go to a temporary file and are renamed into place, so a run interrupted mid-write leaves the
previous entry rather than a half-written one for the next run to reject.

### `--no-cache` is the absence of a destination

Caching is on when the client has a cache directory, and `--no-cache` is that directory being empty.
There is no second flag threaded through the read path to keep in step with it, and a `nil` cache is
a working cache that never hits and never stores. Under test the directory is `t.TempDir()`, which is
also what stops a test run from touching the developer's real cache.

### A miss is never an error

Absent, unreadable, corrupt, truncated, or from another schema — all of it is a miss. Nothing in
`cache.go` returns an error to a caller. The cache is an optimisation, and an optimisation that can
fail a run is a liability; a miss costs one live request, and the entry is rewritten so the next run
is a hit again.

### Only a single-page list is served

An `ETag` covers the response it came from, which is **page one**. A repository with more than a
hundred labels can change beyond page one without page one's representation changing at all, so a
cached list served there would plan creates for labels that already exist. The entry records how many
pages the list took, and a multi-page one is always read live. More than 100 labels in one repository
is rare, so the optimisation keeps the case it is correct for.

For the same reason the header only ever goes out on page one: offering it for page two would ask
about a response it never described.

### Inspecting and clearing the cache

`store.go` is the same directory seen from outside the read path — what `labelsync cache` drives:

```go
func OpenStore(dir, root string) (*Store, error)
func (s *Store) Info() (CacheInfo, error)
func (s *Store) Clear() (CacheCleared, error)
```

`CacheInfo` carries the machine's values — `Bytes int64`, `Oldest time.Time`, the schema version —
and never a rendering. `1.2 MiB` and `3 days ago` belong in the table's columns; a struct field
pre-formatted into a string is a number a consumer can no longer compare.

**The bound is an argument, not an assumption.** `cache clear` takes a path that ultimately comes
from `XDG_CACHE_HOME` and then deletes what is in it, so `OpenStore` is given the root the
directory must sit under and refuses anything else with `ErrUnsafeCacheDir`. A check that derived
its own bound from the same environment variable the path came from would be checking nothing —
and passing it in is also what lets the tests point the whole thing at a temporary directory rather
than at the developer's real cache.

Containment is `filepath.Rel`, not a string prefix: `/tmp/cache-of-somebody-else` has `/tmp/cache`
as a prefix and is not inside it. The cache home *itself* is refused too — that is somebody's whole
cache directory, not ours to empty.

A second bound sits behind the first, because the guard cannot catch a path that is legitimately
under the cache home: only files whose names this tool writes are removed — a 64-character hex
entry, or the `.tmp-` file an interrupted write leaves behind. The directory stays, nothing is
recursed into, and an absent or already-empty cache is a no-op rather than an error.

### `github.Label` mirrors `plan.Label`

The planner declares its own `Label`, which is what keeps `internal/plan` free of
`internal/github` — it takes plain structs and does no HTTP. The client's `Label` has the **same
fields in the same order**, so bridging the two is a conversion:

```go
current := make([]plan.Label, len(labels))
for i, l := range labels {
    current[i] = plan.Label(l)
}
```

A mapping function would compile happily with a field forgotten; a conversion does not. A test pins
it, so a field renamed or reordered on either side breaks the build rather than surfacing later as a
silently empty description.

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
skipped exits `6`. See [Output & Exit Codes]({{< ref "./output.md#exit-codes" >}}).

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

---

The design record for the API surface, the cost model, and the conditional-request strategy is
[design.md § GitHub API surface](https://github.com/specsnl/labelsync/blob/main/docs/design.md#github-api-surface) and
[§ Request efficiency](https://github.com/specsnl/labelsync/blob/main/docs/design.md#request-efficiency).
