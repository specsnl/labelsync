---
title: Planner
weight: 7
---

`internal/plan` turns "these labels are configured" and "these labels exist" into an ordered list of
changes. It is pure — nothing in it touches the network, and it never imports `internal/github`.

What has landed is the **vocabulary** — `Kind`, `Action`, and `Plan`, in `action.go` — the
**reconciler** in `compute.go`, both modes and including the rename pass, and the **rendering** of a
plan in `render.go`. What the planner does not do is decide which removal candidates are actually
removed: that is a prompt, and it belongs to the command.

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

`Mode` is `append` or `prune`, and `append` is the default. The difference is the last step and
nothing else: see [Prune](#prune).

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

1. **Rename.** Each configured `{from, to}` is applied to a local copy of the repository's labels, so
   everything after this step reasons about the names the repository *will* hold. See
   [Renames](#renames) below.
2. **Partition.** Every remote label is matched against the configured set by name,
   *case-insensitively*: GitHub rejects two labels whose names differ only in case, so `Bug` and
   `bug` are one label wearing different casing rather than two labels. What matches is *matched*;
   what does not is *unconfigured*.
3. **Reserve.** Every configured colour is reserved, mapped to the configured label that owns it.
   Two configured labels may share a colour; the first in ascending name order is the one credited
   in a `Reason`, which keeps the text deterministic without changing the recolour.
4. **Recolour squatters.** An unconfigured label sitting on a reserved colour is displaced. In
   ascending name order, each one is given
   [`palette.Allocate(used, reserved)`](./palette.md#the-allocation-rule) and the result is fed back
   into `used` — the allocator is stateless, and feeding it back is the only thing stopping two
   squatters from being handed the same colour. `used` starts as the colours of the unconfigured
   labels that are staying put; matched labels contribute nothing, because they are about to hold
   their configured colour, which is already reserved.
5. **Converge.** The configured labels in ascending name order: `create` when the repository does
   not have one, `update` when colour, description, or casing differs, `noop` when nothing does.
6. **Prune.** Prune mode only: every label still unconfigured becomes a `delete` — a removal
   *candidate* — in ascending name order. See [Prune](#prune).

### Order

| Position | Actions                                                                           |
|----------|-----------------------------------------------------------------------------------|
| 1        | Renames, in the order the config declares them                                    |
| 2        | Squatter recolours, in ascending name order                                       |
| 3        | Creates, in ascending name order                                                  |
| 4        | Existing configured labels — updates and no-ops interleaved, ascending name order |
| 5        | Deletes — prune mode only, in ascending name order                                |

Recolours precede the create or update that claims the colour so that a run aborting mid-repository
never leaves a configured label sharing its colour with a label that was supposed to have moved off
it. GitHub permits duplicate colours and accepts either order, so this is about crash-consistency,
not validity.

Updates and no-ops share one run rather than being separated, because both are outcomes for a label
that already exists and a no-op is never sent to the API. Keeping them together means a report reads
straight down the configured labels in name order.

### Renames

A configured rename becomes an `update` carrying `NewName`, which the applier sends as a `PATCH`.
That is the whole reason renames are a first-class concept: a `PATCH` with `new_name` preserves the
label's issue and pull-request associations, and a delete followed by a create does not.

They run **first**, and the planner rewrites its own copy of the repository's labels as it goes, so
partitioning, squatter detection, and convergence all see the new names. Two things follow from that
ordering:

- A label that was going to be renamed onto a configured name is *matched*, not treated as an
  unconfigured squatter sitting on that label's colour. Without the rename pass it would be
  recoloured and the configured label created next to it.
- A rename plus a recolour of the same label collapses into two coherent steps —
  `bug → type: bug`, then a colour update against `type: bug` — rather than a delete and a create.

A rename is emitted **only** when `from` exists and `to` does not, both compared case-insensitively.
Either check failing skips the entry **silently**:

| Situation                         | Why skipping is right                                                     |
|-----------------------------------|---------------------------------------------------------------------------|
| `from` is absent                  | Already applied, or this repo never had the label — a no-op, not an error |
| `to` already exists               | The `PATCH` would be a `422 already_exists`, exactly as a create would    |
| `to` exists in a different casing | Same `422`: GitHub's name uniqueness is case-insensitive                  |

Silence is what makes the tool convergent. Re-running an applied rename finds no `from`, produces no
actions, and the second run of a converged repository is empty — which is the property the whole
design leans on.

The existence checks run against the **rewritten** view rather than a snapshot of what the API
returned. Chained renames (`a → b`, `b → c`) are
[rejected by config validation](./configuration.md), but the planner does not depend on that holding:
reading the live view means the second entry sees the name the first produced, so a chain resolves to
`c` in one run instead of two actions fighting over `b`. A half-empty entry is ignored for the same
defensive reason — renaming a label to `""` is not a request the API can honour.

The copy is a copy: the caller's `current` slice is never rewritten, because those are the labels
read back from the API and a caller may well still be using them.

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

### Prune

`append` — the default — never emits a `delete`. Whatever a repository holds beyond the configured
set is left where it is, recoloured if it is squatting on a reserved colour and otherwise untouched.
A test asserts this rather than trusting it, over fixtures that are separately checked to produce
deletes under `prune`, so a fixture that stops provoking one cannot quietly turn the assertion into a
test of nothing.

`prune` adds the last step and changes nothing else: every label still unconfigured after the rename
pass becomes a `delete` carrying `Reason: "unconfigured"`, in ascending name order, behind every
other action.

**The planner only ever produces candidates.** Which of them are actually deleted is decided by the
caller — an interactive `huh.MultiSelect`, or `--prune=all` — and `Compute` takes no part in that.
Keeping the split here is what makes prune semantics unit-testable with two slices and no terminal.
The `plan → select → apply` shape also means the selection can drop actions from a plan without the
plan having to be recomputed.

A recoloured squatter is **still a candidate**, and appears in the same plan twice: once as the
recolour, once as the removal candidate. The two answer different questions. The recolour is what has
to happen because a configured label wants that colour; the candidacy is what the user is asked
about. Dropping squatters from the candidate list would make the set of labels offered for removal
depend on which colour they happened to be sitting on — not a rule anyone could predict from the
config. If the candidate is accepted, the recolour was a wasted `PATCH` before the `DELETE`; that is
the price of the split, and it is one API call against a label that is being deleted anyway.

Candidate names are the ones the repository will hold **after** the rename pass, because the
unconfigured set comes from the rewritten view. A label renamed to a name the config still does not
mention is a candidate under its new name, which is also the name the delete will be applied to.

### A repository the config does not cover

An empty `desired` set means no group resolved to this repository, and `Compute` returns **no actions
at all** — not a plan that deletes everything the repository holds. It returns before the rename
pass, so an uncovered repository is not touched in either mode.

This is the tool's primary safety property, and it is the single most dangerous thing to get wrong:
the failure mode is a plan that offers to remove every label from a repository that was never meant
to be in scope. The guard sits on the desired set rather than on *why* it is empty, so a repository
listed in a group no label opts into is as uncovered as one no group names, and there is no path by
which a resolution bug upstream turns into a mass deletion here. A test runs both modes against a
repository full of labels, with a rename configured, and asserts the plan is empty.

The same rule holds one level up in
[config validation](./configuration.md): an empty config is rejected outright, so "no labels
configured anywhere" never reaches the planner at all.

## Rendering

`render.go` projects a plan into the two things a user can read. One call picks between them,
because which one lands is the writer's business and not the caller's:

```go
plan.Render(app.Out, p)
```

Both renderings come out of one pass over the plan. `plan.Diff(p)` returns the same
[`output.DiffData`](./output.md#a-diff-is-neither-a-table-nor-a-value) without writing it, for a test
or for a future `plan -o file` that wants the records with no writer in hand.

### The pretty diff

```text
specsnl/example-website
  ~  update    bug → type: bug
  ~  recolour  wontfix          #16a3c4                             (displaced by "type: bug")
  +  create    type: bug        #d73a4a  "Something isn't working"
  ~  update    type: feature    #0e8a16
  ~  update    priority: low             ""
  =  ok        priority: high
  -  delete    old-label                                            (unconfigured)

specsnl/example-platform
  =  ok  (3 labels, no changes)

2 repositories · 1 created · 4 updated · 1 deleted · 4 unchanged
```

A block per repository, indented two spaces under its heading, aligned by
[`output.RenderColumns`](./output.md#table-rendering) into six columns: gutter, verb, name, colour,
description, reason. Every row carries all six cells even when they are empty, so the reason column
starts at the same offset whether or not the rows above it set a description.

| Gutter | Verb       | Is                                                                |
|--------|------------|-------------------------------------------------------------------|
| `+`    | `create`   | `KindCreate`                                                      |
| `~`    | `update`   | `KindUpdate` — a rename, a recolour, a description change         |
| `~`    | `recolour` | `KindUpdate` changing only the colour **and** carrying a `Reason` |
| `-`    | `delete`   | `KindDelete`                                                      |
| `=`    | `ok`       | `KindNoOp`                                                        |
| `?`    | *the kind* | A kind this build does not know — only reachable from a plan file |

`recolour` is the [displaced squatter](https://github.com/specsnl/labelsync/blob/main/docs/design.md#steps):
nobody configured it, and the `Reason` next to it is what makes the change make sense. That is the
one thing separating it from an ordinary recolour of a configured label, which is why the verb is
derived from the reason rather than from the fields alone.

Three details that are decisions rather than formatting:

- **A cleared description renders as `""`.** A blank cell means *unchanged*, which is the one thing a
  `Description: new("")` is not.
- **A rename shows the transition** — `bug → type: bug` — because `Name` is the lookup key and
  `NewName` is what the API will store. Both matter to a reader.
- **The colour is rendered in the colour itself**, so a column of hex codes is scannable. It degrades
  with the stream like every other style.

The colour cell shows the **new** colour only, not `#1d76db → #0e8a16`. An `Action` carries the
change, not the state it replaces, and the renderer has no second source to diff against. If the
before-colour is wanted in the diff later, it has to arrive as a field on `Action` — filled in by
`Compute` — rather than be reconstructed here.

### Collapsing a converged repository

A repository whose actions are *all* no-ops — or which has no actions at all — collapses to one line:

```text
specsnl/example-platform
  =  ok  (3 labels, no changes)
```

On a converged run that is the difference between one screen and several hundred identical `= ok`
lines. Nothing is lost that a reader needed: the count says how many labels were checked, and the
NDJSON stream still carries every one of them by name.

### The NDJSON stream

One action per line, in plan order, then the summary — never a single document, so a run killed
halfway leaves everything written so far parseable:

```json
{"kind":"update","repo":"specsnl/example-website","name":"wontfix","color":"16a3c4","reason":"displaced by \"type: bug\""}
{"kind":"create","repo":"specsnl/example-website","name":"type: bug","color":"d73a4a","description":"Something isn't working"}
{"kind":"noop","repo":"specsnl/example-platform","name":"type: bug"}
{"kind":"summary","repositories":2,"created":1,"updated":4,"deleted":1,"unchanged":4}
```

The stream reports **every** action, no-ops included and nothing collapsed. Collapsing is a kindness
to a reader; a consumer filters for itself.

`summary` is not an action `Kind` and never appears on an `Action`. It shares the `kind` key so the
stream has one discriminator rather than two:

```sh
labelsync sync --dry-run --output=json | jq 'select(.kind == "summary")'
labelsync sync --dry-run --output=json | jq 'select(.kind != "summary" and .kind != "noop")'
```

Like the action kinds, `"summary"` is a wire contract: added to, never renamed. `plan.Summarise(p)`
returns the same counts to a caller that needs them without rendering — deciding an
[exit code](./output.md#exit-codes), for one: a dry run with anything but no-ops has found drift.

`Unchanged` counts no-ops. They are reported precisely so a clean run says *I looked* rather than
saying nothing at all — and they are still never sent to the API.

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
| `Compute`, thirty-one scenarios       | Creates, no-ops, every drift axis, squatters, action order |
| eight rename rows                     | Emit-then-rewrite, both skips, casing, and the ordering    |
| seven prune rows                      | Candidates and their order, and that deletes sort last     |
| append over delete-provoking fixtures | That `append` never emits a `delete`, in any shape         |
| an empty `desired`, both modes        | The safety property: an uncovered repository is untouched  |
| a prune applied, then recomputed      | Convergence: the pruned repository has nothing left to do  |
| a rename applied, then recomputed     | Convergence: the second run has nothing to do              |
| a chained rename                      | That the planner does not lean on validation rejecting it  |
| description clearing                  | That an omitted description emits `new("")`, not `nil`     |
| casing drift                          | That it travels as `NewName` against the current name      |
| two squatters                         | Name order, and that they never receive the same colour    |
| computing the same input six times    | Determinism, colours included                              |
| a reserved-and-used colour set        | That a recolour avoids both halves of the allocator input  |
| the whole candidate grid reserved     | That exhaustion still recolours, and says so in `Reason`   |
| computing twice over one `desired`    | That `Compute` does not reorder the caller's slice         |
| computing over one `current`          | That the rename pass rewrites a copy, not the caller's     |

The round-trip comparisons use `reflect.DeepEqual`, which follows pointers: it compares the
pointed-to strings and, separately, nil against non-nil — exactly the distinction the pointer fields
exist to carry.

The recolour colours are written into the table literally rather than recomputed by calling
`palette.Allocate` from the test. Recomputing them would assert only that the planner calls the
allocator; pinning them asserts that a second run over an unchanged repository does not churn a
colour, which is the property that matters.

`render_test.go` is external (`package plan_test`), because rendering is exercised through the same
surface a command uses. One fixture plan carries every action kind — a rename, a displaced squatter,
a create with a description, a cleared description, a no-op, and a delete — plus a second repository
that is fully converged, so the collapse is covered by the same fixture:

| Test                                | Guards                                                     |
|-------------------------------------|------------------------------------------------------------|
| pretty golden, plain and colour     | The whole rendering, and that plain output is degradation  |
| NDJSON golden                       | Field names, order, and the final summary object           |
| one object per line, every line     | The NDJSON contract, and that every line carries a `kind`  |
| the summary is the last object      | A consumer can treat it as the end of the run              |
| `Summarise` over three plans        | The counts, including an empty plan and an actionless repo |
| collapse, both shapes               | All-no-op and no-actions-at-all render the same one-liner  |
| the verb table                      | Each kind's gutter and verb, `recolour` included           |
| reasons appear                      | The annotation the field exists for                        |
| a cleared description shows as `""` | That it cannot be confused with an unchanged one           |
| no escapes off a terminal           | Styling never lands raw in a redirected file               |
| rendering twice                     | Byte-identical output — the rendering half of determinism  |

Goldens live in `internal/plan/testdata/` and are regenerated with `task test:update`.

## Still to come

The selection half of prune: the `--mode` and `--prune` flags, the interactive `huh.MultiSelect`
over the candidates, and the non-TTY guard that turns `prune` without `--prune=all` into an error
rather than a hung prompt. All of it sits above `internal/plan`, which hands it a plan and is
finished.
