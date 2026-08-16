---
title: Library Decisions
weight: 11
---

Every direct dependency, what it does, and — where the choice was not obvious — what it was chosen
over. The bar for adding one is deliberately high: `labelsync` is infrastructure that runs
unattended in CI, and each dependency is a thing that can break a release.

Two rules decide most of it:

1. **Prefer what [`specsnl/specs-cli`](https://github.com/specsnl/specs-cli) already uses.** The
   two tools are maintained by the same people and read by the same reviewers. A shared library is
   one set of idioms to know, one upgrade to do, and one place a bug is found.
2. **Prefer the standard library.** `log/slog`, `net/http/httptest`, and `testing` cover logging,
   the client tests, and the whole suite. No test framework, no assertion library, no logging
   facade.

## The direct dependencies

| Package                                 | Purpose                                       | Where it is used                    | Shared with specs-cli                            |
|-----------------------------------------|-----------------------------------------------|-------------------------------------|--------------------------------------------------|
| `github.com/spf13/cobra`                | The command tree                              | `internal/cmd`                      | yes                                              |
| `gopkg.in/yaml.v3`                      | Config parsing and export rendering           | `internal/config`                   | yes                                              |
| `charm.land/huh/v2`                     | The prune `MultiSelect`                       | `internal/cmd/prune.go`             | yes                                              |
| `charm.land/lipgloss/v2`                | Diff colouring, tables, the countdown         | `internal/util/output`              | yes                                              |
| `github.com/adrg/xdg`                   | Config and cache directory resolution         | `internal/labelsync`                | yes                                              |
| `github.com/danwakefield/fnmatch`       | Repository `include` / `exclude` globs        | `internal/config/resolve.go`        | yes                                              |
| `golang.org/x/sync`                     | `errgroup` for the bounded parallel reads     | `internal/github`                   | yes                                              |
| `github.com/google/go-github/v76`       | The GitHub REST client                        | `internal/github`                   | **new**                                          |
| `github.com/cli/go-gh/v2`               | Token resolution, and nothing else            | `internal/github/auth.go`           | **new**                                          |
| `github.com/lucasb-eyer/go-colorful`    | CIELAB and CIEDE2000 for colour distance      | `internal/palette`, `internal/plan` | **new (direct)** — already indirect via lipgloss |
| `github.com/charmbracelet/colorprofile` | Downsampling colour per output stream         | `internal/util/output`              | **new (direct)** — already indirect via lipgloss |
| `github.com/charmbracelet/x/term`       | `IsTerminal`, for the decisions colour cannot | `internal/util/output/tty.go`       | **new (direct)** — already indirect via lipgloss |

`log/slog` carries debug diagnostics, from the standard library — see
[Output § Debug logging]({{< ref "./output.md#debug-logging" >}}).

The three "new (direct)" rows are libraries the module already pulled in through lipgloss. Naming
them in `go.mod` does not add a dependency; it stops the code depending on a transitive one it
cannot see, which is what turns a lipgloss minor upgrade into a compile error somewhere unrelated.

## `go-github`, not a hand-rolled client

The surface this tool touches is a handful of endpoints — list, create, update, and delete labels,
plus repository enumeration and `GET /rate_limit` — and a hand-rolled client over `net/http` would
not be a large file. It was still the wrong call, for one reason: **typed rate-limit errors**.

```go
var rle *github.RateLimitError
var abuse *github.AbuseRateLimitError
```

Secondary rate limits are this tool's most likely failure mode — hundreds of sequential writes
against a limit of roughly 80 a minute. Distinguishing the primary hourly budget from the
content-creation limit decides which backoff to take and what to tell the user, and with go-github
that is a type switch rather than sniffing a status code and a header combination that GitHub is
free to change. The backoff logic stays clean and, more to the point, stays testable:
[Rate Limiting]({{< ref "./rate-limiting.md" >}}).

`resp.NextPage` for enumeration and the `Response` wrapper carrying the rate-limit headers on every
call are the bonus, not the reason.

The version is pinned in the import path — `go-github/v76` — so a major upgrade is a deliberate
import rewrite rather than something `go get -u` does to you.

## `go-gh`, for authentication only

`go-gh` is the `gh` CLI's own library, and it is imported for exactly one thing: reading a token
the user has already authorised. That covers the case worth covering — a developer with `gh auth
login` done never touches a PAT — and it is the only way to do it properly, because on macOS `gh`
keeps its token in the system keychain, where reading its config file finds nothing.

Its REST client is deliberately not used. Two HTTP clients with different error types would put
the rate-limit handling back where it started. The resolution chain the package feeds is in
[Authentication]({{< ref "./authentication.md" >}}).

## `huh`, and why prune has a form at all

Deleting a label removes it from every issue and pull request that carries it. A confirmation
prompt that answers "yes" for the whole list is the wrong shape for that decision — the answer is
usually "these three, not those two". `huh.MultiSelect` is that answer, and it is already the form
library specs-cli uses.

It is confined to one file, `internal/cmd/prune.go`, behind `App.Prompt` so tests replace it
wholesale, and it is never reached without a terminal on stdin — a prune with nobody to ask fails
with `interactive_required` rather than blocking a CI job. See
[Apply § Prune: the selection]({{< ref "./apply.md#prune-the-selection" >}}).

## `fnmatch`, not `path.Match`

`include` and `exclude` are globs over repository names. The standard library's `path.Match` would
almost do, and it treats `/` as a separator that `*` will not cross — a distinction that means
nothing for a repository name and would surprise anyone who wrote a pattern expecting shell
semantics. `fnmatch` gives the shell semantics, case-insensitively as GitHub's own names are, and
it is the matcher specs-cli already uses for `.specsverbatim`, so a pattern means the same thing in
both tools.

## `go-colorful`, for the distance function

The palette allocator has to answer "is this colour too close to that one?" in a way that matches
what an eye sees, which RGB distance does not. `go-colorful` provides CIELAB and CIEDE2000, both of
which are enough numerical work that a hand-rolled version would be a source of subtle wrongness
rather than a saving. Determinism matters here — the same config must always allocate the same
colour — and pinning the library is part of what guarantees it: [Colour Palette]({{< ref "./palette.md" >}}).

## Testing: the standard library

No test framework, no assertion helpers, no mocking library. The suite is table-driven stdlib
`testing`, `net/http/httptest` for the GitHub client, golden files for the renderings, and an
injected clock for everything time-dependent.

This is a default rather than a prohibition. A helper library is worth proposing when it genuinely
improves a test that is hard to read; it is not worth adding by reflex, and so far nothing here has
needed one. What makes it affordable is the package layout: `internal/plan` and `internal/palette`
never import `internal/github`, so the interesting logic is tested with plain structs and no HTTP
at all — [Overview § Why `plan` and `palette` are isolated]({{< ref "./overview.md#why-plan-and-palette-are-isolated" >}}).

## What was rejected

### A `gh` CLI extension

Shipping as `gh-labelsync`, invoked as `gh labelsync`, was seriously considered. It would delete
the auth code, need no release pipeline, and come preinstalled on GitHub-hosted runners. It was
rejected on four counts:

1. **The auth benefit is already captured.** The resolver falls back to `gh`'s own token, so the
   extension would save only the resolver itself — not enough to restructure distribution for.
2. **This is infrastructure, not a `gh` convenience.** Extensions suit interactive, gh-adjacent
   helpers. This tool reads a committed config file, runs on a schedule, and reconciles state —
   closer in shape to Terraform than to `gh pr checkout`.
3. **Consistency compounds.** One Taskfile pattern, one goreleaser config, one Homebrew tap across
   the Specs Go tooling.
4. **`gh label` already exists** as a built-in command group. `gh label` and `gh labelsync` being
   unrelated things is a permanent confusion no documentation fixes.

A thin `gh-labelsync` shim can be added later without touching this codebase.

### A plan file format

`plan -o file` / `apply file` is not implemented, and no serialisation library was added for it.
The design keeps the option open at no cost: `plan.Compute` is pure and returns plain structs, so
the split would be a thin shell rather than a refactor. Adding the format before anyone needs it
would mean maintaining a compatibility contract for a file nobody writes.

### A YAML schema library

Config validation is hand-written in `internal/config/validate.go`, not driven by a JSON Schema.
Every rule it enforces is one GitHub enforces anyway, and the value of checking locally is the
*message* — the line to fix, and a stable `error_kind` a CI job can branch on. A schema validator
produces neither. See [Error Handling]({{< ref "./error-handling.md" >}}).

---

The forward-looking version of this page — including the libraries considered before any of this
was written — is the [design plan](https://github.com/specsnl/labelsync/blob/main/docs/design.md#dependencies).
