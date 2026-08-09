---
title: Planner
weight: 7
---

`internal/plan` turns "these labels are configured" and "these labels exist" into an ordered list of
changes. It is pure — nothing in it touches the network, and it never imports `internal/github`.

What has landed is the **vocabulary** — `Kind`, `Action`, and `Plan`, in `action.go` — and the
**reconciler** in `compute.go`, in append mode. Renames and prune are still
[design.md § Reconciliation algorithm](https://github.com/specsnl/labelsync/blob/main/docs/design.md#reconciliation-algorithm).

## The types

```go
type Kind string // "create" | "update" | "delete" | "noop"

type Action struct {
    Kind        Kind    `json:"kind"`
    Repo        string  `json:"repo"`          // owner/repo
    Name        string  `json:"name"`          // current name (lookup key)
    NewName     *string `json:"new_name,omitempty"`
    Color       *string `json:"color,omitempty"`
    Description *string `json:"description,omitempty"`
    Reason      string  `json:"reason,omitempty"`
}

type Plan struct {
    Repos []RepoPlan `json:"repos"`
}

type RepoPlan struct {
    Repo    string   `json:"repo"` // owner/repo
    Actions []Action `json:"actions"`
}
```

All of it is plain data: no methods, no client handle, nothing that needs constructing. That is the
point. Writing a plan out is `json.Marshal` and reading one back is `json.Unmarshal`, so the
`plan -o file` / `apply file` split the design leaves room for is a serialisation shell rather than a
restructuring exercise.

`Name` is the label's *current* name, and the key an action is looked up under in the repository's
existing labels. A rename changes the name the API ends up storing — `NewName` — but never the name
used to find the label.

### The four kinds

| Kind     | Means                                                          |
|----------|----------------------------------------------------------------|
| `create` | The configured label does not exist in the repository          |
| `update` | A rename, a recolour, a description change, or any combination |
| `delete` | Prune mode only, and always last within a repository           |
| `noop`   | The configured label already matches — reported, never sent    |

The strings are a wire contract in the same sense the `error_kind` values are: they appear in NDJSON
output and in any plan file written out, so they may be added to and never renamed. A test pins all
four.

`noop` exists so that reporting can show a label as *checked* rather than silently omitting it. It is
never sent to the API.

## Why the optional fields are pointers

An update carries only the fields it changes, and a nil field means "unchanged". Plain strings cannot
carry that, because the design makes descriptions
[authoritative](https://github.com/specsnl/labelsync/blob/main/docs/design.md#configuration): an
omitted description in `labels.yml` means *clear it*, so the empty string is a value an update
legitimately sets. With a `string`, "leave the description alone" and "set the description to empty"
are the same zero value, and clearing a description would be indistinguishable from not touching one.

The pointers carry that distinction through serialisation, too — `omitempty` drops a nil field, while
a `*string` pointing at `""` marshals as an explicit `""` rather than disappearing:

| Field value | On the wire         | Read back as | Means                    |
|-------------|---------------------|--------------|--------------------------|
| `nil`       | *(key absent)*      | `nil`        | Leave the field alone    |
| `new("")`   | `"description":""`  | `new("")`    | Clear the description    |
| `new("x")`  | `"description":"x"` | `new("x")`   | Set the description to x |

An explicit `null` reads back as `nil`, the same as an absent key.

`new(expr)` is Go 1.26's — it takes the address of a *value* rather than of a zero, so a `*string`
needs no `ptr` helper and no temporary variable at the call site.

`config.Label.Description` is a plain `string` for the opposite reason, and the two are not
inconsistent: a config file has no "unchanged" to express, so there is nothing there for a pointer to
distinguish.

`Reason` is a plain `string` because it is reporting text with no authoritative empty value. Absent
and empty mean the same thing, so `omitempty` costs nothing.

## Why `Repo` is on the action

`Plan` already groups actions by repository, so `Action.Repo` is redundant inside a plan. It is there
anyway so a single action is self-describing: a log line, one NDJSON record, or an error about one
failed write carries its repository without the surrounding group having to be threaded along with
it.

## Why `Plan.Repos` is a slice

Ordering is part of what a plan *is*, and a map has none. Within a repository, actions are emitted in
the order they must be applied — renames, then squatter recolours, then creates, then updates, then
deletes — and the repositories themselves are reported in a stable order, so two runs over the same
input render identically. A test asserts that marshalling the same plan twice produces the same
bytes.

An empty plan marshals as `{"repos":null}`: `Repos` carries no `omitempty`, and a nil slice is what
"no repositories" is. A run resolving to no repositories is a normal outcome, not an error.

## `Reason`

`Reason` is reporting context and never affects what is sent to the API. Two things fill it in:

- **Displacement** — `displaced by "type: bug"` on a recoloured squatter. A recolour that looks
  arbitrary in a diff becomes obvious when annotated with which configured label displaced it.
- **Palette exhaustion** — the warning
  [`palette.Allocation.Exhausted`](./palette.md#exhaustion-is-a-warning-never-a-failure) reports.
  Turning it into a reason is the planner's half of that contract.

## `Compute`

```go
func Compute(repo string, desired []config.Label, current []Label, mode Mode, renames []config.Rename) RepoPlan
```

Pure: no network, no clock, no randomness. The same input always produces the same actions, colours
included — which is what stops a re-run from churning the squatters the last run recoloured.

### The signature, and where it departs from the design sketch

The design originally sketched
`Compute(desired []config.Label, current []github.Label, mode Mode, renames []Rename) Plan`.
[design.md](https://github.com/specsnl/labelsync/blob/main/docs/design.md#reconciliation-algorithm)
now carries the signature below instead; three things moved, each of them forced by something the
design says elsewhere:

- **`current []Label`, not `[]github.Label`.** The planner never imports `internal/github`, so the
  remote half of the input is a plain struct this package declares. Translating an API response into
  it is the caller's job, and the interesting logic stays testable with two slices and no HTTP mock.
- **`repo string`.** Both `Action` and `RepoPlan` carry the repository, and a pure function has
  nowhere else to get it from.
- **Returns `RepoPlan`, not `Plan`.** `Compute` reconciles *one* repository. A run assembles the
  `Plan` from the per-repository results, in the order its repositories were resolved.

`Mode` is `append` or `prune`. `mode` and `renames` are accepted but not yet acted on — the rename
pass and prune land next, and having the parameters now means they are a change to the function
rather than to every call site.

### `Label`

```go
type Label struct {
    Name        string // as the repository stores it, casing included
    Color       string // six-digit hex; a leading # and upper case are tolerated
    Description string // empty when it has none
}
```

GitHub does not distinguish an absent description from an empty one, so neither does this — which is
also why `config.Label.Description` can be a plain string while `Action.Description` cannot.

### What it does

1. **Partition.** Every remote label is matched against the configured set by name,
   *case-insensitively*: GitHub rejects two labels whose names differ only in case, so `Bug` and
   `bug` are one label wearing different casing rather than two labels. What matches is *matched*;
   what does not is *unconfigured*.
2. **Reserve.** Every configured colour is reserved, mapped to the configured label that owns it.
   Two configured labels may share a colour; the first in ascending name order is the one credited
   in a `Reason`, which keeps the text deterministic without changing the recolour.
3. **Recolour squatters.** An unconfigured label sitting on a reserved colour is displaced. In
   ascending name order, each one is given
   [`palette.Allocate(used, reserved)`](./palette.md#the-allocation-rule) and the result is fed back
   into `used` — the allocator is stateless, and feeding it back is the only thing stopping two
   squatters from being handed the same colour. `used` starts as the colours of the unconfigured
   labels that are staying put; matched labels contribute nothing, because they are about to hold
   their configured colour, which is already reserved.
4. **Converge.** The configured labels in ascending name order: `create` when the repository does
   not have one, `update` when colour, description, or casing differs, `noop` when nothing does.
5. **Prune.** Prune mode only, and still to come.

### Order

| Position | Actions                                                                           |
|----------|-----------------------------------------------------------------------------------|
| 1        | Renames — *still to come*                                                         |
| 2        | Squatter recolours, in ascending name order                                       |
| 3        | Creates, in ascending name order                                                  |
| 4        | Existing configured labels — updates and no-ops interleaved, ascending name order |
| 5        | Deletes — prune mode only, *still to come*                                        |

Recolours precede the create or update that claims the colour so that a run aborting mid-repository
never leaves a configured label sharing its colour with a label that was supposed to have moved off
it. GitHub permits duplicate colours and accepts either order, so this is about crash-consistency,
not validity.

Updates and no-ops share one run rather than being separated, because both are outcomes for a label
that already exists and a no-op is never sent to the API. Keeping them together means a report reads
straight down the configured labels in name order.

### Casing drift is a rename

A configured `type: bug` against a remote `Type: Bug` is an `update` whose `Name` is `Type: Bug` —
the key the label is found under — and whose `NewName` is `type: bug`. GitHub has no other way to
restyle a name. Unlike a configured rename, a case-only correction cannot collide: the only existing
label its target can match case-insensitively is the label being renamed itself.

### Descriptions are authoritative

A configured label with no description means the description is `""`, and converging on that clears
whatever the repository holds. That is why `Action.Description` is a `*string`: `new("")` is
*clear it*, and `nil` is *leave it alone*.

A create always carries both `Color` and `Description`, `""` included — there is no existing value
for it to leave alone.

### Colours compare normalised

One leading `#` is stripped and the rest lower-cased before any comparison, on both sides. `#D73A4A`
in a repository is not drift against `d73a4a` in the config, and a squatter is still detected through
it. Whether what remains is valid hex is
[config validation's](./configuration.md) question; here an unparseable colour simply never matches
and never enters a colour set.

## Testing

Table-driven and in-package. `action_test.go` is entirely about serialisation; `compute_test.go` is
the core suite, `(desired, current) → expected actions`:

| Test                                  | Guards                                                     |
|---------------------------------------|------------------------------------------------------------|
| the four `Kind` values                | The wire strings, which may be added to but never renamed  |
| `Action` round trip, ten shapes       | That marshal → unmarshal is lossless for every field       |
| exact marshalled JSON                 | Which keys `omitempty` drops, and which are always present |
| absent vs `""` vs `null` on unmarshal | The pointer contract, from the reading side                |
| `Plan` round trip                     | Grouping, plus repository and action order                 |
| marshalling the same plan twice       | Stable bytes — why `Repos` is a slice                      |
| `Plan{}`                              | That an empty plan is `{"repos":null}` and reads back nil  |
| `Compute`, fifteen scenarios          | Creates, no-ops, every drift axis, squatters, action order |
| description clearing                  | That an omitted description emits `new("")`, not `nil`     |
| casing drift                          | That it travels as `NewName` against the current name      |
| two squatters                         | Name order, and that they never receive the same colour    |
| computing the same input six times    | Determinism, colours included                              |
| a reserved-and-used colour set        | That a recolour avoids both halves of the allocator input  |
| the whole candidate grid reserved     | That exhaustion still recolours, and says so in `Reason`   |
| computing twice over one `desired`    | That `Compute` does not reorder the caller's slice         |

The round-trip comparisons use `reflect.DeepEqual`, which follows pointers: it compares the
pointed-to strings and, separately, nil against non-nil — exactly the distinction the pointer fields
exist to carry.

The recolour colours are written into the table literally rather than recomputed by calling
`palette.Allocate` from the test. Recomputing them would assert only that the planner calls the
allocator; pinning them asserts that a second run over an unchanged repository does not churn a
colour, which is the property that matters.

## Still to come

The rename pass, prune, and the pretty and NDJSON renderings of a plan.
