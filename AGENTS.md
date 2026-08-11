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
| `task release:dry-run` | Local goreleaser snapshot, no publishing                          |
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
    commands, flags, config file syntax, output formats, or exit codes.
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
    └── util/                     # exit/, output/, validate/
```

The full structure, with a file-level breakdown, is in
[docs/design.md](./docs/design.md#package-structure).

## Documentation

| File                                                                                                   | Description                                                     |
|--------------------------------------------------------------------------------------------------------|-----------------------------------------------------------------|
| [docs/design.md](./docs/design.md)                                                                     | The design plan: goals, algorithm, API surface, milestones      |
| [docs/content/docs/architecture/_index.md](./docs/content/docs/architecture/_index.md)                 | Architecture section index                                      |
| [docs/content/docs/architecture/overview.md](./docs/content/docs/architecture/overview.md)             | Package structure, CLI tree, data flow                          |
| [docs/content/docs/architecture/error-handling.md](./docs/content/docs/architecture/error-handling.md) | Sentinel errors, the wrapping rule, and `error_kind` contract   |
| [docs/content/docs/architecture/output.md](./docs/content/docs/architecture/output.md)                 | `output.Writer`, pretty vs NDJSON, TTY detection, exit codes    |
| [docs/content/docs/architecture/versioning.md](./docs/content/docs/architecture/versioning.md)         | The linker-injected `Version`, and what each build produces     |
| [docs/content/docs/architecture/palette.md](./docs/content/docs/architecture/palette.md)               | The HSL candidate grid, legibility bounds, determinism          |
| [docs/content/docs/architecture/rate-limiting.md](./docs/content/docs/architecture/rate-limiting.md)   | The write bucket, backoff, `--max-wait`, and the countdown      |
| [docs/content/docs/architecture/plan.md](./docs/content/docs/architecture/plan.md)                     | The `Action` / `Plan` vocabulary and its JSON contract          |
| [docs/content/docs/architecture/apply.md](./docs/content/docs/architecture/apply.md)                   | Executing a plan, the prune selection, the startup budget check |

`docs/design.md` is the *plan*; `docs/content/docs/architecture/` describes what has been built. As
subsystems land, their behaviour moves from the former into the latter.
