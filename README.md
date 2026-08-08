# specsnl/labelsync

Synchronise GitHub issue/PR labels across a set of repositories from a local YAML file.

`labelsync` is a reconciler, not a script: for each target repository it reads the current labels,
resolves the desired set, computes an ordered plan, and then applies it — or prints it, under
`--dry-run`. One `labels.yml` describes the labels you want; groups describe which repositories
should have them.

> **Work in progress.** Not ready for use yet.

## Install

Once released:

```sh
brew install specsnl/tap/labelsync
```

Or download a `tar.gz` for your platform from the
[releases page](https://github.com/specsnl/labelsync/releases) — Linux and macOS, amd64 and arm64.

Building from a checkout needs nothing but Docker and [Task](https://taskfile.dev):

```sh
task build
```

## Documentation

| Page                                                                 | Covers                                                       |
|----------------------------------------------------------------------|--------------------------------------------------------------|
| [Usage](./docs/content/docs/usage/_index.md)                         | Commands, global flags, where the config file is found       |
| [Overview](./docs/content/docs/architecture/overview.md)             | Package structure, the command tree, the reconciliation flow |
| [Output & Exit Codes](./docs/content/docs/architecture/output.md)    | stdout vs stderr, pretty vs NDJSON, the exit-code contract   |
| [Error Handling](./docs/content/docs/architecture/error-handling.md) | Sentinel errors and the `error_kind` contract                |
| [Versioning](./docs/content/docs/architecture/versioning.md)         | What version string each build produces                      |
| [Design plan](./docs/design.md)                                      | Goals, prior art, the algorithm, milestones                  |

## Contributing

Every command runs through [Task](https://taskfile.dev), which wraps the Docker Compose services
that pin the Go, golangci-lint, and Node versions — so a check runs the same way locally as it does
in CI. Run `task --list` for the full set.

```sh
task checkall   # lint, test, md:check — run this before opening a pull request
```

Conventions, workflow, and the house rules that reviews are held to: [AGENTS.md](./AGENTS.md).
