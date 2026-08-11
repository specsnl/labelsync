---
title: Apply
weight: 9
---

`internal/apply` executes a [`plan.Plan`](./plan.md). It is the only code in `labelsync` that writes
to GitHub, and it is deliberately the smallest package that can be: the planner already decided what
to do and in what order, so all that is left is to do it, in that order, and to be honest about what
happened.

## The mode decides whether deleting is allowed at all

`Apply` takes the [`plan.Mode`](./plan.md) the plan was computed under, and it is the only thing that
distinguishes an append from a prune once the writing starts:

| Mode     | Deletes                                    |
|----------|--------------------------------------------|
| `append` | Refused, before the first write of the run |
| `prune`  | Executed, last in each repository          |

Any mode that is neither — a zero value, a string from somewhere unexpected — refuses as well. Only
`prune` opts in, so a caller that lost track of which mode it was in deletes nothing; the default has
to be the recoverable one.

**Which** candidates a prune's plan carries is not this package's business. The selection happens in
`internal/cmd`, ahead of the first write, and arrives here as a plan that has already been narrowed —
see [Prune § The selection](#prune-the-selection) below. Nothing here can tell a candidate that was
chosen from one that was never offered, which is precisely why the narrowing has to happen first.

Under `append` the guard is in this package and not only in the planner, because this is the package
holding the destructive call. A plan carrying a `delete` — read back from a file, or computed under
prune and handed over by a caller that forgot which mode it was in — is refused **before the first
write of the run**, so a refused apply has changed nothing. Refusing when the delete is *reached*
would mean a run that created six labels on its way to declining the job.

## Prune: the selection {#prune-the-selection}

Prune is three parts in three packages, and the split is the design:

1. `internal/plan` records every unconfigured label as a removal candidate and decides nothing.
   `Candidates(p)` names them, in plan order; `RetainDeletes(p, keep)` returns the plan minus the
   candidates that were not kept.
2. `internal/cmd` asks — a `huh.MultiSelect`, or `--prune=all` — and narrows the plan.
   See [Usage § Prune](../usage/commands.md#prune).
3. `internal/apply` executes what it is given.

`RetainDeletes` only ever **filters**. A candidate can be dropped between the report on stdout and
the writes, never introduced, so "the plan you were shown is the plan that ran, minus what you
declined" is a property of the code rather than a promise about it. Passing every candidate back,
which is what `--prune=all` does, returns the plan unchanged.

Repositories survive a selection that empties them. One whose candidates were all declined is still a
repository the run visited, and still applies its creates and updates.

## The order is a crash-consistency guarantee

Actions go out in exactly the order the planner emitted them: renames, then squatter recolours, then
creates, then updates, then deletes. That order is what makes every intermediate state coherent — see
[Planner § Ordering](./plan.md) — so a run killed halfway through a repository leaves it consistent
rather than with a configured label sharing a colour with a squatter that was supposed to have moved
off it.

Deletes are last for the same reason turned up a notch. A `DELETE` removes the label from every issue
and pull request that carried it and nothing restores that, so every recoverable action for the
repository has already been attempted by the time one goes out: a run cut short in the middle has lost
a rename or a recolour, not a label and all of its associations.

Nothing here reorders for throughput, and nothing writes to two repositories at once. That is not a
sacrifice: the [write bucket](./rate-limiting.md#proactive) paces writes at roughly one a second, so
parallelism would buy no wall-clock time and would cost the guarantee.

Within a repository, the **first** failure stops that repository. The steps after a failed rename
assume it landed, so carrying on would apply them against a name that is not there.

## `LabelPatch`, and why an update is not a whole label

An `Action` carries the change and not the state it replaces, so an update's three optional fields
are pointers and a nil one means *unchanged*. `github.LabelPatch` mirrors them field for field, and
`PatchLabel` marshals them with `omitempty` on pointers — a nil field disappears from the body and
GitHub leaves it alone; a pointer to `""` marshals as an explicit empty string and clears the
description.

Both halves matter, and each is a bug the other would cause:

- A recoloured squatter's action carries a **colour and nothing else**. Filling the rest in from the
  desired label would rename a label nobody configured and clear a description the config has no
  opinion about.
- A configured label with no description means the description is `""`, and clearing it is a thing
  the config authoritatively asks for. Plain strings with `omitempty` would silently turn "clear it"
  into "leave it".

`Client.UpdateLabel` is the whole-label form, for the one caller that has a complete desired label:
the `422 already_exists` path, where a create is
[reclassified as an update](./github-client.md) and there is no observed label to diff against.

## A failed repository is not a failed run

A repository that becomes unreachable partway through is abandoned, and the run continues with the
next one. The failure is already recorded in `github.Failures` by the time `Apply` sees it — that
happens inside `Client.Do` — so the end-of-run summary names it and the exit code carries
`exit.Skipped`, which is `4`.

Every other error ends the run: a cancelled context, or a rate-limit wait that would exceed
`--max-wait`. Neither is about one repository, and treating one as though it were would skip every
repository left in the set and report a run that mostly succeeded.

A partially applied run therefore has two things to say, and says both: the plan on stdout is what
it set out to do, and the `applied` record is what it managed.

## What was applied is not what was planned

`Report` is a typed record on stdout carrying `"kind": "applied"`, alongside the planner's
`"summary"`. It is a separate record because it answers a separate question — the two differ exactly
when a repository failed partway — and because a consumer discriminating on `kind` should not have
to guess which of two summaries it is holding.

```json
{"kind":"applied","repositories":2,"created":4,"updated":0,"deleted":0,"unchanged":0}
```

```text
applied: 4 created · 0 updated · 0 deleted · 0 unchanged
```

Every count is shown even at zero, including `deleted` on an append-mode run that could not have
produced one. The line is read by eye across runs, and a column that appears only when non-zero moves
the others — which is exactly what makes two runs hard to compare.

`Abandoned` names the repositories a failure cut short. Being in that list does not say whether
anything was written before the failure, which is the honest answer: the write that failed is the
only one whose outcome is known.

## The startup budget check

`sync` refuses to begin an apply that obviously cannot finish. `GET /rate_limit` is free, so the run
reads the budget before its first write and compares it with `apply.Writes(p)` — every action but the
no-ops, which never reach the API.

The refusal is `budget_exhausted`, and the message quotes both numbers and the reset time, because
"not enough budget" is not actionable and "30 writes to make, 25 requests left until 14:22Z" is.

The decision lives in `internal/cmd` rather than in the limiter or in this package.
`Limiter.Affordable` deliberately only *answers*: whether a half-finished apply is worse than none at
all is a policy question, and for this command the answer is yes.

Note the two thresholds are different mechanisms. This one refuses to start; the limiter's own
[threshold pause](./rate-limiting.md#proactive) stops mid-run when the budget drops to its last 20
requests, and waits for the window to turn over.

## Nothing here needs an HTTP mock to test

`Apply` takes a `Writer` — the three methods of `*github.Client` it uses — so the semantics that
matter here are testable against a fake that records calls in order: the emitted order, deletes going
out last, the abandonment of a failed repository, the refusal to delete under append, the no-op that
is never sent. `DeleteLabel` is on that interface unconditionally, which is what makes "append mode
never called this" an assertion rather than a method the fake cannot see. The end-to-end suite in
`internal/cmd` drives the real client against a **stateful** `httptest` fixture, which is what makes
the convergence assertion possible: a server that answered every listing with the same fixed body
could not tell an apply that worked from one that sent its requests into a void.

Convergence is asserted rather than assumed. Applying, then immediately planning again, must produce
a plan of nothing but no-ops — a create that quietly failed and a create that landed look identical
from one run.

### Except the one thing a label store cannot show

A rename is a `PATCH` rather than a delete plus a create *because* `new_name` keeps the label's
issue and pull-request associations, and that is invisible in a label list: both routes leave the
repository holding exactly the same labels. So the end-to-end fixture models the other side of it —
one issue whose labels a `PATCH` carries across and a `DELETE` takes off for good — and the suite
asserts what the issue still carries afterwards, alongside the ordering (the rename is the run's
first write) and the convergence (the second run writes nothing).

That is a model of GitHub, not GitHub. What the live API does with `new_name` is checked by hand
against a scratch repository, and the result recorded in the pull request that wired renames
through — see [GitHub client § the update request](./github-client.md).
