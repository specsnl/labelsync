---
title: Configuration
weight: 6
---

`internal/config` turns a YAML file into the structs every later stage reads. It is deliberately
three separate concerns in three files, and only the first has landed:

| File          | Concern                                           | Status  |
|---------------|---------------------------------------------------|---------|
| `config.go`   | Find the file, parse it, normalise it             | landed  |
| `validate.go` | The rules in the design's validation table        | planned |
| `resolve.go`  | Groups → repository sets, `include_groups` cycles | planned |

Nothing in the package touches the network, and nothing in `config.go` rejects a config: an
invalid colour parses into the struct exactly as written and is `validate.go`'s answer to give.
Splitting it that way keeps the error a user sees attached to the rule it broke rather than to a
decoder.

## Finding the file

```go
func Find(explicit string) (string, error)      // path only
func Load(explicit string) (*Config, error)     // Find + LoadFile
func LoadFile(path string) (*Config, error)     // read + Parse, records Config.Path
func Parse(data []byte) (*Config, error)        // decode + normalise, no I/O
```

`Find` resolves in this order:

1. `explicit` — the `--config` value, when one was given
2. `labels.yml` or `labels.yaml` in the working directory
3. `labels.yml` or `labels.yaml` under `ConfigDir()` — `$XDG_CONFIG_HOME/labelsync`

A `--config` value naming a **directory** is searched the same way as the other two, so
`--config ~/.config/labelsync` and `--config ~/.config/labelsync/labels.yml` both work.

Two outcomes are sentinels, wrapped with `%w` so `KindOf` can name them in JSON output:

| Situation                                     | Sentinel                 |
|-----------------------------------------------|--------------------------|
| Both spellings in the same directory          | `ErrAmbiguousConfigFile` |
| No config file at the path, or in any of them | `ErrConfigNotFound`      |

Ambiguity is checked **per directory, while searching it**. A working directory holding both
spellings is an error even though the XDG directory holds a perfectly good file — a run whose
config depends on which name the tool happened to look at first is worse than a run that stops.
This is the rule specs-cli applies to `project.yml` / `project.yaml`.

A YAML syntax error is not one of the run's failure modes and carries no sentinel. It is wrapped
with the file it came from, so `KindOf` returns `""` and the message still says where to look.

## Normalisation

`Parse` normalises before it returns, so no later stage has to ask whether a colour still has its
`#` or whether a label's groups have been filled in.

| Input                           | Normalised to                                         |
|---------------------------------|-------------------------------------------------------|
| `color: "#D73A4A"`              | `d73a4a` — one leading `#` stripped, then lowercased  |
| `name: "  type: bug  "`         | `type: bug` — trimmed                                 |
| `from` / `to` in a rename       | Trimmed, because they are matched against label names |
| A label with no `groups`        | A copy of `defaults.groups`                           |
| A group with no `skip_archived` | `true`                                                |
| A group with no `skip_forks`    | `true`                                                |
| A group with no `visibility`    | `all`                                                 |

Only **one** leading `#` is stripped: `##abc` normalises to `#abc`, which is invalid, which is the
point. Normalisation tidies, it never repairs — a repaired colour would silently apply a colour
the file does not name.

Inherited groups are **cloned per label** rather than aliased to the one `defaults.groups` slice.
Sharing a backing array across every defaulted label is the kind of thing that works until the
first stage that filters a label's groups in place.

### Why group defaults are applied while decoding

`skip_archived` and `skip_forks` default to `true`, which cannot be done after decoding: once the
struct is filled in, an explicit `skip_archived: false` and an omitted `skip_archived` are both
`false` and nothing can tell them apart. So `Group.UnmarshalYAML` decodes over a defaulted value
instead of over a zero one.

That alone is not quite enough. A group written as a bare key —

```yaml
groups:
  self:
```

— is a YAML null, and `yaml.v3` resolves a null to the zero value *without* consulting the
target's own `UnmarshalYAML`. The defaults would be zeroed straight back out. `Groups` therefore
has a decoder of its own that walks the mapping entry by entry and hands a null entry the defaults
directly. Both paths are pinned by tests.

### Descriptions

`Label.Description` is a plain `string`, not a `*string`, and that is a decision rather than an
omission. The design defines descriptions as **authoritative**: omitting one *clears* the remote
description. An omitted description and an explicitly empty one therefore describe the same
desired state, and nothing downstream would do anything different with the distinction.

If a future mode ever means "leave descriptions alone", that is the change that introduces the
pointer — not this one.

## Testing

- **Resolution** is table-driven over every branch of the order, including both ambiguity cases
  and both not-found cases, with the working directory and `XDG_CONFIG_HOME` pointed at temporary
  directories. Every error case also asserts `KindOf` still names the sentinel through the
  wrapping.
- **Normalisation** is pinned by golden files: `testdata/*.yml` in, the normalised struct
  marshalled back to YAML out. A change to any defaulting or tidying rule shows up as a diff in
  the golden rather than as a subtly different plan several stages later.

```sh
task test:update   # rewrite the goldens, here and in every other golden package
```
