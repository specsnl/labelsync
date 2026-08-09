---
title: Planner
weight: 7
---

`internal/plan` turns "these labels are configured" and "these labels exist" into an ordered list of
changes. It is pure — nothing in it touches the network, and it never imports `internal/github`.

What has landed so far is the **vocabulary**: `Kind`, `Action`, and `Plan`, in `action.go`. The
reconciliation itself — `Compute` — is still
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

## Testing

Table-driven, in-package, and entirely about serialisation — there is no behaviour here to test yet:

| Test                                  | Guards                                                     |
|---------------------------------------|------------------------------------------------------------|
| the four `Kind` values                | The wire strings, which may be added to but never renamed  |
| `Action` round trip, ten shapes       | That marshal → unmarshal is lossless for every field       |
| exact marshalled JSON                 | Which keys `omitempty` drops, and which are always present |
| absent vs `""` vs `null` on unmarshal | The pointer contract, from the reading side                |
| `Plan` round trip                     | Grouping, plus repository and action order                 |
| marshalling the same plan twice       | Stable bytes — why `Repos` is a slice                      |
| `Plan{}`                              | That an empty plan is `{"repos":null}` and reads back nil  |

The round-trip comparisons use `reflect.DeepEqual`, which follows pointers: it compares the
pointed-to strings and, separately, nil against non-nil — exactly the distinction the pointer fields
exist to carry.

## Still to come

Everything that produces these types. `Compute(desired, current, mode, renames) Plan` — the rename
pass, squatter detection, the convergence rules, and prune — along with the pretty and NDJSON
renderings of a plan.
