# Labelsync

- **Binary:** `labelsync`
- **Module:** `github.com/specsnl/labelsync`

A Go CLI that synchronises GitHub issue/PR labels across a configured set of repositories, using a
local YAML file as the source of truth. Structure, conventions, and library choices deliberately
mirror [`specsnl/specs-cli`](https://github.com/specsnl/specs-cli).

## Commands

**Every command runs through [Task](https://taskfile.dev).** Never invoke `docker compose`, `go`,
or `npx` directly — the Taskfile wraps Docker Compose services that pin the Go, golangci-lint, and
Node versions, and set the shared build caches. Running the underlying tool directly uses whatever
happens to be installed locally, which is not what CI runs.

Run `task --list` for the full set. The ones used most:

| Command                | What it does                                                      |
|------------------------|-------------------------------------------------------------------|
| `task checkall`        | The full check sequence: `tidy:check`, `lint`, `test`, `md:check` |
| `task tidy:check`      | `go mod tidy -diff` — fails if `go.mod`/`go.sum` are untidy       |
| `task tidy`            | `go mod tidy`                                                     |
| `task lint`            | `golangci-lint run`                                               |
| `task lint:fix`        | `golangci-lint run --fix`                                         |
| `task test`            | `go test -race -tags=integration ./...`                           |
| `task test:update`     | Rewrite the golden files from the current output                  |
| `task md:check`        | markdownlint over every Markdown file                             |
| `task md:fix`          | Align Markdown tables, then apply autofixable rules               |
| `task build`           | Build the binary into the working directory                       |
| `task docs:serve`      | Hugo dev server with live reload on <http://localhost:1313>       |
| `task docs:preview`    | Build, then serve the static site over nginx on port 8080         |
| `task docs:build`      | Build the site into `docs/public/`                                |
| `task docs:mod:tidy`   | Tidy the Hugo module in `docs/`                                   |
| `task release:dry-run` | Local goreleaser snapshot, no publishing                          |
| `task demo:record:*`   | Re-record one demo GIF with VHS: `:labelsync`, `:init`            |
| `task dc:shell`        | Shell into the `go-builder` service                               |

### Local check sequence

Before opening a pull request, run:

```sh
task checkall
```

That is exactly `task tidy:check`, then `task lint`, then `task test`, then `task md:check`, in
that order — run them individually while iterating, and `checkall` before pushing.

Every step of the sequence reports; none of them writes. `tidy:check` runs `go mod tidy -diff`, so
an untidy `go.mod`/`go.sum` fails the check with the diff it would have applied rather than quietly
rewriting the tree mid-check. Run `task tidy` to apply it. CI runs the same check in the `Unit
tests` job.

`task build` runs `task lint` first, so a green build implies a green lint — but it does not run the
tests or the Markdown checks.

## Workflow

- **Always work on a branch off `main`.** Implementing an issue starts with
  `git checkout -b <type>/<short-slug> main` — never commit to `main` directly, even for a
  one-line docs fix. Changes reach `main` through a pull request.
- **One issue, one branch, one pull request.** Reference the issue in the PR description so it
  closes on merge.
- **"Implement milestone X" means "implement every remaining open issue in milestone X".** Start by
  listing them (`gh issue list --milestone "<X>" --state open`) — closed issues in the milestone are
  already done and are not reopened or redone.
- **Work out the order before writing any code, and state it.** Read every open issue in the
  milestone in full, not just its title, and build the order from three rules, applied in this
  sequence:
  1. **Blocked issues come after what blocks them.** Blocking is whatever the issues themselves say
     — a `Blocked by #N` / `Depends on #N` line, a task-list reference, or a Scope section that
     plainly needs another issue's output. Topologically sort on that; a cycle or an ambiguous
     dependency is a question for the user, not something to guess at.
  2. **A spike or research issue goes first**, ahead of the implementation issues it informs, unless
     something else blocks the spike — in which case that comes first. Its result must be *merged*
     before the issues that depend on it are implemented, because those issues are written against
     conclusions the spike has not reached yet. Do not stack implementation work on an unmerged
     spike branch, and do not start it in parallel; wait, then re-read the spike's outcome and let
     it shape the work. If the spike changes an issue's scope, say so rather than implementing the
     issue as originally written.
  3. **Otherwise, order for the smallest reviewable diffs** — foundations before the things built on
     them, so each PR in the stack stands on its own.
- **The rest of a milestone is delivered as a stack of pull requests**, built with the `gh-stack`
  skill
  (`gh stack`). One issue still gets one branch and one PR: the bottom branch of the stack sits on
  `main`, and each subsequent branch is based on the branch below it, so every issue keeps its own
  reviewable PR that closes it on merge. Never collapse a milestone into a single branch or PR.
  The stack is ordered bottom-to-top by the dependency order above, so a blocking issue is always
  below the issue it blocks. After each rebase or new layer, `gh stack push` to keep the PR bases
  correct.
- **Run `task checkall` before pushing.** For a stack, every branch in it must pass on its own — a
  layer that only goes green once a later layer lands is a sign the split is wrong.
- **Close the loop against the issue.** Before opening the PR, re-read the issue and check the work
  against its Scope and Done-when sections. Tick the checkboxes that the change actually satisfies,
  and say plainly in the PR what is left unticked and why. An unticked box is a scope decision, not
  an oversight to be discovered at review time.

## Conventions

- **Code changes require tests and docs.** Whenever code is added, removed, or updated, all of the
  following happen in the *same* change:
  - **Tests** — add or update `*_test.go` files covering the changed behaviour.
  - **Architecture docs** — update the relevant file(s) under
    `docs/content/docs/architecture/` if the change affects package structure, data flows, error
    handling, or any documented design decision.
    *This step is mandatory and must not be skipped, even for "internal" fixes.*
  - **User docs** — update `docs/content/docs/usage/` if the change affects anything user-facing:
    `commands.md` for commands and flags, `configuration.md` for config file syntax,
    `ci.md` for exit codes and the CI recipe, `getting-started.md` when the first-run path changes.
  - **README** — only when what labelsync *is*, or how it is installed, changes. It is an overview
    and a set of pointers; reference material belongs in `docs/content/`, not there.

- **Tests default to stdlib `testing`.** No test framework dependency so far, and nothing here
  needs one. A helper library is not forbidden — propose one when it genuinely improves the tests
  or the developer experience, not by reflex. Prefer table-driven tests; use
  `net/http/httptest` for the GitHub client and an injected clock for anything time-dependent.

- **Sentinel errors are always wrapped with `%w`.** Every way a run can fail has a sentinel in
  [internal/labelsync/errors.go](./internal/labelsync/errors.go). A call site with context to add
  never returns a sentinel bare, and never renders one with `%v` or into a freshly constructed
  error — either breaks both `errors.Is` matching and `labelsync.KindOf`, which feeds the stable
  `error_kind` field in JSON output:

  ```go
  return fmt.Errorf("%w: %s", labelsync.ErrInvalidColor, raw)
  ```

  Kind strings are a public contract: they may be added to, never renamed. **Adding a sentinel
  means adding a `KindOf` case, a row in the error table in
  [docs/design.md](./docs/design.md#error-handling), and an entry in the `allSentinels` test
  table** — the test parses the package source for exported `Err*` variables, so an untabled
  sentinel fails the build rather than silently rendering an empty `error_kind`.

- **User-facing output goes through `output.Writer`**, never `fmt.Println`. `log/slog` is a
  debug-only diagnostic channel on stderr, silent on a normal run, and is never used for reporting.

- **`internal/plan` and `internal/palette` never import `internal/github`.** They take plain structs
  and return plain structs. This is what keeps the interesting logic testable without HTTP mocking,
  and keeps a future `plan -o file` / `apply file` split a thin shell rather than a refactor.

## Package layout

```text
labelsync/
├── main.go                       # XDG init, cmd.Execute()
└── internal/
    ├── labelsync/                # configuration.go (XDG paths), errors.go (sentinels + KindOf)
    ├── cmd/                      # one file per Cobra command
    ├── config/                   # YAML load, validation, group resolution
    ├── github/                   # auth, client, enumeration, label CRUD, ETag cache, ratelimit/
    ├── plan/                     # Compute() — pure, no network — plus Action, candidates, rendering
    ├── palette/                  # Allocate() and the deterministic HSL candidate grid
    ├── apply/                    # executes a Plan in append or prune mode
    └── util/                     # exit/, output/
```

The full structure, with a file-level breakdown, is in
[docs/design.md](./docs/design.md#package-structure).

## Documentation

`docs/` is a [Hugo](https://gohugo.io) site on the [Hextra](https://github.com/imfing/hextra)
theme, published at <https://labelsync.specs.dev/>. The theme is consumed as a Hugo module from
`docs/go.mod` — a module of its own, so the root `go mod tidy -diff` never sees it. `task
docs:serve` renders the site locally.

Everything under `docs/content/` is published; `docs/design.md` sits outside it and is deliberately
not part of the site, so pages link to it by absolute github.com URL.

**Links between content pages go through `{{< ref >}}`**, keeping the path exactly as it is on
disk — `[Plan]({{< ref "./plan.md" >}})`, `[Commands]({{< ref "../usage/commands.md#prune" >}})`.
A plain `./plan.md` link 404s on the built site — Hugo does not rewrite `.md` links and Hextra
ships no render-link hook — while `task md:check` stays green. `ref` instead fails the build when
its target moves or disappears.

### The design record

| File                               | Description                                                |
|------------------------------------|------------------------------------------------------------|
| [docs/design.md](./docs/design.md) | The design plan: goals, algorithm, API surface, milestones |

`docs/design.md` is the *plan*; the pages below describe what has been built. As subsystems land,
their behaviour moves from the former into the latter, and each architecture page links back to the
design section it grew out of.

### Architecture — `docs/content/docs/architecture/`

| File                                                                          | Description                                      |
|-------------------------------------------------------------------------------|--------------------------------------------------|
| [_index.md](./docs/content/docs/architecture/_index.md)                       | Section index                                    |
| [overview.md](./docs/content/docs/architecture/overview.md)                   | Package structure, CLI tree, data flow           |
| [error-handling.md](./docs/content/docs/architecture/error-handling.md)       | Sentinels, the wrapping rule, `error_kind`       |
| [output.md](./docs/content/docs/architecture/output.md)                       | `output.Writer`, pretty vs NDJSON, exit codes    |
| [versioning.md](./docs/content/docs/architecture/versioning.md)               | The linker-injected `Version`                    |
| [palette.md](./docs/content/docs/architecture/palette.md)                     | The HSL candidate grid, legibility, determinism  |
| [configuration.md](./docs/content/docs/architecture/configuration.md)         | Resolution, YAML load, normalisation, validation |
| [plan.md](./docs/content/docs/architecture/plan.md)                           | The `Action` / `Plan` vocabulary and its JSON    |
| [authentication.md](./docs/content/docs/architecture/authentication.md)       | The token resolution chain                       |
| [github-client.md](./docs/content/docs/architecture/github-client.md)         | The go-github wrapper and its error taxonomy     |
| [rate-limiting.md](./docs/content/docs/architecture/rate-limiting.md)         | The write bucket, backoff, the countdown         |
| [apply.md](./docs/content/docs/architecture/apply.md)                         | Executing a plan, prune, the budget check        |
| [distribution.md](./docs/content/docs/architecture/distribution.md)           | The release pipeline, install channels, the cask |
| [library-decisions.md](./docs/content/docs/architecture/library-decisions.md) | Every direct dependency, and the rejects         |
| [demo.md](./docs/content/docs/architecture/demo.md)                           | The VHS tape, and how to re-record the GIF       |

### Usage — `docs/content/docs/usage/`

| File                                                               | Description                                    |
|--------------------------------------------------------------------|------------------------------------------------|
| [_index.md](./docs/content/docs/usage/_index.md)                   | Section index                                  |
| [getting-started.md](./docs/content/docs/usage/getting-started.md) | Install, export first, dry run, apply          |
| [configuration.md](./docs/content/docs/usage/configuration.md)     | Every field of the config file, and validation |
| [commands.md](./docs/content/docs/usage/commands.md)               | Every command and flag, renames, prune         |
| [ci.md](./docs/content/docs/usage/ci.md)                           | Exit codes, NDJSON, the workflow, the CI token |
