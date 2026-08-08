---
title: Configuration
weight: 6
---

`internal/config` turns a YAML file into the structs every later stage reads. It is deliberately
three separate concerns in three files, all of which have landed:

| File          | Concern                                            | Status |
|---------------|----------------------------------------------------|--------|
| `config.go`   | Find the file, parse it, normalise it              | landed |
| `validate.go` | The rules in the design's validation table         | landed |
| `resolve.go`  | Groups → selectors, `include_groups` cycles, globs | landed |

Nothing in the package touches the network, and nothing in `config.go` rejects a config: an
invalid colour parses into the struct exactly as written and is `validate.go`'s answer to give.
Splitting it that way keeps the error a user sees attached to the rule it broke rather than to a
decoder.

## Finding the file

```go
func Find(explicit string) (string, error)      // path only
func Load(explicit string) (*Config, error)     // Find + LoadFile
func LoadFile(path string) (*Config, error)     // read + Parse + Validate, records Config.Path
func Parse(data []byte) (*Config, error)        // decode + normalise, no I/O, no rules
func (*Config) Validate() error                 // every rule, offline, first failure wins
```

`Parse` is the one entry point that stops short of validating, which is what lets the
normalisation tests drive fragments that were never meant to be whole files. Everything a user can
reach goes through `LoadFile`, so a `*Config` that reaches the planner has passed every rule and
no later stage re-asks whether a colour is hex.

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

## Validation

`Validate` runs on an already normalised `Config`, entirely offline, and returns the **first**
broken rule wrapped in its sentinel. Every rule is a rule GitHub would enforce anyway — validating
locally turns a `422` discovered partway through a run into a message that names the line to fix,
before a single request is sent.

| Rule                                                       | Sentinel                      |
|------------------------------------------------------------|-------------------------------|
| `version` is present and equals `SchemaVersion`            | `ErrUnsupportedConfigVersion` |
| At least one label is defined                              | `ErrEmptyConfig`              |
| Label names are unique, compared case-insensitively        | `ErrDuplicateLabelName`       |
| Colours are unique globally, across every label            | `ErrDuplicateLabelColor`      |
| Colour is 6 hex digits                                     | `ErrInvalidColor`             |
| Description is ≤ 100 code points                           | `ErrDescriptionTooLong`       |
| Label name is 1–50 code points, measured after trimming    | `ErrInvalidLabelName`         |
| Label name is not emoji only                               | `ErrInvalidLabelName`         |
| Every group a label or `defaults.groups` names exists      | `ErrUnknownGroup`             |
| Every group has exactly one source                         | `ErrAmbiguousGroupSource`     |
| `include_groups` has no cycle                              | `ErrCyclicGroup`              |
| `repos` entries are `owner/repo`                           | `ErrInvalidRepoRef`           |
| Rename `to` targets a configured label; no chained renames | `ErrInvalidRename`            |
| Rename `from` is not itself a configured label name        | `ErrInvalidRename`            |

**The first failure wins**, rather than a collected list. A config file is edited by hand and
re-run in a second, and most of the errors in a collected list are knock-on effects of the first
one. Map iteration is sorted before it is walked, so a file with two problems always reports the
same one.

### The bounds count Unicode code points

`utf8.RuneCountInString`, never `len()`. A 100-emoji description is 400 bytes and 200 UTF-16
units, and GitHub accepts it; a byte-counting bound would reject it as though it were four times
too long — while passing every ASCII test written to check it. Not grapheme clusters either: a ZWJ
family emoji spends 5 of the 100. This was measured against the live API rather than assumed, and
overflow is a `422` rather than a truncation, so the bounds mirror the API instead of being
stricter than it.

Names are compared and measured **after trimming**, because GitHub stores names trimmed.
A name with a stray trailing space would otherwise produce a diff that never converges.

### Emoji are permitted in a name, never as the whole of one

GitHub rejects `🐛` with `name must contain more than native emoji`, and accepts `🐛 bug`. The
check strips emoji, variation selectors, zero-width joiners, and whitespace, and asks whether
anything is left — so `🐛 🐞` is as emoji-only as `🐛🐞` is. The range table behind it deliberately
errs towards *not* matching: a pictograph it misses lets an emoji-only name through to an
apply-time `422`, whereas one it wrongly matched would reject a name GitHub would have taken.

### Colour uniqueness is global

Two labels in groups that never share a repository could reuse a colour without ever colliding.
Global uniqueness is checked anyway, because it needs no group resolution, holds offline, and is a
rule a user can keep in their head. It is deliberately conservative and there is a comment in
`validate.go` saying so, because it looks like something worth "fixing".

### Renames are compared case-insensitively too

A label's identity is case-insensitive, so both rename rules use the same fold as
`ErrDuplicateLabelName`:

- **`to` targets a configured label** — the rename has to land on something the config declares,
  in any casing.
- **`from` is not itself a configured label** — which rejects a case-only rename such as
  `bug` → `Bug`. That is the right outcome rather than a limitation: casing drift needs no rename
  entry, because step 5 of the reconciliation algorithm converges it on its own.

Two further rules fall out of the same map: the same `from` renamed twice, and two renames onto
one name — the second of which would collide with the label the first just created. Chained
renames cannot survive the two rules above, since the middle name would have to be a configured
label and not one at once; the chain is checked explicitly all the same, so the message names the
chain rather than leaving the user to work out why the middle name is the one being complained
about.

## Resolution

`resolve.go` turns the `groups` section into **selectors**, not repository lists:

```go
func (c *Config) Resolve(authenticatedUser string) (*Resolution, error)

func (r *Resolution) Selectors() []Selector          // the enumeration work list
func (r *Resolution) SelectorsFor(group string) []Selector
func (r *Resolution) Matches(group string, repo Repo) bool
func (r *Resolution) Groups(repo Repo) []string      // the groups that select repo
func (r *Resolution) Desired(repo Repo) []Label      // the resolution rule
func (r *Resolution) Warnings() []Warning
```

A repository list would need the network, and the network is what would make composition, cycle
detection, and glob precedence untestable without an HTTP mock. So a `Selector` carries everything
an enumerator needs to *list* repositories — kind, owner, filters — and everything `Matches` needs
to decide whether a repository it was handed *belongs*. Enumeration itself lives in
`internal/github`.

`Repo` is a plain struct — owner, name, and the three facts the filters look at (`Archived`,
`Fork`, `Private`). That is what lets the same rule judge a repository the API listed and a
repository `--repo` named directly.

### One source, then composition

Every group sets **exactly one** of `org`, `user`, `repos`, or `include_groups`. Two is
`ErrAmbiguousGroupSource`, and so is none: a group that says nothing is no less ambiguous than one
that says two things, and the message names the group and what it set.

`include_groups` is flattened during resolution, so no kind survives for it — a composed group
resolves to the selectors of the groups it reaches, deduplicated by the group that defined them.
That makes `Selectors()` an enumeration work list with no repeated walks, however many composed
groups point at the same org.

Composition is a DFS with two states rather than one:

| Situation                           | Meaning   | Result                         |
|-------------------------------------|-----------|--------------------------------|
| A group reached twice, already done | A diamond | Contributes its selectors once |
| A group reached while still open    | A cycle   | `ErrCyclicGroup`               |

The cycle error names the whole chain — `a -> b -> c -> a` — because the group it was *noticed* at
is rarely the one the reader has to change. A group named in `include_groups` that does not exist
is `ErrUnknownGroup` here; a *label* naming a group that does not exist is `validate.go`'s to
report, since a label is not part of the graph and simply selects nothing.

### Filters

`include` is an allowlist and `exclude` a denylist applied after it, both through
`github.com/danwakefield/fnmatch` and both against the **repository name only**, never
`owner/repo`. An empty `include` means everything, which is why the two cannot collapse into one
pass. Matching is case-insensitive (`FNM_CASEFOLD`), because GitHub's repository names are:
`owner/Repo` and `owner/repo` address the same repository, so a case-sensitive pattern would
include or exclude it depending on how the API happened to spell it.

Filters — the globs, `skip_archived`, `skip_forks`, `visibility` — apply to `org` and `user`
selectors only. A `repos` entry names a repository outright and carries none of them: a filter that
silently dropped a repository the config asked for by name is a surprise, not a safety net.

### The `user` split

`user` has two endpoints, and which one applies is a property of the *config*, not of the API call:

| Value                  | Endpoint                            | Sees        |
|------------------------|-------------------------------------|-------------|
| The authenticated user | `GET /user/repos?affiliation=owner` | Private too |
| Anybody else           | `GET /users/{user}/repos`           | Public only |

`Resolve` takes the authenticated login and records the decision as `Selector.AuthenticatedUser`,
so `internal/github` does not have to ask who the token belongs to a second time. An empty login —
authentication has not happened yet — resolves to "somebody else", the conservative answer.

Requesting `visibility: private` for anybody else therefore selects nothing at all. That is not an
error, because the config is well-formed, and it is not silence either: it comes back as a
`Warning`, which `Resolve` collects rather than prints. This package has no writer, and only the
caller knows whether the run is pretty or NDJSON.

### The resolution rule

> For repository *R*, the desired set is every label whose resolved groups contain *R*. If no group
> resolves to *R*, *R* is never touched.

`Desired` returns `nil` for a repository no group selects, and that answer must never be confused
with an empty config: for an unselected repository it means *leave it alone*, and in `prune` mode
the difference between the two readings is every label in the repository. The test suite pins the
case directly for that reason.

## Testing

- **Validation** is table-driven over every rule, each with a valid case and an invalid one,
  asserting the specific sentinel through `errors.Is` and that `KindOf` still names it through the
  wrapping. Cases are whole config files run through `Parse` rather than hand-built structs,
  because the rules assume normalisation has happened. The boundary cases the API probe measured
  are in there by name — a 100-code-point emoji description and a 101, a 50-code-point name mixing
  emoji and ASCII, an emoji-only name, and a name that is only within bounds once trimmed.
- **Finding the file** is table-driven over every branch of the order, including both ambiguity
  cases and both not-found cases, with the working directory and `XDG_CONFIG_HOME` pointed at
  temporary directories. Every error case also asserts `KindOf` still names the sentinel through
  the wrapping.
- **Normalisation** is pinned by golden files: `testdata/*.yml` in, the normalised struct
  marshalled back to YAML out. A change to any defaulting or tidying rule shows up as a diff in
  the golden rather than as a subtly different plan several stages later.
- **Group resolution** is table-driven from YAML fragments, so a case reads as the config a user
  would have written: every source and every way of mixing them, composition and diamonds, the
  three shapes of cycle with the chain each one reports, glob precedence, the `skip_*` and
  `visibility` defaults, both sides of the `user` split — and, on its own, that a repository no
  group selects yields an empty desired set rather than an empty config.

```sh
task test:update   # rewrite the goldens, here and in every other golden package
```
