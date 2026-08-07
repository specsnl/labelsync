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

| Command                | What it does                                        |
|------------------------|-----------------------------------------------------|
| `task checkall`        | The full check sequence: `lint`, `test`, `md:check` |
| `task lint`            | `golangci-lint run`                                 |
| `task lint:fix`        | `golangci-lint run --fix`                           |
| `task test`            | `go test -race -tags=integration ./...`             |
| `task md:check`        | markdownlint over every Markdown file               |
| `task md:fix`          | Align Markdown tables, then apply autofixable rules |
| `task build`           | Build the binary into the working directory         |
| `task release:dry-run` | Local goreleaser snapshot, no publishing            |
| `task dc:shell`        | Shell into the `go-builder` service                 |

### Local check sequence

Before opening a pull request, run:

```sh
task checkall
```

That is exactly `task lint`, then `task test`, then `task md:check`, in that order — run them
individually while iterating, and `checkall` before pushing.

`task build` runs `task lint` first, so a green build implies a green lint — but it does not run the
tests or the Markdown checks.

## Workflow

- **Always work on a branch off `main`.** Implementing an issue starts with
  `git checkout -b <type>/<short-slug> main` — never commit to `main` directly, even for a
  one-line docs fix. Changes reach `main` through a pull request.
- **One issue, one branch, one pull request.** Reference the issue in the PR description so it
  closes on merge.
- **Run `task checkall` before pushing.**
- **Close the loop against the issue.** Before opening the PR, re-read the issue and check the work
  against its Scope and Done-when sections. Tick the checkboxes that the change actually satisfies,
  and say plainly in the PR what is left unticked and why. An unticked box is a scope decision, not
  an oversight to be discovered at review time.

## Conventions

- **Code changes require tests, docs, and README updates.** Whenever code is added, removed, or
  updated, all of the following happen in the *same* change:
  - **Tests** — add or update `*_test.go` files covering the changed behaviour.
  - **Docs** — update the relevant file(s) under `docs/content/` if the change affects package
    structure, data flows, CLI flags, configuration, error handling, or any documented design
    decision. The architecture docs live at `docs/content/docs/architecture/`.
    *This step is mandatory and must not be skipped, even for "internal" fixes.*
  - **README** — update `README.md` if the change affects anything user-facing: commands, flags,
    config file syntax, output formats, or exit codes.

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
    ├── plan/                     # Compute() — pure, no network — plus Action and rendering
    ├── palette/                  # Allocate() and the deterministic HSL candidate grid
    ├── apply/                    # executes a Plan, prune prompts
    └── util/                     # exit/, output/, validate/
```

The full structure, with a file-level breakdown, is in
[docs/design.md](./docs/design.md#package-structure).

## Documentation

| File                                                                                                   | Description                                                   |
|--------------------------------------------------------------------------------------------------------|---------------------------------------------------------------|
| [docs/design.md](./docs/design.md)                                                                     | The design plan: goals, algorithm, API surface, milestones    |
| [docs/content/docs/architecture/_index.md](./docs/content/docs/architecture/_index.md)                 | Architecture section index                                    |
| [docs/content/docs/architecture/overview.md](./docs/content/docs/architecture/overview.md)             | Package structure, CLI tree, data flow                        |
| [docs/content/docs/architecture/error-handling.md](./docs/content/docs/architecture/error-handling.md) | Sentinel errors, the wrapping rule, and `error_kind` contract |

`docs/design.md` is the *plan*; `docs/content/docs/architecture/` describes what has been built. As
subsystems land, their behaviour moves from the former into the latter.
