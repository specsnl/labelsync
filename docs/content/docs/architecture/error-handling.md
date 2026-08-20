---
title: Error Handling
weight: 2
---

Every way a `labelsync` run can fail has a sentinel error in `internal/labelsync/errors.go`.
Sentinels are always wrapped with `%w`, and are surfaced as a stable `error_kind` string in JSON
output.

## The wrapping rule

A call site with context to add never returns a sentinel bare, and never renders one with `%v` or
into a freshly constructed error — either breaks both `errors.Is` matching and `KindOf`:

```go
return fmt.Errorf("%w: %s", labelsync.ErrInvalidColor, raw)
```

Callers match with `errors.Is`; the JSON output layer calls `KindOf` to render `error_kind`:

```go
func KindOf(err error) string // stable kind string, or "" when no known sentinel is wrapped
```

The kind strings are a **public contract**. They may be added to, never renamed.

## Sentinels

| Sentinel                      | Kind string                  | Raised when                                                                    |
|-------------------------------|------------------------------|--------------------------------------------------------------------------------|
| `ErrConfigNotFound`           | `config_not_found`           | No config at `--config`, in the working directory, or under the XDG config dir |
| `ErrAmbiguousConfigFile`      | `ambiguous_config_file`      | Both `labels.yml` and `labels.yaml` exist in one directory                     |
| `ErrConfigExists`             | `config_exists`              | `init` was asked to scaffold over an existing config file, without `--force`   |
| `ErrUnsupportedConfigVersion` | `unsupported_config_version` | `version` is missing, or names a schema this binary does not understand        |
| `ErrEmptyConfig`              | `empty_config`               | The config parses but declares no labels                                       |
| `ErrDuplicateLabelName`       | `duplicate_label_name`       | Two labels share a name (compared case-insensitively, as GitHub does)          |
| `ErrDuplicateLabelColor`      | `duplicate_label_color`      | Two labels share a colour — uniqueness is global, not per repository           |
| `ErrInvalidColor`             | `invalid_color`              | A colour is not a 6-digit hex value, with or without a leading `#`             |
| `ErrInvalidLabelName`         | `invalid_label_name`         | A label name is empty, emoji only, or over the 50 code points GitHub accepts   |
| `ErrDescriptionTooLong`       | `description_too_long`       | A description exceeds 100 code points                                          |
| `ErrUnknownGroup`             | `unknown_group`              | A label, or `defaults.groups`, references an undefined group                   |
| `ErrAmbiguousGroupSource`     | `ambiguous_group_source`     | A group sets more than one of `org`, `user`, `repos`, `include_groups`         |
| `ErrCyclicGroup`              | `cyclic_group`               | `include_groups` forms a cycle                                                 |
| `ErrInvalidRepoRef`           | `invalid_repo_ref`           | A repository reference is not in `owner/repo` form                             |
| `ErrInvalidRename`            | `invalid_rename`             | A rename is malformed, chained, or targets a name no label declares            |
| `ErrNoToken`                  | `no_token`                   | The token resolution chain found no GitHub credential                          |
| `ErrInteractiveRequired`      | `interactive_required`       | An operation needs a prompt but stdin is not a TTY                             |
| `ErrRepoInaccessible`         | `repo_inaccessible`          | A repository is missing, archived, or outside the token's scopes               |
| `ErrUnsafeCacheDir`           | `unsafe_cache_dir`           | A cache command was pointed at a directory outside the XDG cache home          |
| `ErrMaxWaitExceeded`          | `max_wait_exceeded`          | A rate-limit backoff would sleep past the `--max-wait` ceiling                 |
| `ErrBudgetExhausted`          | `budget_exhausted`           | An apply needs more requests than the rate-limit budget has left               |

All config validation runs at load, before any network call, and fails fast.
`ErrRepoInaccessible` is the exception to fail-fast: it is reported per repository and the run
continues, with the process exit code reflecting that repositories were skipped.

## Adding a sentinel

Three edits, in the same change:

1. The `Err*` variable and a `KindOf` case in `internal/labelsync/errors.go`.
2. A row in the table above.
3. An entry in the `allSentinels` table in `internal/labelsync/errors_test.go`.

The test derives its expected set by parsing the package source for exported `Err*` variables, so a
sentinel that is declared but not tabled — or tabled after being removed — fails the build rather
than silently escaping `KindOf` and rendering an empty `error_kind`.

---

The sentinel table and the reasoning behind the `error_kind` contract are also in the design
record: [design.md § Error handling](https://github.com/specsnl/labelsync/blob/main/docs/design.md#error-handling).
